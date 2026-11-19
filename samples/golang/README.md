# Go samples

Two small Go services, `orders` and `pricing`, that call each other (gRPC,
HTTP and Pub/Sub) and the JVM services in `../java/inventory` and `../kotlin/shipping`. They exist so
dotweavier has a known target to index and trace: every hop between and inside
the projects is listed below, so you can check a trace against what the code
really does. `../README.md` describes all four services together.

| Project   | Module                        | Entry point        | Serves                          | Calls                              |
|-----------|-------------------------------|--------------------|---------------------------------|------------------------------------|
| `orders`  | `example.com/samples/orders`  | `cmd/orders`       | HTTP on `:8080`                 | inventory and pricing over gRPC; publishes `OrderPlaced` |
| `pricing` | `example.com/samples/pricing` | `cmd/pricing`      | gRPC `PricingService` on `:9090` | orders and shipping over HTTP; subscribes to `OrderPlaced` |

`tools/pubsub-emulator` is a third module: a dev command serving an in-memory
Pub/Sub API for local runs. It is not part of the domain.

## Call flow

A client places an order. Orders reserves stock in Java inventory and asks
pricing for a quote, both over gRPC. Pricing asks orders for the customer's
tier and Kotlin shipping for the delivery price, both over HTTP, then applies
discount and tax. Orders then publishes `OrderPlaced`; pricing consumes it and
counts the customer's orders, which unlocks a loyalty discount.

```
POST /orders                                       [orders]
  httpapi.Handler.placeOrder
    -> order.Service.Place
         -> order.Validate
         -> customer.Store.Lookup
         -> order.Reserver.Reserve                 interface hop, sure
              inventory.Client.Reserve
                -> InventoryServiceClient.Reserve  gRPC hop: /inventory.v1.InventoryService/Reserve
                     (Java inventory, which calls GET /customers/{id} on orders)
         -> order.Pricer.Price                     interface hop, sure
              pricer.Client.Price
                -> PricingServiceClient.Quote      gRPC hop: /pricing.v1.PricingService/Quote
                     server.Server.Quote           [pricing]
                       -> server.TierSource.Tier   interface hop, sure
                            customers.Client.Tier
                              -> GET /customers/{id}  HTTP hop back to orders
                                   httpapi.Handler.getCustomer   [orders]
                                     -> customer.Store.Lookup
                       -> server.ShippingSource.Rate  interface hop, sure
                            shipping.Client.Rate
                              -> GET /rates        HTTP hop to Kotlin shipping
                                   (which calls GET /customers/{id} on orders)
                       -> quote.Calculate
                            -> discount.Rules
                            -> discount.Best
                                 -> discount.Rule.Discount   interface hop, sure
                                      TierRule.Discount | VolumeRule.Discount | LoyaltyRule.Discount
                            -> tax.Amount
         -> order.Reserver.Release                 only when pricing failed
         -> order.EventPublisher.OrderPlaced       interface hop, sure
              events.Publisher.OrderPlaced
                -> pubsub.Publisher.Publish        Pub/Sub hop: topic order-placed
         -> notify.Send
              -> notify.Notifier.Notify            interface hop, guess
                   Email.Notify | SMS.Notify

topic order-placed, subscription pricing-order-placed   [pricing]
  events.Subscriber.Run
    -> pubsub.Subscriber.Receive(callback)         closure passed to the library, guess
         events.Subscriber.Handle
           -> loyalty.Ledger.Record                dedupes on order ID
```

### Hops

| Hop | From | To | Why |
|-----|------|----|-----|
| gRPC | `orders` `pricer.Client.Price` | `pricing` `server.Server.Quote` | Generated client and server for `api/pricing/v1/pricing.proto`. |
| gRPC | `orders` `inventory.Client.Reserve`, `Release` | Java `InventoryGrpcService` | Client generated from `../java/inventory/src/main/proto/inventory/v1/inventory.proto`. |
| HTTP | `pricing` `customers.Client.Tier` | `orders` `httpapi.Handler.getCustomer` | `GET /customers/{id}`, typed by `orders/api`. Java inventory and Kotlin shipping call it too. |
| HTTP | `pricing` `shipping.Client.Rate` | Kotlin `GET /rates` | Typed by `rate` in `internal/shipping`, mirroring Kotlin `RateResponse`. |
| Pub/Sub | `orders` `events.Publisher.OrderPlaced` | `pricing` `events.Subscriber.Handle` | `OrderPlaced` in `orders/api/events/v1/events.proto`; topic `eventsv1.TopicOrderPlaced`. pricing binds its subscription to that const in `events.EnsureSubscription`. |
| Sure interface hop | `order.Service.Place` | `events.Publisher.OrderPlaced` | `main` passes an `*events.Publisher` into `order.NewService`. |
| Sure interface hop | `order.Service.Place` | `pricer.Client.Price` | `main` passes a `*pricer.Client` into `order.NewService`. |
| Sure interface hop | `order.Service.Place` | `inventory.Client.Reserve`, `Release` | `main` passes a `*inventory.Client` into `order.NewService`. |
| Sure interface hop | `server.Server.Quote` | `shipping.Client.Rate` | `main` passes a `*shipping.Client` into `server.New`. |
| Sure interface hop | `server.Server.Quote` | `customers.Client.Tier` | `main` passes a `*customers.Client` into `server.New`. |
| Sure interface hop | `discount.Best` | `TierRule.Discount`, `VolumeRule.Discount`, `LoyaltyRule.Discount` | `discount.Rules` builds the slice from these concrete types. |
| Guess hop | `events.Subscriber.Run` | its receive callback | A closure handed to `pubsub.Subscriber.Receive` is called inside the library; dotweavier assumes it runs. |
| Guess interface hop | `notify.Send` | `Email.Notify`, `SMS.Notify` | The `Notifier` is stored in a `context.Context` and read back with a type assertion, so no analysis can see which type arrives. |

