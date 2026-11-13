# gofacon

gofacon is a CLI that indexes many code projects into one typed graph and traces call paths between symbols, including across project boundaries.

## Language

### Sources

**Project**:
One unit of source code, discovered by its module manifest (Go: a directory containing `go.mod`).
_Avoid_: repo, service, module

**Workspace**:
A directory that gofacon has been initialized on, plus the set of projects found beneath it. Workspaces nest: a parent workspace reuses the indexes of the projects inside it.
_Avoid_: monorepo, root

**Index**:
The stored graph of one project, kept outside the project in `~/.gofacon`. An index is **stale** when the project's source has changed since it was built.
_Avoid_: cache, snapshot

**Extractor**:
The language-specific part of gofacon that reads one project and emits nodes and edges in the core schema.
_Avoid_: parser, analyzer, provider

### Graph

**Node**:
A typed element of the graph, such as a function, method or type. Its identity is the language-qualified symbol name, not its file path.
_Avoid_: vertex, entity

**Edge**:
A typed, directed relation between two nodes, such as `call`, `grpc` or `pubsub`.
_Avoid_: link (reserved), relation

**Hop**:
One edge along a path, from one node to the next.
_Avoid_: step, jump

**Interface hop**:
A hop where the code calls through an interface or a function value (callback, closure), so the function that runs is decided at runtime. All other call hops are direct calls.
_Avoid_: dispatch, dynamic call

**Sure**:
An edge known to be real. Every direct call is sure. An interface hop is sure only when gofacon has tracked that a value of that concrete type actually reaches the call.

**Guess**:
An interface hop whose target was linked only because its type has a matching method, with no evidence that such a value reaches the call. A guess may be an invented call.
_Avoid_: approx, low-confidence, inferred

**Link**:
An edge that crosses from one project to another.
_Avoid_: cross-edge, bridge

**Test node**:
A node that comes from test code. In Go, that means `_test.go` files and configured mock packages. Test nodes are excluded from traces unless `--include-tests` is given.

### Querying

**Symbol address**:
How a user names a node on the command line: `project::Type.Method`, or `project:relative/path.go:Type.Method`. A line form, `project:relative/path.go:LINE`, names the innermost function whose declaration contains that line.
_Avoid_: selector, locator

**Trace**:
A query for the directed paths from symbol A to symbol B.

**Path**:
One ordered chain of nodes and edges returned by a trace. Paths are ranked shortest first.
_Avoid_: route, chain
