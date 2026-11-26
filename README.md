# dotweavier

dotweavier indexes Go, Java and Kotlin projects into one call graph and traces paths between symbols, across project boundaries. A call into a gRPC client is joined to the server method that handles it. Interface calls are marked **sure** when the concrete type is known to reach the call, and **guess** otherwise.

dotweavier never writes inside the projects it analyzes. All state lives in `~/.dotweavier`.

## Quick start

Requires Go and the Go toolchain on your `PATH`.

```sh
go install ./cmd/dotweavier                 # from the repo root
dotweavier init ~/code                      # add a folder of projects to the default workspace
dotweavier index ~/code/orders              # index one project
dotweavier trace path -A 'orders::Service.Place' -B 'pricing::Server.Quote'
```

Full install, uninstall and flag reference: [docs/cli.md](docs/cli.md).

## Usage

Index a project into a named workspace:

```sh
dotweavier index ~/work/billing --workspace work
```

Trace a call path between two symbols:

```sh
dotweavier trace path -A 'orders::Service.Place' -B 'pricing::Server.Quote'
```

```
1. orders:internal/order/order.go:Service.Place -iface:sure-> orders:internal/pricer/pricer.go:Client.Price -grpc /pricing.v1.PricingService/Quote-> pricing:internal/server/server.go:Server.Quote  (1/1 sure)
```

Trace what happens to a value on one line, and follow it into other services two hops deep:

```sh
dotweavier trace field golang/orders:internal/order/order.go:81 --project samples-monorepo --follow 2
```

Open results in the browser instead of the terminal:

```sh
dotweavier trace path -A 'orders::Service.Place' -B 'pricing::Server.Quote' --format ui   # overview at 127.0.0.1:7357
dotweavier trace field orders:internal/order/order.go:94 --format html                    # self-contained HTML file
```

## Demo

The `samples-monorepo/` folder holds small Go, Java and Kotlin services that call each other over gRPC, HTTP and Pub/Sub. `samples-monorepo/golang/README.md` lists every expected hop, so you can check a trace against it.

```sh
dotweavier init samples-monorepo
for p in samples-monorepo/golang/orders samples-monorepo/golang/pricing samples-monorepo/java/inventory samples-monorepo/kotlin/shipping; do dotweavier index $p; done
dotweavier trace field golang/orders:internal/order/order.go:81 --project samples-monorepo --follow 2
```

`orders` reserves stock and prices an order on this line. The trace follows the `Reserve`, `Release` and `Quote` calls into the handlers that serve them, in Java `inventory` and Go `pricing`. Excerpt:

```
followed 3 hops
[seed] internal/order/order.go:81  resID, err := s.reserver.Reserve(ctx, c.ID, req.Items)  in Service.Place
├─ [arg] internal/inventory/client.go:26  func (c *Client) Reserve(...)  in Client.Reserve
│  └─ [grpc] java/inventory:.../grpc/InventoryGrpcService.java:31  /inventory.v1.InventoryService/Reserve
│     └─ [seed] java/inventory:.../grpc/InventoryGrpcService.java:33  public Uni<ReserveResponse> reserve(ReserveRequest req) {
├─ [arg] internal/inventory/client.go:47  func (c *Client) Release(...)  in Client.Release
│  └─ [grpc] java/inventory:.../grpc/InventoryGrpcService.java:52  /inventory.v1.InventoryService/Release
└─ [read] internal/pricer/pricer.go:23  CustomerId: c.ID,  in Client.Price
   └─ [grpc] golang/pricing:internal/server/server.go:43  /pricing.v1.PricingService/Quote
      └─ [seed] golang/pricing:internal/server/server.go:43  func (s *Server) Quote(ctx context.Context, req *pricingv1.QuoteRequest) ...
         ├─ [arg] golang/pricing:internal/customers/client.go:27  func (c *Client) Tier(...)  in Client.Tier
         └─ [arg] golang/pricing:internal/shipping/client.go:32  func (c *Client) Rate(...)  in Client.Rate
```

The excerpt keeps only the hops and the steps that lead to them. The real output has every step in between.

Browser version: run `dotweavier overview`, open `http://127.0.0.1:7357/ui/trace-field.html`, trace the line from the file viewer, and press **follow all hops**.

Known limit: Go HTTP clients are not linked yet, so `pricing → orders` and `pricing → shipping` are not followed.

## Docs

- [docs/cli.md](docs/cli.md): install, uninstall, full command and flag reference
- [docs/GLOSSARY.md](docs/GLOSSARY.md): domain terms
- [docs/adr/](docs/adr/): design decisions
