# 3D galaxy rendering for `dotweavier ui`

Research date: 2026-11-19. Question: how should the Overview tab of `dotweavier ui` draw a 3D "galaxy" of up to ~20 000 function nodes, clustered and colored by project, with cross-project (gRPC) edges highlighted? Constraints: Go stdlib `net/http`, offline, every JS asset vendored through `go:embed`, no CDN, no JS build step.

Sections 1 to 4 hold facts read from primary sources, each with its link. Section 5 holds the recommendation. Statements marked *(inference)* are my reasoning, not something a source says.

Snapshots read:

- `DeusData/codebase-memory-mcp` at [`1111d9a`](https://github.com/DeusData/codebase-memory-mcp/tree/1111d9a9e1380a0429ef2250f63223706af6289a) (2026-11-19)
- `zzet/gortex` at [`5e79a35`](https://github.com/zzet/gortex/tree/5e79a3583bd3caffe6a673d2349ee4652d20ec64) and its UI repo `gortexhq/web` at [`9b0b48a`](https://github.com/gortexhq/web/tree/9b0b48a5ee8d7a3499265f0fae4f28fd26d5114f)
- npm packages as published on 2026-11-19: three 0.187.0, 3d-force-graph 1.80.2, three-forcegraph 1.43.6, force-graph 1.52.0, d3-force-3d 3.0.6, ngraph.forcelayout 3.3.1, sigma 4.0.6, graphology 0.26.0, @cosmos.gl/graph 3.6.0

All file links below point to the default branch. Line numbers match the snapshots above.

---

## 1. How codebase-memory-mcp renders its galaxy

The UI is **CBM Lens** (`graph-ui/`), a React 19 + Vite app. The [README](https://github.com/DeusData/codebase-memory-mcp/blob/main/README.md) calls it an "optional visual companion" in open beta, with a 3D "Galaxy" view, and says it "builds on CodeAtlasWeb" ([graph-ui/README.md](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/README.md)). Several galaxy files say in their headers that they were adopted from the project's earlier `graph-ui/src/components/*` on branch `feat/atlas-r1`, with the logic left unchanged (for example the header of [NodeCloud.tsx](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/NodeCloud.tsx)).

### Library

- It uses **three.js 0.183.2** through **@react-three/fiber 9.5**, **@react-three/drei** and **@react-three/postprocessing** ([graph-ui/package.json](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/package.json)). It does not use a graph library such as 3d-force-graph.
- The scene sits in an R3F `<Canvas>` with OrbitControls (from `three-stdlib`) and an `EffectComposer` + `Bloom` pass ([GraphScene.tsx](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/GraphScene.tsx), imports at lines 122-125, Bloom at line 1198).

### Rendering primitives

- **Nodes** ([NodeCloud.tsx](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/NodeCloud.tsx)): the default draws one `THREE.InstancedMesh` of spheres. Sphere tessellation drops as the node count rises: 32x24 segments up to 8 000 nodes, 16x12 up to 25 000, then 10x7. Above `POINT_MODE_THRESHOLD = 75000` it switches to `THREE.Points` sprites ("one position per node"). Colors above 1.0 feed the bloom glow.
- **Edges** ([EdgeLines.tsx](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/EdgeLines.tsx)): all edges go into **one `BufferGeometry`** with `position`, `color` and a custom `edgeFlow` attribute (lines 274-287). The direction pulse is a shader uniform, so "only a time uniform changes per frame". Edge opacity scales with edge count. The header notes a `targetNodes` prop that served "edges between two galaxies" in the original.

### Layout: precomputed on the server, in C

- [layout-source.ts](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/layout-source.ts) (header comment, in German) says that `/api/layout` is the only route that returns a finished 3D layout. The C server ([layout3d.c](https://github.com/DeusData/codebase-memory-mcp/blob/main/src/ui/layout3d.c)) computes coordinates, sizes and colors, not the browser, so there is "no force simulation" in the client. The JSON per node carries `x,y,z,size,color` plus metadata.
- Algorithm (header of [layout3d.c](https://github.com/DeusData/codebase-memory-mcp/blob/main/src/ui/layout3d.c)), "structured first, then refined":
  1. **Clustering.** The cluster key is the **first 3 directory components of the file path** (lines 720-733). Each cluster is placed on a ring: `angle = fnv1a(key)` and radius 500-750 from the hash (lines 735-737). Each node gets ±20 of deterministic jitter seeded from the qualified name.
  2. **z = call depth** × 50 (`Z_DEPTH_SPACING`, `compute_call_depth`).
  3. **Refinement.** A "gentle" ForceAtlas2-style pass with **Barnes-Hut octree** repulsion (θ = 1.2) and anchor springs (`LOCAL_ANCHOR_K 0.25`) that keep nodes near their seed. It runs 40 iterations, or 20 above 100k nodes and 10 above 500k (lines 325-336, 361-371).
  - Color is a "stellar" palette by **degree**, from red dwarf to blue giant (`stellar_color`, lines 113-133). Size is a base per label plus a degree boost (lines 760-763).
- **Large graphs.** The default request budget is `DEFAULT_MAX_NODES 5000`, with a hard maximum of 10 000 000 that the `CBM_UI_MAX_RENDER_NODES` environment variable can lower (lines 31-32, 183-212). The client asks for `LAYOUT_NODE_BUDGET = 5000` ([galaxy-model.ts](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/galaxy-model.ts) line 41). The node set is the first `max_nodes` results of a store search (`cbm_layout_compute`, lines 504-547). Edges with an endpoint outside the window are dropped.
- **Scoped/neighbourhood views** (expanding a symbol's callers or callees) are laid out **in the browser, in a Web Worker** ([organic-layout.worker.ts](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/organic-layout.worker.ts), [use-organic-layout.ts](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/use-organic-layout.ts), [organic-clusters.ts](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/organic-clusters.ts)). Those clusters are by shared source directory. Already visible positions stay fixed when a new layer arrives. Scoped queries are paged at 5 000 rows ([graph-scope.ts](https://github.com/DeusData/codebase-memory-mcp/blob/main/graph-ui/src/galaxy/graph-scope.ts) line 27).

### Shipping

The built `graph-ui/dist` is compiled into the C binary as a generated asset table ([embedded_assets.h](https://github.com/DeusData/codebase-memory-mcp/blob/main/src/ui/embedded_assets.h); `embed` and `cbm-with-ui` targets in [Makefile.cbm](https://github.com/DeusData/codebase-memory-mcp/blob/main/Makefile.cbm) lines 1442-1460, script `scripts/embed-frontend.sh`). That route **requires Node.js and a Vite build**, which dotweavier rules out.

### gortex, for comparison

- gortex's web UI is a separate Next.js app, [`gortexhq/web`](https://github.com/gortexhq/web) ([docs/server.md](https://github.com/zzet/gortex/blob/main/docs/server.md) "Web UI"). The docs list "Sigma.js 2D + five react-three-fiber 3D modes", but the current [package.json](https://github.com/gortexhq/web/blob/main/package.json) has no `sigma`. It has `three` 0.184, `@react-three/fiber`, `graphology` and `graphology-layout-forceatlas2`.
- Its 3D views cap aggressively:
  - [Galaxies.tsx](https://github.com/gortexhq/web/blob/main/src/components/graph/views/Galaxies.tsx): `MAX_PER_REPO = 80` nodes, `MAX_EDGES = 500`, top nodes by degree per repo
  - [Constellation.tsx](https://github.com/gortexhq/web/blob/main/src/components/graph/views/Constellation.tsx): `MAX_NODES = 1600`, `MAX_EDGES = 3000`, with `forceatlas2.assign` run synchronously on the main thread and Barnes-Hut above 400 nodes
  - City uses `InstancedMesh`
- gortex therefore never renders anything near 20 000 nodes.
- License: gortex's own source-available license, "Based on PolyForm Small Business 1.0.0" ([LICENSE.md](https://github.com/gortexhq/web/blob/main/LICENSE.md)). The main repo is the same kind ([gortex LICENSE.md](https://github.com/zzet/gortex/blob/main/LICENSE.md)). It is not a source to copy code from.

---

## 2. Candidate libraries: performance at ~20 000 nodes

### three.js (raw)

- [`InstancedMesh`](https://github.com/mrdoob/three.js/blob/dev/src/objects/InstancedMesh.js): "Use this class if you have to render a large number of objects with the same geometry and material(s) but with different world transformations ... will help you to reduce the number of draw calls". It also supports per-instance color.
- [`LineSegments`](https://github.com/mrdoob/three.js/blob/dev/src/objects/LineSegments.js): "A series of lines drawn between pairs of vertices". All edges fit in one geometry and one draw call, which is what CBM's `EdgeLines` does. [`Points`](https://threejs.org/docs/#api/en/objects/Points) is the cheaper fallback for nodes, and CBM uses it above 75k.
- three.js has no layout engine. Positions must come from somewhere else.
- Evidence at scale: CBM Lens tunes its instanced spheres explicitly for 8k / 25k / 75k nodes (see section 1). That is a first-party signal that one InstancedMesh plus one LineSegments is viable well past 20k.

### 3d-force-graph / three-forcegraph (vasturiano)

- Renders with three.js. Layout uses [d3-force-3d](https://github.com/vasturiano/d3-force-3d) (default) or [ngraph.forcelayout](https://github.com/anvaka/ngraph.forcelayout) ([README](https://github.com/vasturiano/3d-force-graph#readme), `forceEngine`).
- **No instancing.** In [three-forcegraph/src/forcegraph-kapsule.js](https://github.com/vasturiano/three-forcegraph/blob/master/src/forcegraph-kapsule.js), each node is its own `new three.Mesh()` (line 691) with shared sphere geometries and materials per size and color (lines 707-730). Each link is its own `new three.Line(lineGeometry)` (lines 811-816). The published minified bundle has no `InstancedMesh`.
- The maintainer explains why in [issue #439](https://github.com/vasturiano/3d-force-graph/issues/439): "links are rendered as Line objects ... I don't think there is an equivalent `InstancedLine`". As a workaround he suggests using `InstancedMesh` yourself in `nodeThreeObject`, but "I haven't actually tried".
- **Layout runs on the main thread**, ticking per rendered frame. `warmupTicks`, `cooldownTicks` (default ∞) and `cooldownTime` (default 15 000 ms) are the only controls ([README](https://github.com/vasturiano/3d-force-graph#readme), "Force engine configuration"). A Web Worker is an open feature request ([issue #353](https://github.com/vasturiano/3d-force-graph/issues/353)). A commenter there notes that the blocker is the library's use of DOM elements. The reporter saw the page block for seconds at "4K nodes and 3K links".
- Precomputed positions are possible: d3-force-3d honors fixed `fx/fy/fz` on nodes (used in the kapsule, lines 120, 1082-1091), and `cooldownTicks(0)` stops the simulation.
- The largest official example is ["Larger graph (~4k elements)"](https://vasturiano.github.io/3d-force-graph/example/large-graph/) (README line 24).
- The README advises turning off `enablePointerInteraction` "for maximum gain in your graph performance".
- *(Inference)* At 20k nodes and a similar or larger number of edges, this means tens of thousands of scene objects and draw calls per frame, before any layout cost.

### force-graph (vasturiano, 2D)

The same API family, rendered on a 2D HTML canvas ([repo](https://github.com/vasturiano/force-graph)). It is not 3D. It is listed only as the lighter fallback if 3D is dropped.

### Notable alternatives

- **sigma.js + graphology** (2D, WebGL). The [README](https://github.com/jacomyal/sigma.js) says it targets "graphs of thousands of nodes and edges using WebGL" and gives no benchmark. Layout comes from [graphology-layout-forceatlas2](https://graphology.github.io/standard-library/layout-forceatlas2.html), which "comes with a utility" to run in a **web worker**, and offers Barnes-Hut repulsion in "O(n*log(n)) ... rather than default O(n^2)". It is 2D only.
- **cosmos.gl** (`@cosmos.gl/graph`, 2D). [README](https://github.com/cosmosgl/graph): "All the computations and drawing occur on the GPU in fragment and vertex shaders", for "hundreds of thousands of points and links on modern hardware". Points are x,y pairs, so it is 2D.
- **d3-force-3d / ngraph.forcelayout** used standalone. Either can run in a plain Web Worker with no DOM, since both are pure JS. This would be the "browser layout" option for a hand-written three.js scene.

### Where layout should run

- Both large-graph systems surveyed avoid a live, main-thread simulation for the overview:
  - CBM precomputes in its native server (Barnes-Hut octree, anchored, 40 iterations)
  - CBM uses a Web Worker only for small scoped subgraphs
  - gortex caps at 1 600 nodes when it lays out on the main thread
- *(Inference)* For dotweavier, Go is the natural place. The cluster key ("project") is already known, and seeding a ring or sphere per project plus a short Barnes-Hut refinement is plain numeric Go code. The result can be cached in `~/.dotweavier` next to the index and served as one JSON or binary blob.

---

## 3. Vendoring for `go:embed` (no CDN, no build step)

Sizes measured on 2026-11-19 from the npm tarballs via jsDelivr. Gzip is `gzip -9`.

| Library | File(s) to vendor | Raw | Gzip | Self-contained? |
|---|---|---|---|---|
| three 0.187.0 | `build/three.module.js` + `build/three.core.js` (unminified, shipped in package) | 659 KB + 1 475 KB | – | ES modules; `three.module.js` imports `./three.core.js` (relative) |
| three 0.187.0, minified | `three.module.min.js` + `three.core.min.js` (generated by jsDelivr's Terser; **no `.min.js` is in the npm package any more**) | 392 KB + 422 KB = 814 KB | 193 KB | same |
| three OrbitControls | `examples/jsm/controls/OrbitControls.js` | 41 KB (21 KB min) | 5 KB | imports bare `"three"`, so it needs a native `<script type="importmap">` (no build step) |
| 3d-force-graph 1.80.2 | `dist/3d-force-graph.min.js` (UMD, global `ForceGraph3D`) | 1 390 KB | 364 KB | yes, bundles three.js and all deps |
| three-forcegraph 1.43.6 | `dist/three-forcegraph.min.js` | 104 KB | 33 KB | needs three separately |
| force-graph 1.52.0 (2D) | `dist/force-graph.min.js` (UMD) | 173 KB | 56 KB | yes |
| d3-force-3d 3.0.6 | `dist/d3-force-3d.min.js` | 11 KB | 4 KB | yes (UMD), worker-friendly |
| ngraph.forcelayout 3.3.1 | `dist/ngraph.forcelayout.min.js` | 37 KB | 10 KB | yes |
| graphology 0.26.0 | `dist/graphology.umd.min.js` | 74 KB | 14 KB | yes |
| sigma 4.0.6 | none: `dist/` has only ESM/CJS chunks with bare imports (`events`, `graphology-utils`) | – | – | **no**, needs a bundler (or a jsDelivr `+esm` rewrite) |
| @cosmos.gl/graph 3.6.0 | `dist/index.min.js` (UMD, global `Cosmos`) | 817 KB | 215 KB | yes |

Package metadata is from the npm registry (`registry.npmjs.org/<pkg>/latest`). File listings are from `data.jsdelivr.com/v1/packages/npm/<pkg>@<ver>`.

Notes:

- `go:embed` serves either form as-is. Go's `net/http` does not gzip automatically, so the raw size is what goes over loopback unless a gzip wrapper is added. Over loopback this hardly matters. The figure that matters is binary size: about +0.8 MB for minified three.js versus about +1.4 MB for 3d-force-graph.
- The existing self-contained pages (`cmd/dotweavier/trace-path.html`, `trace-field.html`) are not on this branch, so I could not check how they inline assets.

---

## 4. Licenses

| Library | License | Source |
|---|---|---|
| three.js | MIT | [LICENSE](https://github.com/mrdoob/three.js/blob/dev/LICENSE); npm `license: MIT` |
| 3d-force-graph, three-forcegraph, force-graph, d3-force-3d | MIT (© Vasco Asturiano) | [3d-force-graph LICENSE](https://github.com/vasturiano/3d-force-graph/blob/master/LICENSE); npm metadata |
| ngraph.forcelayout | BSD-3-Clause | npm metadata, [repo](https://github.com/anvaka/ngraph.forcelayout) |
| sigma.js, graphology, graphology-layout-forceatlas2 | MIT | [sigma README](https://github.com/jacomyal/sigma.js); npm metadata |
| @cosmos.gl/graph 3.x | MIT | [README](https://github.com/cosmosgl/graph); npm metadata. **The predecessor `@cosmograph/cosmos` 2.x is CC-BY-NC-4.0** (npm metadata), so pin to `@cosmos.gl/graph`. |
| codebase-memory-mcp | MIT, © 2025 DeusData | [LICENSE](https://github.com/DeusData/codebase-memory-mcp/blob/main/LICENSE). Its layout ideas (and code, with attribution) may be reused. |
| gortex / gortexhq/web | Source-available, PolyForm Small Business-based | [LICENSE.md](https://github.com/gortexhq/web/blob/main/LICENSE.md). Do not copy. |

All the MIT and BSD candidates require keeping the copyright notice. A `THIRD_PARTY` notice next to the embedded files is enough.

---

## 5. Recommendation for dotweavier

**Vendor plain three.js and write a small galaxy page. Compute the layout in Go and serve it precomputed.**

- **Assets.** `three.module.min.js` + `three.core.min.js` + `OrbitControls.js`, about 835 KB raw, loaded through a native `<script type="importmap">`. No build step and no CDN.
- **Nodes.** One `InstancedMesh` of low-poly spheres with per-instance color by project. Fall back to `THREE.Points` if needed.
- **Edges.** One `LineSegments` with a vertex-color buffer. Draw cross-project gRPC edges in a second, brighter `LineSegments` so they can be toggled and highlighted independently. That is two or three draw calls for the whole galaxy.
- **Layout in Go.** This is the CBM pattern, adapted:
  - seed each project's cluster at a point on a sphere or ring (hash of project id)
  - jitter nodes around it deterministically
  - run a few dozen Barnes-Hut refinement iterations with anchor springs
  - cache the result in `~/.dotweavier` keyed by the index version
  - optional, borrowed from CBM: use call depth for the z axis
- **Picking.** `Raycaster` against `InstancedMesh` returns `instanceId`, which is enough for hover and click into the existing trace views.

**Main trade-off.** dotweavier gives up the ready-made API of 3d-force-graph (drag, labels, auto-coloring, built-in hover in about 20 lines) for a few hundred lines of its own JS and Go layout code. In return:

- rendering stays at a handful of draw calls instead of roughly one object per node and per link
- the main thread does no physics
- the layout is deterministic, cacheable and testable in Go
- the binary grows by about 0.8 MB instead of 1.4 MB

**Fallback if time matters more than scale.** Use 3d-force-graph with Go-precomputed `fx/fy/fz` and `cooldownTicks(0)`. That removes the layout cost but keeps the per-object rendering the maintainer describes in issue #439. *(Inference)* Expect it to struggle well before 20 000 nodes plus edges; its own largest demo is about 4k elements.

**Not recommended for this tab:**

- sigma.js: 2D and no single-file bundle
- cosmos.gl: 2D, though GPU layout is attractive if a 2D overview is ever acceptable
- gortex's approach: caps of 80 to 1 600 nodes, and a non-OSS license