## Layout

```
samples/golang/
├── orders/
│   ├── api/                  HTTP contract, imported by pricing
│   ├── api/events/v1/        event contract: events.proto, generated code, topic name
│   ├── buf.gen.yaml          generates api/ protos
│   ├── buf.gen.inventory.yaml  generates internal/inventorypb from the Java proto
│   ├── cmd/orders/           entry point
│   └── internal/
│       ├── httpapi/          HTTP transport (routes, handlers)
│       ├── order/            domain: Place, Validate; defines Pricer, Reserver, EventPublisher
│       ├── events/           Pub/Sub adapter implementing order.EventPublisher
│       ├── inventory/        gRPC adapter implementing order.Reserver
│       ├── inventorypb/      generated client for inventory.v1
│       ├── customer/         domain: in-memory Store, Lookup, IsVIP
│       ├── notify/           domain: Notifier interface, Email, SMS, Send
│       └── pricer/           gRPC adapter implementing order.Pricer
└── pricing/
    ├── api/pricing/v1/       gRPC contract: pricing.proto and generated code, imported by orders
    ├── cmd/pricing/          entry point
    └── internal/
        ├── server/           gRPC transport; defines TierSource and ShippingSource
        ├── customers/        HTTP client to orders, implementing server.TierSource
        ├── shipping/         HTTP client to Kotlin shipping, implementing server.ShippingSource
        ├── events/           Pub/Sub subscriber feeding the loyalty ledger
        ├── loyalty/          domain: Ledger of orders per customer
        ├── quote/            domain: Calculate, subtotal
        ├── discount/         domain: Rule interface, TierRule, VolumeRule, LoyaltyRule, Rules, Best
        └── tax/              domain: Amount, rate
tools/pubsub-emulator/        dev command: in-memory Pub/Sub on :8085
```

The two modules import each other's `api` package. Each `go.mod` points at the
other project with a `replace` directive, so nothing is fetched from a remote
module. Only `api` packages cross the boundary; everything else is `internal/`.

## Business rules

Seed customers live in `customer.Seed`:

| ID   | Tier     | Region | Phone        |
|------|----------|--------|--------------|
| `c1` | gold     | EU     |              |
| `c2` | silver   | US     | `+15550100`  |
| `c3` | standard | UK     |              |

Pricing is subtotal, then the single best discount, then tax on the discounted
amount:

- Volume: 500 cents off baskets of 10 or more units.
- Tier: gold takes 15%, silver takes 5%.
- Loyalty: 10% off once the customer has 3 earlier orders, counted from
  `OrderPlaced` events.
- Discounts do not stack; the largest wins.
- Tax: EU and UK 20%, US 7%. An empty region is untaxed; any other region is rejected.
- Shipping comes from Kotlin shipping and is added after tax, untaxed.
- Inventory may refuse the order: see `../java/inventory/README.md`.

Gold and silver customers with a phone get a "VIP order" message, others a
plain confirmation. Notification failures are logged and never fail an order.

## Running

Orders needs Java inventory, pricing needs Kotlin shipping, and both need
Pub/Sub, so start everything as `../README.md` shows. The Go ones:

```sh
cd samples/golang/tools/pubsub-emulator && go run .
export PUBSUB_EMULATOR_HOST=localhost:8085   # in each shell below
cd samples/golang/pricing && go run ./cmd/pricing
cd samples/golang/orders && go run ./cmd/orders
```

Orders creates the topic at startup. Pricing creates its subscription and, if
the topic does not exist yet, retries every 2 seconds until orders has made it.
Without `PUBSUB_EMULATOR_HOST`, the services use real Google Cloud Pub/Sub
with Application Default Credentials.

Flags:

- `pricing`: `-addr` (default `:9090`), `-orders` (default `http://localhost:8080`), `-shipping` (default `http://localhost:8081`), `-pubsub-project` (default `samples-local`)
- `orders`: `-addr` (default `:8080`), `-pricing` (default `127.0.0.1:9090`), `-inventory` (default `127.0.0.1:9091`), `-pubsub-project` (default `samples-local`), `-sms` (notify by SMS instead of email)
- `tools/pubsub-emulator`: `-port` (default `8085`)

