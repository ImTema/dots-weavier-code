# Samples

Five small services in three languages that call each other over gRPC,
HTTP and Pub/Sub. They give
dotweavier a known target: every hop between and inside the projects is listed
here, so a trace can be checked against what the code really does.

| Service     | Language          | Path                     | Serves                                        | Calls                                    |
|-------------|-------------------|--------------------------|-----------------------------------------------|------------------------------------------|
| `orders`    | Go                | `golang/orders`          | HTTP `:8080`: `POST /orders`, `GET /customers/{id}` | inventory (gRPC), pricing (gRPC), fulfillment (gRPC); publishes `order-placed` |
| `pricing`   | Go                | `golang/pricing`         | gRPC `:9090`: `pricing.v1.PricingService`     | orders (HTTP), shipping (HTTP); subscribes to `order-placed` |
| `fulfillment` | Go              | `golang/fulfillment`     | gRPC `:9092`: `fulfillment.v1.FulfillmentService` | pricing (gRPC), shipping (HTTP); subscribes to `order-placed` |
| `inventory` | Java 25, Quarkus  | `java/inventory`         | gRPC `:9091`: `inventory.v1.InventoryService` | orders (HTTP)                            |
| `shipping`  | Kotlin, Ktor      | `kotlin/shipping`        | HTTP `:8081`: `GET /rates`                    | orders (HTTP)                            |

## Topology

```
orders    ──gRPC──▶ inventory   Reserve, Release
orders    ──gRPC──▶ pricing     Quote
inventory ──HTTP──▶ orders      GET /customers/{id}
pricing   ──HTTP──▶ orders      GET /customers/{id}
pricing   ──HTTP──▶ shipping    GET /rates
shipping  ──HTTP──▶ orders      GET /customers/{id}
orders    ──Pub/Sub──▶ pricing  topic order-placed, subscription pricing-order-placed
orders    ──gRPC──▶ fulfillment Schedule
orders    ──Pub/Sub──▶ fulfillment  topic order-placed, subscription fulfillment-order-placed
fulfillment ──gRPC──▶ pricing   Quote (tax on the insurance premium)
fulfillment ──HTTP──▶ shipping  GET /rates
```

Every hop between projects:

| Hop  | Caller                                     | Callee                                         | Contract |
|------|--------------------------------------------|------------------------------------------------|----------|
| gRPC | Go `orders` `inventory.Client.Reserve`/`Release` | Java `inventory` `InventoryGrpcService`  | `java/inventory/src/main/proto/inventory/v1/inventory.proto` |
| HTTP | Java `inventory` `OrdersCustomerDirectory.tier` | Go `orders` `GET /customers/{id}`         | `golang/orders/api` |
| HTTP | Go `pricing` `shipping.Client.Rate`        | Kotlin `shipping` `GET /rates`                 | `RateResponse` in `kotlin/shipping/.../http/Routes.kt` |
| HTTP | Kotlin `shipping` `OrdersCustomerDirectory.find` | Go `orders` `GET /customers/{id}`         | `golang/orders/api` |
| gRPC | Go `orders` `pricer.Client.Price`          | Go `pricing` `server.Server.Quote`             | `golang/pricing/api/pricing/v1/pricing.proto` |
| HTTP | Go `pricing` `customers.Client.Tier`       | Go `orders` `GET /customers/{id}`              | `golang/orders/api` |
| Pub/Sub | Go `orders` `events.Publisher.OrderPlaced` | Go `pricing` `events.Subscriber.Handle`     | `golang/orders/api/events/v1/events.proto` (`OrderPlaced`, topic const in `topic.go`) |
| gRPC | Go `orders` `fulfiller.Client.Schedule`    | Go `fulfillment` `server.Server.Schedule`      | `golang/fulfillment/api/fulfillment/v1/fulfillment.proto` |
| Pub/Sub | Go `orders` `events.Publisher.OrderPlaced` | Go `fulfillment` `events.Subscriber.Handle` | `golang/orders/api/events/v1/events.proto` |
| gRPC | Go `fulfillment` `pricing.Client.Tax`      | Go `pricing` `server.Server.Quote`             | `golang/pricing/api/pricing/v1/pricing.proto` |
| HTTP | Go `fulfillment` `shipping.Client.Rate`    | Kotlin `shipping` `GET /rates`                 | `RateResponse` in `kotlin/shipping/.../http/Routes.kt` |

The inventory proto lives with its owner, the Java service. Go `orders`
generates its client from that file (`golang/orders/buf.gen.inventory.yaml`)
into `golang/orders/internal/inventorypb`. The event contract lives with its
publisher, `orders`; `pricing` imports it.

## Placing an order

1. `orders` validates the request and looks the customer up.
2. `orders` asks `inventory` to reserve the items. `inventory` asks `orders`
   for the customer's tier, because the tier caps how many units of one SKU a
   customer may reserve.
3. `orders` asks `pricing` for a quote. `pricing` asks `orders` for the tier
   (for the discount) and `shipping` for the delivery price. `shipping` asks
   `orders` for the region and tier.
4. If pricing fails, `orders` releases the reservation.
5. `orders` publishes `OrderPlaced` to Pub/Sub, asks `fulfillment` to schedule
   the shipment, notifies the customer and returns the order. A failed publish
   or schedule is logged and does not fail the order. `fulfillment` asks
   `shipping` for the rate and region, and `pricing` for the tax on the
   insurance premium.
