# dotweavier

dotweavier is a CLI that indexes Go projects into one call graph and traces paths between symbols, across project boundaries too. A call into a gRPC client stub is joined to the server method that serves it. Calls through an interface are marked **sure** when the concrete type is known to reach the call, otherwise **guess**.

dotweavier never writes inside the projects it analyzes. All state lives in `~/.dotweavier`.

Status: prototype.

## Build

```sh
go build -o dotweavier ./cmd/dotweavier
```

## Install

To run `dotweavier` from any folder, install it into `~/go/bin` and put that folder on your `PATH`:

```sh
go install ./cmd/dotweavier
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
```

Re-run `go install ./cmd/dotweavier` after pulling changes.

## Uninstall

Remove the binary, and optionally all stored indexes and pages:

```sh
rm "$(go env GOPATH)/bin/dotweavier"
rm -rf ~/.dotweavier
```

If you added the `PATH` line only for dotweavier, delete it from `~/.zshrc`.

## Usage

### 1. Index

A workspace is a name for a set of projects; `index` uses the `default` workspace unless you pass `--workspace NAME`, and creates the workspace if it does not exist. Its projects are found under its registered folders: dotweavier treats every directory under them that contains a `go.mod`, `pom.xml`, `build.gradle(.kts)` or `settings.gradle(.kts)` as a project. Java and Kotlin projects are read from source, without building them (`docs/adr/0007-jvm-extractor-from-source.md`). A project's name is its path relative to the workspace folder that contains it.

```sh
dotweavier init ~/code              # add ~/code to the default workspace; its projects are detected, not indexed
dotweavier index ~/code/orders      # index one project and its modules into the default workspace
dotweavier index ~/work/billing --workspace work   # into the workspace "work", created on first use
```

If no folder of the workspace contains the project, `index` adds the project's parent folder to the workspace. A project has one index, shared by every workspace that contains it. Re-run `index` after the source changes, or press Sync in the overview.

### 2. Trace a call path between two symbols

```sh
dotweavier trace path -A 'orders::Service.Place' -B 'pricing::Server.Quote'
```

```
1. orders:internal/order/order.go:Service.Place -iface:sure-> orders:internal/pricer/pricer.go:Client.Price -grpc /pricing.v1.PricingService/Quote-> pricing:internal/server/server.go:Server.Quote  (1/1 sure)
```

Address forms:

- `project::Type.Method`
- `project:rel/path.go:Type.Method`
- `project:rel/path.go:LINE`

Flags:

- `--max-paths N` (default 10)
- `--include-tests`: also follow `_test.go` files and mock/fake packages
- `--format ui`: write an interactive view to `~/.dotweavier/ui/trace-path.html`

### 3. Trace a value's lifecycle

```sh
dotweavier trace field orders:internal/order/order.go:94
dotweavier trace field java/inventory:src/main/java/com/example/inventory/reservation/ReservationService.java:24
dotweavier trace field kotlin/shipping:src/main/kotlin/com/example/shipping/http/Routes.kt:47
```

The line must compute a value, declare a parameter, or declare a field. The output is two trees: where the value comes from, and where it goes. Java and Kotlin files are traced from source within their own project (ADR 0007).

Flags:

- `--depth N`: limit the tree depth
- `--max-projects N`: how many projects to load when following fields shared across projects (default 10)
- `--include-tests`
- `--follow N`: follow gRPC, HTTP and Pub/Sub calls into the services that handle them, N services deep (at most 10 calls)
- `--format ui`: write a tree view to `~/.dotweavier/ui/trace-field.html`; every run is kept in `~/.dotweavier/trace-field-history`

#### Example: follow a value into other services

In `samples/`, `orders` reserves stock and prices an order on one line (`internal/order/order.go:81`):

```go
resID, err := s.reserver.Reserve(ctx, c.ID, req.Items)
```

Index the samples, then trace that line and follow its calls two services deep:

```sh
dotweavier init samples
for p in samples/golang/orders samples/golang/pricing samples/java/inventory samples/kotlin/shipping; do dotweavier index $p; done
dotweavier trace field golang/orders:internal/order/order.go:81 --project samples --follow 2
```

The lifecycle tree crosses into Java `inventory` and Go `pricing`. Each `[grpc]` step is the handler, with the trace of the request it receives below it (excerpt):

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

The excerpt keeps only the hops and the steps that lead to them; the real tree has every step in between.

What to check against `samples/README.md`:

- `orders → inventory` (`Reserve`, `Release`) and `orders → pricing` (`Quote`) are followed into their handlers.
- Inside `pricing`, the customer ID reaches its HTTP clients `customers.Client.Tier` and `shipping.Client.Rate`. The trace stops there: Go HTTP clients are not linked yet, so `pricing → orders` and `pricing → shipping` are not followed.
- A handler reached from several steps is traced once; the other steps show `(traced at another step)`.

The same in the browser: run `dotweavier overview`, open `http://127.0.0.1:7357/ui/trace-field.html`, trace the line from its file viewer, and press **follow all hops** (or **follow ⇢** on one step).

## Try it

`samples/` holds small services in Go, Java and Kotlin that call each other over gRPC, HTTP and Pub/Sub. `samples/golang/README.md` lists every expected hop, so you can check a trace against it. All of them are indexed; the Go client of `inventory` links to its Java gRPC handler.

```sh
cd samples/golang && dotweavier index
```

dotweavier loads Go projects with the Go toolchain. A project whose modules cannot be downloaded fails with `no packages`. Run `go mod download` in that project first.

## Docs

- `docs/GLOSSARY.md`: domain terms
- `docs/adr/`: design decisions
