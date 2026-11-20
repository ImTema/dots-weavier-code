# inventory (Java, Quarkus)

Holds stock and reserves it for orders. Serves `inventory.v1.InventoryService`
over gRPC on `:9091`; calls the Go `orders` service over HTTP to learn each
customer's tier. See `../../README.md` for how it fits with the other services.

## Contract

`src/main/proto/inventory/v1/inventory.proto` is the source of truth. Quarkus
generates the Java stubs at build time; Go `orders` generates its client from
the same file.

| RPC       | Does | Errors |
|-----------|------|--------|
| `Reserve` | Holds every item for the customer, or none. Returns a reservation ID. | `INVALID_ARGUMENT` bad line, `NOT_FOUND` unknown SKU, `FAILED_PRECONDITION` out of stock or over the tier limit, `UNAVAILABLE` orders unreachable |
| `Release` | Returns a reservation's stock. `released=false` if unknown or already released. | none |

## Rules

- Seed stock (`application.properties`): `a`=100, `b`=5, `c`=0.
- Per-SKU limit per order by tier: gold unlimited, silver 50, standard 20.
  Unknown customers are standard.

## Layout

```
src/main/java/com/example/inventory/
├── grpc/InventoryGrpcService.java      gRPC transport, maps exceptions to status codes
├── reservation/ReservationService.java domain: validation, tier limits, reserve, release
├── reservation/TierLimits.java
├── stock/StockLedger.java              domain: thread-safe stock, all-or-nothing reserve
├── stock/InventoryConfig.java          seed stock from config
└── customers/
    ├── CustomerDirectory.java          port: tier lookup
    ├── OrdersApi.java                  Quarkus REST client for GET /customers/{id}
    └── OrdersCustomerDirectory.java    adapter: 404 means standard, other failures throw
```

## Run and test

```sh
./mvnw test
./mvnw package && java -jar target/quarkus-app/quarkus-run.jar
```

Configuration through environment variables: `PORT` (gRPC, default `9091`) and
`ORDERS_URL` (default `http://localhost:8080`). Quarkus' own HTTP port is
`8092`. `InventoryGrpcServiceTest` starts Quarkus and replaces the orders call
with `StubCustomerDirectory`.