6. `fulfillment` also consumes the event and schedules the order if the
   direct call was lost; it dedupes on the order ID.
7. `pricing` consumes the event and counts the customer's orders. From the
   fourth order on, a customer qualifies for a 10% loyalty discount.

Business rules are in each service's README.

## Running everything

Each service in its own shell, from this directory. Both Go services need a
Pub/Sub endpoint; locally that is the in-memory emulator in
`golang/tools/pubsub-emulator`, found through `PUBSUB_EMULATOR_HOST`:

```sh
(cd golang/tools/pubsub-emulator && go run .)
export PUBSUB_EMULATOR_HOST=localhost:8085   # in each Go service's shell
(cd java/inventory && ./mvnw package && java -jar target/quarkus-app/quarkus-run.jar)
(cd kotlin/shipping && ./gradlew run)
(cd golang/pricing && go run ./cmd/pricing)
(cd golang/fulfillment && go run ./cmd/fulfillment)
(cd golang/orders && go run ./cmd/orders)

curl -XPOST localhost:8080/orders \
  -d '{"customer_id":"c3","items":[{"sku":"a","unit_cents":1000,"qty":12}]}'
# {"order_id":"o-1","total_cents":15050}
```

The JVM projects build for JDK 25: inventory through Maven
(`maven.compiler.release`), shipping through Gradle toolchains. Run the
inventory jar with a JDK 25 or newer `java`. Start everything before calling
`POST /orders`. `orders` creates the topic; `pricing` creates its
subscription and, if it starts first, waits for the topic.

Place the same order for `c3` four times: the first three cost 15050, the
fourth 14210 once the loyalty discount applies.

| Response | When |
|----------|------|
| 201      | Order placed. |
| 400      | Malformed request, missing items, unknown customer. |
| 409      | Inventory refused: out of stock, unknown SKU, over the tier limit. |
| 502      | A downstream service is down or failing. |

## Tests

```sh
(cd golang/orders && go test ./...)
(cd golang/pricing && go test ./...)
(cd golang/fulfillment && go test ./...)
(cd java/inventory && ./mvnw test)
(cd kotlin/shipping && ./gradlew test)
```

No test needs another service running; the Pub/Sub tests use the client
library's in-process fake (`pstest`).

## Tracing with dotweavier

`index` picks up every project: the Go ones by `go.mod`, `java/inventory` by
`pom.xml` and `kotlin/shipping` by `build.gradle.kts`. Indexed from this
directory, the project names are their paths, such as `golang/orders`:

```sh
dotweavier index --project samples-monorepo
dotweavier trace path -A golang/orders::Handler.placeOrder -B golang/pricing::TierRule.Discount
# 1. Handler.placeOrder -call-> Service.Place -iface:sure-> Client.Price
#    -grpc /pricing.v1.PricingService/Quote-> Server.Quote -call-> Calculate
#    -call-> Best -iface:sure-> TierRule.Discount  (2/2 sure)
dotweavier trace path -A golang/orders::Service.Place -B golang/orders::Client.Reserve
# 1. Service.Place -iface:sure-> Client.Reserve  (1/1 sure)
dotweavier trace path -A golang/pricing::Server.Quote -B golang/pricing::Client.Rate
# 1. Server.Quote -iface:sure-> Client.Rate  (1/1 sure)
dotweavier trace path -A golang/orders::Service.Place -B java/inventory::StockLedger.reserve
# 1. Service.Place -iface:sure-> Client.Reserve
#    -grpc /inventory.v1.InventoryService/Reserve-> InventoryGrpcService.reserve
#    -call-> ReservationService.reserve -call-> StockLedger.reserve  (1/1 sure)
```

The gRPC call into `inventory` reaches the Java handler: the JVM extractor
reads `inventory.proto` and finds `InventoryGrpcService` by its supertype.
HTTP hops join a client request to the route serving its method and path.
Ktor and Go `net/http` clients and routes are read so far, so
`GET /customers/{id}` from `shipping` and from `pricing` reaches `orders`,
`GET /rates` from `pricing` and `fulfillment` reaches `shipping`, while the
calls from `inventory` still end at their clients:

```sh
dotweavier trace path -A kotlin/shipping::ShippingRates.quote -B golang/orders::Handler.getCustomer
# 1. ShippingRates.quote -iface:guess-> OrdersCustomerDirectory.find
#    -http GET /customers/{}-> Handler.getCustomer  (0/1 sure)
dotweavier trace path -A golang/pricing::Server.Quote -B golang/orders::Handler.getCustomer
# 1. Server.Quote -iface:sure-> Client.Tier
#    -http GET /customers/{}-> Handler.getCustomer  (1/1 sure)
dotweavier trace path -A golang/pricing::Server.Quote -B kotlin/shipping::ShippingRates.quote
# 1. Server.Quote -iface:sure-> Client.Rate
#    -http GET /rates-> Application.shippingModule -call-> ShippingRates.quote  (1/1 sure)
```

dotweavier does not link Pub/Sub yet either: a trace from
`golang/orders::Service.Place` to `golang/pricing::Subscriber.Handle` prints
`no path`. Both ends are indexed, matched by the topic `order-placed`, so a
path there is the test for `pubsub` edges.
