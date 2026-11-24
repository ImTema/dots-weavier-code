# dotweavier

dotweavier is a CLI that indexes many code projects into one typed graph and traces call paths between symbols, including across project boundaries.

## Language

### Sources

**Workspace**:
A name that keeps a set of projects apart from others, by the user's preference. A workspace has one or more registered folders, and its projects are the ones detected under them. `index` uses the `default` workspace unless `--workspace NAME` is given, and creates the workspace if it does not exist. A project's name is its path relative to the deepest folder of the workspace that contains it, so `golang/orders` for a workspace with the folder `samples`.
_Avoid_: project folder, root, monorepo

**Project**:
A single folder that is indexed on its own, recognised by its manifest: `go.mod` for Go; `pom.xml`, `build.gradle(.kts)` or `settings.gradle(.kts)` for Java and Kotlin. A folder with its own manifest inside a project is not part of that project's index: it is a **Module** of it.
_Avoid_: workspace, repo, service, monorepo

**Module**:
A project detected inside the folder of another project. It keeps its own index, but it is synced and removed together with the project that contains it, and shown beneath it. A module of a module belongs to the outermost project.
_Avoid_: subproject, nested project, child project

**Index**:
The stored graph of one project, kept outside the project in `~/.dotweavier`. An index is **stale** when the project's source has changed since it was built.
_Avoid_: cache, snapshot

**Invalid index**:
An index whose project no longer exists where it was found. It is kept, shown as invalid, and never re-indexed automatically.
_Avoid_: orphaned index, missing index

**Extractor**:
The language-specific part of dotweavier that reads one project and emits nodes and edges in the core schema.
_Avoid_: parser, analyzer, provider

### Graph

**Node**:
A typed element of the graph, such as a function, method or type. Its identity is the language-qualified symbol name, not its file path.
_Avoid_: vertex, entity

**Edge**:
A typed, directed relation between two nodes, such as `call`, `grpc`, `http` or `pubsub`.
_Avoid_: link (reserved), relation

**Hop**:
One edge along a path, from one node to the next.
_Avoid_: step, jump

**Interface hop**:
A hop where the code calls through an interface or a function value (callback, closure), so the function that runs is decided at runtime. All other call hops are direct calls.
_Avoid_: dispatch, dynamic call

**Sure**:
An edge known to be real. Every direct call is sure. An interface hop is sure only when dotweavier has tracked that a value of that concrete type actually reaches the call.

**Guess**:
An interface hop whose target was linked only because its type has a matching method, with no evidence that such a value reaches the call. A guess may be an invented call.
_Avoid_: approx, low-confidence, inferred

**Link**:
An edge that crosses from one project to another.
_Avoid_: cross-edge, bridge

**Test node**:
A node that comes from test code. In Go, that means `_test.go` files and configured mock packages. In Java and Kotlin, files under a `src/test*` source set. Test nodes are excluded from traces unless `--include-tests` is given.

### Querying

**Symbol address**:
How a user names a node on the command line: `project::Type.Method`, or `project:relative/path.go:Type.Method`. A line form, `project:relative/path.go:LINE`, names the innermost function whose declaration contains that line.
_Avoid_: selector, locator

**Trace**:
A query for the directed paths from symbol A to symbol B (`dotweavier trace path`).

**Field trace**:
A query for the full lifecycle of one value (`dotweavier trace field [project:]relative/path.{go,java,kt}:LINE`), printed as two trees: its origin (where it comes from) and its uses (every variable, conversion, struct field, call and return it may flow into). A field of a struct declared in any loaded project stands for that field in every object of the type, across projects, so a protobuf field written by a client in one project meets its server's reads in another. The trees show where the value may come from and go, not where it must. Unlike a trace, it analyses projects live and needs no index. A Java or Kotlin field trace stays inside its own project.
_Avoid_: variable trace, taint trace

**Path**:
One ordered chain of nodes and edges returned by a trace. Paths are ranked shortest first.
_Avoid_: route, chain

**Excluded node**:
A node the user bars from a trace: no returned path passes through it. Excluding a project excludes every node in it.
_Avoid_: blocked node, avoided node, filter

**Overview**:
The galaxy view of every indexed project: the top K nodes that are link endpoints or public functions, ranked by how many edges they have. Test nodes never appear in it.
_Avoid_: galaxy, map

### Running

**Server**:
The long-running dotweavier process (`dotweavier serve`) that serves the UI for every synced project and keeps their indexes from going stale. One runs per machine.
_Avoid_: daemon, ui, backend
