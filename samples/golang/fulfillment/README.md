# fulfillment

Plans the shipment of a placed order: warehouse, carrier, delivery price and
insurance. Go, gRPC on `:9092`.

## Contract

`api/fulfillment/v1/fulfillment.proto`: `fulfillment.v1.FulfillmentService/Schedule`.
`Schedule` is idempotent on `order_id`; a second call returns the first
shipment with `duplicate: true`.

| Hop | Direction | Caller | Callee |
|-----|-----------|--------|--------|
| gRPC | in | `orders` `fulfiller.Client.Schedule` | `server.Server.Schedule` |
| Pub/Sub | in | `orders` `events.Publisher.OrderPlaced` | `events.Subscriber.Handle` (subscription `fulfillment-order-placed`) |
| gRPC | out | `pricing.Client.Tax` | `pricing` `server.Server.Quote` |
| HTTP | out | `shipping.Client.Rate` | Kotlin `shipping` `GET /rates` |

`orders` calls `Schedule` right after publishing `OrderPlaced`. The event is the
fallback for a lost call: `Subscriber.Handle` goes through the same
`Server.Schedule`, so whichever arrives second is a duplicate.

## Rules

- Warehouse by the customer's region, which `GET /rates` returns: EU `AMS`,
  UK `LHR`, US `EWR`. Any other region fails with `FailedPrecondition`.
- Carrier: over 50 units `freight`; else a delivery price of 1500 cents or more
  `courier`; else `post`.
- Orders worth 20000 cents or more are insured at 1% of the total, plus the
  tax pricing charges on that premium in the customer's region.
- Invalid requests (no order, no customer, no units) fail with
  `InvalidArgument`; a failing downstream with `Unavailable`. The subscriber
  nacks only `Unavailable` failures, so they are redelivered.

## Layout

```
cmd/fulfillment     entry point: flags, wiring, graceful shutdown
internal/server     gRPC transport, maps plan errors to status codes
internal/plan       rules and the Planner, with the Rates and Taxes ports
internal/shipping   Rates adapter: HTTP client to Kotlin shipping
internal/pricing    Taxes adapter: gRPC client to pricing
internal/events     OrderPlaced subscriber
```

## Run and test

```sh
export PUBSUB_EMULATOR_HOST=localhost:8085
go run ./cmd/fulfillment        # -addr :9092 -pricing 127.0.0.1:9090 -shipping http://localhost:8081
go test ./...
```

Regenerate the gRPC code with `buf generate` (`protoc-gen-go` v1.36.12 and
`protoc-gen-go-grpc` v1.6.2 on `PATH`).
