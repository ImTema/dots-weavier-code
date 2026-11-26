# dotweavier

Trace call paths and value flows across Go, Java and Kotlin services, through gRPC, HTTP and Pub/Sub.

Status: early development. Commands, output and the storage format may change.

dotweavier indexes your projects into one call graph and traces paths between symbols, across project boundaries. A call into a gRPC client is joined to the server method that handles it. Interface calls are marked **sure** when the concrete type is known to reach the call, and **guess** otherwise ([ADR 0005](docs/adr/0005-sure-and-guess-interface-hops.md)).

dotweavier never writes inside the projects it analyzes. All state lives in `~/.dotweavier`.

## Install

Requirements:

- Go 1.27.2 or later (from `go.mod`)
- a C compiler, because the tree-sitter parser uses cgo

```sh
git clone https://github.com/ImTema/dots-weavier-code
cd dots-weavier-code
go install ./cmd/dotweavier
```

Putting `~/go/bin` on your `PATH`, updating and uninstalling are covered in [docs/cli.md](docs/cli.md#install).

## Try it on the samples

`samples-monorepo/` holds small Go, Java and Kotlin services that call each other over gRPC, HTTP and Pub/Sub. [Its README](samples-monorepo/README.md) lists every expected hop, so you can check each trace against it.

From the repo root:

```sh
dotweavier init samples-monorepo --index
```

A project's name is its folder path under the folder you added with `init`, so here the projects are `golang/orders`, `golang/pricing`, `java/inventory` and so on.

Trace the call paths between two symbols:

```sh
dotweavier trace path -A 'golang/orders::Service.Place' -B 'golang/pricing::Server.Quote'
```

The first of four paths:

```
1. golang/orders:internal/order/order.go:Service.Place -iface:sure-> golang/orders:internal/pricer/pricer.go:Client.Price -grpc /pricing.v1.PricingService/Quote-> golang/pricing:internal/server/server.go:Server.Quote  (1/1 sure)
```

Trace where a value on one line comes from and where it goes, and follow it into the services it calls, two services deep:

```sh
dotweavier trace field golang/orders:internal/order/order.go:109 --project samples-monorepo --follow 2
```

Line 109 is `orders` reserving stock. An excerpt of the lifecycle tree, which follows the reserver into the Java `inventory` service:

```
[seed] internal/order/order.go:109  resID, err := s.reserver.Reserve(ctx, c.ID, req.Items)  in Service.Place
├─ [field] order.Service.reserver : order.Reserver
│  └─ [read] internal/order/order.go:178  if err := s.reserver.Release(context.WithoutCancel(ctx), resID); err != nil {  in Service.release
│     └─ [arg] internal/inventory/client.go:47  func (c *Client) Release(ctx context.Context, reservationID string) error {  in Client.Release
│        └─ [grpc] java/inventory:.../grpc/InventoryGrpcService.java:52  /inventory.v1.InventoryService/Release
│           └─ [seed] java/inventory:.../grpc/InventoryGrpcService.java:53  public Uni<ReleaseResponse> release(ReleaseRequest req) {
```

The full output also has the origin tree and the hops into `pricing`, `fulfillment` and `shipping`.

## Use it on your code

```sh
dotweavier init ~/code --index      # add a folder of projects and index every project under it
dotweavier trace path -A 'orders::Service.Place' -B 'pricing::Server.Quote'
```

Symbols are addressed as `project::Type.Method`, `project:rel/path.go:Type.Method` or `project:rel/path.go:LINE`. Workspaces, `--dry-run` and every flag are in [docs/cli.md](docs/cli.md).

Go projects are loaded with the Go toolchain, so their modules must be downloadable. Run `go mod download` in a project that fails with `no packages`.

## Browser UI

```sh
dotweavier overview                          # browser UI at http://127.0.0.1:7357
dotweavier trace path -A … -B … --format ui  # open the paths in the overview
dotweavier trace field … --format html       # write one self-contained HTML file
```

## Supported languages and frameworks

| Language | Framework or library | Side | Supported |
|---|---|---|---|
| Go | gRPC (`google.golang.org/grpc`, generated code) | client and server | yes |
| Go | `net/http` (`Handle`/`HandleFunc`, `http.Client`, `NewRequest`) | client and server | yes |
| Go | Google Cloud Pub/Sub (`cloud.google.com/go/pubsub/v2`) | publisher and subscriber | yes |
| Java | gRPC server (Quarkus/Mutiny, grpc-java `ImplBase`) | server | yes |
| Kotlin | gRPC server (grpc-kotlin `CoroutineImplBase`) | server | yes |
| Kotlin | Ktor (`HttpClient`, `get("/path") { … }` routes) | client and server | yes |
| Java, Kotlin | gRPC clients | client | no |
| Java, Kotlin | Pub/Sub | publisher and subscriber | no |
| Java | Spring `RestClient`, `@HttpExchange`, Spring MVC | client and server | no |
| Java | MicroProfile REST | client and server | no |

Plain calls and interface calls are followed in all three languages. Java and Kotlin are parsed from source with tree-sitter and traced within their own project ([ADR 0007](docs/adr/0007-jvm-extractor-from-source.md)). Ktor routes nested in `route("/prefix") { … }` are not read.

## Docs

- [docs/cli.md](docs/cli.md): install, uninstall, full command and flag reference
- [docs/GLOSSARY.md](docs/GLOSSARY.md): domain terms
- [docs/adr/](docs/adr/): design decisions
- [samples-monorepo/README.md](samples-monorepo/README.md): the sample services and their expected hops

## Contributing

Run the tests with `go test ./...`. Issues are tracked as markdown files under `.scratch/`.