Place an order:

```sh
curl -XPOST localhost:8080/orders \
  -d '{"customer_id":"c1","items":[{"sku":"a","unit_cents":1000,"qty":12}]}'
# {"order_id":"o-1","total_cents":12240}
```

The totals for 12 units at 1000 cents:

| Customer | Discount      | Tax    | Shipping     | Total   |
|----------|---------------|--------|--------------|---------|
| `c1`     | 1800 (gold)   | 2040   | 0 (gold)     | 12240   |
| `c2`     | 600 (silver)  | 798    | 425 (silver) | 12623   |
| `c3`     | 500 (volume)  | 2300   | 1250         | 15050   |
| `c3`, 4th order | 1200 (loyalty) | 2160 | 1250     | 14210   |

A gold discount on `c1` shows that the HTTP hop back to orders worked. An
unknown customer or an empty basket returns 400, an inventory refusal (out of
stock, unknown SKU, over the tier limit) returns 409, and a failing downstream
service returns 502. A failed publish is logged and does not fail the order;
events are at most once from orders' side and at least once on delivery, so
pricing dedupes on order ID.

Both services stop cleanly on Ctrl-C or SIGTERM.

## Testing

```sh
cd samples/golang/orders && go test ./...
cd samples/golang/pricing && go test ./...
```

The tests are table-driven and need no running services: `order.Place` with a
fixed-price stub and a fake reserver (including release after failed
pricing), the `GET /customers/{id}` handler, `quote.Calculate` (including the
loyalty threshold), the loyalty ledger, both HTTP clients against `httptest`
servers, and the publisher and subscriber against `pstest`, the Pub/Sub
client's in-process fake. dotweavier treats them as test nodes
and leaves them out of traces unless `--include-tests` is given.

## Tracing with dotweavier

From the repository root:

```sh
dotweavier index --workspace samples/golang

# Across projects: gRPC hop plus two sure interface hops.
dotweavier trace path -A orders::Handler.placeOrder -B pricing::TierRule.Discount
# 1. Handler.placeOrder -call-> Service.Place -iface:sure-> Client.Price
#    -grpc /pricing.v1.PricingService/Quote-> Server.Quote -call-> Calculate
#    -call-> Best -iface:sure-> TierRule.Discount  (2/2 sure)

# Guess hop: the notifier comes out of a context.
dotweavier trace path -A orders::Service.Place -B orders::SMS.Notify
# 1. Service.Place -call-> Send -iface:guess-> SMS.Notify  (0/1 sure)

# Sure hops into the clients of the JVM services.
dotweavier trace path -A orders::Service.Place -B orders::Client.Reserve
# 1. Service.Place -iface:sure-> Client.Reserve  (1/1 sure)
dotweavier trace path -A pricing::Server.Quote -B pricing::Client.Rate
# 1. Server.Quote -iface:sure-> Client.Rate  (1/1 sure)
```

dotweavier indexes Go only, so traces into Java inventory and Kotlin shipping
end at `inventory.Client.Reserve` and `shipping.Client.Rate`.

dotweavier does not detect Pub/Sub hops yet. Both ends are indexed, but a
trace from `orders::Service.Place` to `pricing::Subscriber.Handle` prints
`no path`; finding one is the test for `pubsub` edges:

```sh
dotweavier trace path -A orders::Service.Place -B orders::Publisher.OrderPlaced
# 1. Service.Place -iface:sure-> Publisher.OrderPlaced  (1/1 sure)
dotweavier trace path -A pricing::Subscriber.Run -B pricing::Ledger.Record
# 1. Subscriber.Run -iface:guess-> Subscriber.Run$1 -call-> Subscriber.Handle -call-> Ledger.Record  (0/1 sure)
```

dotweavier does not detect HTTP hops yet, so a trace stops at
`customers.Client.Tier` and does not continue into
`httpapi.Handler.getCustomer`. A trace from `pricing::Server.Quote` to
`orders::Store.Lookup` finding a path is the test for that feature.

## Regenerating the protobuf code

The generated Go files are committed. To regenerate them after editing a
proto, put `buf`, `protoc-gen-go` and `protoc-gen-go-grpc` on `PATH` and run
`buf generate`:

```sh
cd samples/golang/pricing && buf generate   # pricing.proto
cd samples/golang/orders && buf generate    # api/events/v1/events.proto
cd samples/golang/orders && buf generate --template buf.gen.inventory.yaml   # ../java/inventory/.../inventory.proto
```

## Known issue

The `orders` default for `-pricing` is `127.0.0.1:9090`, not `localhost:9090`.
On some machines gRPC's DNS resolver takes about 30 seconds to resolve
`localhost`, which made the first order time out.
