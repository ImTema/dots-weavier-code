# Interface hops link every implementation, tagged sure or guess

At an interface hop, gofacon links every implementation in the project (class-hierarchy analysis, as gortex and grpctrace do), so no real path is silently missed. A second pass (variable type analysis, VTA) checks which concrete values actually reach the call. Implementations it confirms are tagged **sure**; the rest are tagged **guess**. None of the reference tools works this way:
- codebase-memory-mcp stops at interfaces that have several implementations, which breaks traces.
- gortex and graphify link everything and attach a confidence value, with no flow check.

On `scoring`'s own code the two analyses give 19,841 vs 8,578 edges and take about 0.5s together, so the extra pass is cheap.
