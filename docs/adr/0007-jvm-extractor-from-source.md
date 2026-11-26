# Java and Kotlin are extracted from source with tree-sitter, without building

Java (Maven) and Kotlin (Gradle) projects get one JVM extractor (`internal/domain/jvm.go`). It parses `.java` and `.kt` files with tree-sitter and emits the same nodes, edges and gRPC handlers as the Go extractor, so indexes, links, traces and the overview need no language-specific code.

We rejected compiler-based analysis (javac Trees, the Kotlin compiler, bytecode). Each of those needs a resolved classpath, which means running Maven or Gradle: that needs the network, takes minutes on large projects, fails on projects that do not build, and writes `target/` or `build/` inside the project, which dotweavier must never do. Kotlin also needs `kotlinc`, which is not on the machine; Gradle downloads it per project.

## How calls are resolved

With no classpath, a call is resolved only from declarations in the project:

- The receiver's type comes from a parameter, local, field or Kotlin property with a written type, or from the class it is constructed from (`val x = Foo(...)`, `var x = new Foo(...)`). A receiver whose type cannot be told this way (a chained call, a library value) is not linked.
- A method found on a class, or inherited from a project supertype, is a direct call. Every override in a project subtype, and every implementation behind an interface, is a **guess** (ADR 0005): there is no value tracking, so nothing is **sure** beyond direct calls.
- Supertypes are matched by simple name. A type whose own declaration does not parse still reaches its implementers. The bundled Kotlin grammar fails on `fun interface`, for example.
- Lambdas and anonymous classes are folded into the function that declares them.

## gRPC

Servers are matched through the `.proto` files in the project: a class whose supertype is `X` (Quarkus, Mutiny), `XImplBase` (grpc-java) or `XCoroutineImplBase` (grpc-kotlin), for a service `X`, serves each rpc `M` through its method `m`. JVM gRPC clients and Pub/Sub are not extracted yet.

## HTTP

A Ktor `HttpClient.get/post/...` call with a string URL is an HTTP call: interpolations become `{}`, and a leading base URL (an interpolation, or a scheme and host) is dropped. The Go extractor registers `net/http` `Handle`/`HandleFunc` routes with a constant pattern, and reads `net/http` client requests (`NewRequest[WithContext]` with a constant method, `Get`/`Head`/`Post`/`PostForm`) whose URL it rebuilds within the function from constants, `+`, `fmt.Sprintf`, `url.JoinPath`, `url.Parse` and `URL.JoinPath`/`String`; any other part is `{}` and a leading base URL is dropped. Both sides are keyed by method and path with path parameters reduced to `{}`, and `join` links them as `http` hops; a route without a method serves every method. A Ktor server route `get("/path") { … }` (any verb, string literal path, trailing lambda) is served by the function the lambda is folded into; `route("/prefix") { … }` nesting is not read. Other JVM clients and servers (MicroProfile REST, Spring) are not read yet.

## Field traces

`trace field` on a `.java` or `.kt` line runs the same two walks as the Go trace, over the tree-sitter syntax (`internal/domain/jvm_field.go`). The keys are expressions, locals and parameters (by name, in one flat scope per function), function results, and fields keyed `pkg.Type.field`. Calls follow the extractor's resolution. A Java record or a Kotlin primary constructor fills its components, and a getter or record accessor reads its field. A `getX()` on a type outside the project (a protobuf message) reads the field `Type.x`.

With no types:

- Values that only describe the datum are dropped by their shape: comparisons, `!`, predicate calls (`isBlank`, `equals`, ...), and plain reads that lead nowhere.
- A library call derives its result from its receiver, or from its one data argument.
- A library call on a local (`add`, `put`, `merge`, ...) stores its arguments into that local.
- A lambda passed to a library call receives the receiver's elements (`callback?`).

A JVM trace stays inside its project. A return flows to every caller, with no call-site matching. A project method's `this` is not tracked, so a field it reads is reached only through that field.

## Consequences

- JVM indexing takes milliseconds and works on projects that do not build.
- It misses calls whose receiver type is only known by inference, and returns no **sure** interface hops.
- The dependency `github.com/smacker/go-tree-sitter` needs cgo.
- `build`, `target`, `out` and dot-folders are skipped as build output, so generated sources in them are not indexed.
