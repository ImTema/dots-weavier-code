# Field traces are field-based taint over live SSA, over-approximating by design

`dotweavier trace field` shows the lifecycle of one value as two trees. **Origin** runs back to where the value comes from: literals, parsing, other projects' gRPC clients. **Lifecycle** runs forward through every local the value is copied into, every conversion, every struct field it is stored in and every place that field is read back.

The analysis is field-based taint propagation over SSA, run live rather than from the index. A struct field is one location for every object of its type, keyed by package path, type and field name. So a protobuf field written by a gRPC client in one project is the same location its server reads in another. Projects that import a reached field's package are loaded until nothing new is reached. Only named structs declared in the projects are shared locations: a field of a library struct (`uuid.NullUUID.UUID`) or an anonymous one stays part of its object, otherwise every UUID in a project would meet in `uuid.NullUUID.UUID`.

Keying a field by type rather than by object is what lets one trace cross projects. The cost is over-approximation: the trees show where the value may come from and go, not where it must.

Calls are matched one level deep. A function entered from a call site (forward through a parameter, backward through a return) leaves only through that site. A function reached on its own (seed, field, capture) may leave through every call site.

Java and Kotlin traces build the same two trees from tree-sitter syntax instead of SSA (ADR 0007). Keys are expressions, locals and parameters by name (one flat scope per function, as the extractor has), function results and fields, keyed `pkg.Type.field` as in Go. Calls follow what the JVM extractor resolved; an unresolved call derives its result from its receiver or its one data argument. Without types, values that only describe the datum are dropped by shape: comparisons and predicate calls (`isBlank`, `equals`).
