# Nodes are identified by language-qualified symbol name, not file path

A node's identity is its language-qualified name (Go: `importpath.(*Type).Method`). Project, relative path, absolute path and line are attributes. Projects are indexed separately and composed later. A call from project B into project C can only be recorded by name while C is not loaded, so linking needs name-based identity. Absolute paths would also break when a checkout moves.
