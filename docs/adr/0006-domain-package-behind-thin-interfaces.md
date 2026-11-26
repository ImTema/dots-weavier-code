# All logic lives in internal/domain; the CLI and future UI are thin interfaces over it

dotweavier will have more than one interface: the CLI now, a UI later. Both must retrieve the same information, so indexing, storage and both traces live in `internal/domain`, and each interface lives under `cmd/` and only parses input and renders output.

- `domain` exports a small API: `Index`, `TracePath` and `TraceField` (with the field trace runs it keeps, ADR 0012). They take a context where they run long, a query struct of addresses and options, and return plain result types (`PathTrace`, `Path`, `Node`, `FieldTrace`, `FieldStep`) that carry no analysis internals such as SSA values or graph node numbers.
- Results are presentation-neutral. Text trees, HTML pages and their templates belong to the interface that renders them. `cmd/dotweavier` renders text and HTML; a UI renders its own.
- Interfaces never reach into domain internals. If an interface needs something new, the domain API grows to provide it.

## Consequences

- The UI will reuse `domain` as-is, with no CLI code in between.
- Storage (`store` code, ADR 0004) lives inside `domain` for now. Split it into its own package when a second store or a store-only consumer appears.
- `domain` never writes to stdout or stderr. Progress and skipped projects come back as warnings in each result (`FieldTrace.Warnings`, `PathTrace.Warnings`, `IndexResult.Warnings`), and each interface decides how to show them. Long-running calls (`Index`, `TraceField`, `FollowFieldRun`) take a `context.Context` and stop when it is cancelled.
