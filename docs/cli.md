# dotweavier CLI reference

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

## Index

A workspace is a name for a set of projects. `index` uses the `default` workspace unless you pass `--workspace NAME`, and creates the workspace if it does not exist. Its projects are found under its registered folders: dotweavier treats every directory under them that contains a `go.mod`, `pom.xml`, `build.gradle(.kts)` or `settings.gradle(.kts)` as a project. Java and Kotlin projects are read from source, without building them (`docs/adr/0007-jvm-extractor-from-source.md`). A project's name is its path relative to the workspace folder that contains it.

```sh
dotweavier init ~/code              # add ~/code to the default workspace; its projects are detected, not indexed
dotweavier index ~/code/orders      # index one project and its modules into the default workspace
dotweavier index ~/work/billing --workspace work   # into the workspace "work", created on first use
dotweavier init ~/code --index      # add ~/code and index every project detected under it
```

`--dry-run` on `init` or `index` prints the folder it would add and the projects it would detect or index, and writes nothing. Given a folder that holds projects but is not one, `index` fails and hints at `init DIR --index`.

If no folder of the workspace contains the project, `index` adds the project's parent folder to the workspace. A project has one index, shared by every workspace that contains it. Re-run `index` after the source changes, or press Sync in the overview.

dotweavier loads Go projects with the Go toolchain. A project whose modules cannot be downloaded fails with `no packages`. Run `go mod download` in that project first.

## Trace a call path

```sh
dotweavier trace path -A 'orders::Service.Place' -B 'pricing::Server.Quote'
```

Address forms:

- `project::Type.Method`
- `project:rel/path.go:Type.Method`
- `project:rel/path.go:LINE`

Flags:

- `--max-paths N` (default 10)
- `--include-tests`: also follow `_test.go` files and mock/fake packages
- `--format ui`: start the overview server (`127.0.0.1:7357`) and open the paths in it; Ctrl-C stops it
- `--verbose`: with `--format text`, also print progress and stats to stderr; ui and html always print them
- `--format html`: write a self-contained view to `~/.dotweavier/ui/trace-path.html` and open it

## Trace a value's lifecycle

```sh
dotweavier trace field orders:internal/order/order.go:94
dotweavier trace field java/inventory:src/main/java/com/example/inventory/reservation/ReservationService.java:24
dotweavier trace field kotlin/shipping:src/main/kotlin/com/example/shipping/http/Routes.kt:47
```

The file is looked up under `--project` (default: the current folder), then in the folders of `--workspace` (default: `default`). The trace then runs over the folder of that workspace that holds the file's project, so call paths and hops reach the indexed projects next to it, even when started inside one project's folder.

The line must compute a value, declare a parameter, or declare a field. The output is two trees: where the value comes from, and where it goes. Java and Kotlin files are traced from source within their own project (ADR 0007).

Flags:

- `--depth N`: limit the tree depth
- `--max-projects N`: how many projects to load when following fields shared across projects (default 10)
- `--include-tests`
- `--follow N`: follow gRPC, HTTP and Pub/Sub calls into the services that handle them, N services deep (at most 10 calls)
- `--format ui`: start the overview server (`127.0.0.1:7357`) and open the run in it, where hops can be followed and sources read; Ctrl-C stops it. Every run it opens, and every run traced or followed from the page, is kept as JSON in `~/.dotweavier/trace-field-history`, with the options it ran with, `--only` included. With the overview already running, the page opens there.
- `--format html`: write one self-contained file to `~/.dotweavier/ui/trace-field_<address>.html` and open it

## Overview server

`dotweavier overview` starts the browser UI at `http://127.0.0.1:7357`. The field trace page is at `/ui/trace-field.html`.

## Related

- [GLOSSARY.md](GLOSSARY.md): domain terms
- [adr/](adr/): design decisions
