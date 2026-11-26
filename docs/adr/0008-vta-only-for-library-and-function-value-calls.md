# Library calls and function-value calls take VTA targets only

ADR 0005 links every class-hierarchy (CHA) target of an interface call and tags the unconfirmed ones guess. Two kinds of call skip CHA and keep only the targets variable type analysis (VTA) confirms:

- **Library calls**: an interface call that VTA resolves to a dependency's type (an engine built by another project) is a library call. CHA-only targets would make it noise, because small interfaces like `io.Writer` match half of every dependency. A dependency's functions come from export data and count as synthetic, so library targets are collected before the synthetic nodes are deleted.
- **Function-value calls**: CHA links every function with the same signature, which is pure noise.

The cost: a library or function-value target that VTA cannot see is missed rather than guessed.
