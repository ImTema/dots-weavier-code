# Go samples

Two projects that call each other, for testing dotweavier.

```
POST /orders (orders, entry point: cmd/orders)
  httpapi.Handler.placeOrder -> order.Service.Place
    -> customer.Store.Lookup
    -> order.Pricer.Price           interface hop, sure (main wires pricer.Client)
    -> PricingServiceClient.Quote   gRPC hop to pricing (api/pricing/v1/pricing.proto)
         server.Server.Quote
           -> server.TierSource.Tier  interface hop, sure (main wires customers.Client)
                customers.Client.Tier   HTTP hop back: GET /customers/{id} on orders
                  (orders) httpapi.Handler.getCustomer -> customer.Store.Lookup
           -> quote.Calculate -> discount.Best -> discount.Rule.Discount  interface hop, sure
                              -> tax.Amount
    -> notify.Send -> notify.Notifier.Notify  interface hop, guess (value travels in context)
```

Layout per project: `cmd/<name>` is the entry point, `api` holds the importable
contract, `internal/` holds transport (`httpapi`, `server`), adapters (`pricer`,
`customers`) and domains (orders: `order`, `customer`, `notify`; pricing:
`quote`, `discount`, `tax`). The modules import each other's `api` packages
through `replace` directives:

- `example.com/samples/pricing/api/pricing/v1`: gRPC contract (proto and generated code)
- `example.com/samples/orders/api`: HTTP request and response types

## Try it

```sh
dotweavier index --workspace samples/golang
dotweavier trace path -A orders::Handler.placeOrder -B pricing::TierRule.Discount  # grpc + 2 sure hops
dotweavier trace path -A orders::Service.Place -B orders::SMS.Notify               # guess hop
```

dotweavier does not detect HTTP hops yet, so traces stop at `customers.Client.Tier`.

Run for real (each in its own shell):

```sh
cd samples/golang/pricing && go run ./cmd/pricing
cd samples/golang/orders && go run ./cmd/orders
curl -XPOST localhost:8080/orders -d '{"customer_id":"c1","items":[{"sku":"a","unit_cents":1000,"qty":12}]}'
```

Tests: `go test ./...` in each project. Regenerate protos in `pricing/` with
`buf generate` (needs `buf`, `protoc-gen-go`, `protoc-gen-go-grpc` on PATH).
