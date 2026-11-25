# Ill-typed packages of a target are kept and stubbed, not dropped

Target projects are often messy: a broken import or two is common. `ssautil.Packages` drops ill-typed packages, and dropping a package loses every path through it. dotweavier keeps them instead. Before building SSA it empties, in memory only, the bodies and package-level initializers that use an invalid type, because the SSA builder panics on them.

The cost: a stubbed function stays as a node but loses its outgoing calls.
