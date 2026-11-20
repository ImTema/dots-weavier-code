# dotweavier

dotweavier is a CLI that indexes Go projects into one call graph and traces paths between symbols, across project boundaries too. A call into a gRPC client stub is joined to the server method that serves it. Calls through an interface are marked **sure** when the concrete type is known to reach the call, otherwise **guess**.

dotweavier never writes inside the projects it analyzes. All state lives in `~/.dotweavier`.

Status: prototype.

## Build

```sh
go build -o dotweavier ./cmd/dotweavier
```

## Usage

### 1. Index

Run it from a project folder, or pass `--project DIR`. dotweavier treats every directory under it that contains a `go.mod`, `pom.xml`, `build.gradle(.kts)` or `settings.gradle(.kts)` as a project. Java and Kotlin projects are read from source, without building them (`docs/adr/0007-jvm-extractor-from-source.md`). A project's name is its path relative to that folder.

```sh
dotweavier index                    # every project under the current folder
dotweavier index orders pricing     # only these projects
```

Re-run `index` after the source changes.

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
- `--format ui`: write a tree view to `~/.dotweavier/ui/trace-field.html`

## Try it

`samples/` holds small services in Go, Java and Kotlin that call each other over gRPC, HTTP and Pub/Sub. `samples/golang/README.md` lists every expected hop, so you can check a trace against it. All of them are indexed; the Go client of `inventory` links to its Java gRPC handler.

```sh
cd samples/golang && dotweavier index
```

dotweavier loads Go projects with the Go toolchain. A project whose modules cannot be downloaded fails with `no packages`. Run `go mod download` in that project first.

## Docs

- `docs/GLOSSARY.md`: domain terms
- `docs/adr/`: design decisions
