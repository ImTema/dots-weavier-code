# Handoff: overview UI prototype (2026-11-20)

Read this first, then `docs/GLOSSARY.md`. The UI's starting point is `docs/research/3d-galaxy-rendering.md`, and the MVP map is `.scratch/gofacon-mvp/map.md`.

## Where things stand

- Branch: `prototype/overview`, cut from `master` at `c48d5d7`, committed and pushed in one commit. The three "belongs on master" items in the table below are in that same commit; split them out when moving them to `master`.
- The prototype is throwaway. Every new file has `prototype` in its name and a `PROTOTYPE, throwaway` header comment.
- Run it:
  ```
  go run ./cmd/dotweavier overview --workspace ~/GolandProjects/scoring-workspace   # http://127.0.0.1:7357/
  ```
  Flags: `--addr` (default `127.0.0.1:7357`, loopback on purpose) and `--max-nodes` (default 20000, `0` means no cap).
- Building from this machine: the default Go proxy (`athens.tabby.dev`) returns 403 for `modernc.org/libc`. Build with `GOPROXY=https://proxy.golang.org,direct`.

## Working tree

| Path | State | What |
|---|---|---|
| `internal/domain/overview_prototype.go` | new | `OverviewSource`: merged graph, `Build` (layout), `Routes`, `Detect` with `counts` and `parentOf`, `Sync` and `Remove` (cascade over `family`), `selectNodes` (cap), `changedSince` |
| `internal/domain/overview_prototype_test.go` | new | tests for `changedSince` and `selectNodes` ranking |
| `cmd/dotweavier/overview_prototype.go` | new | `overview` subcommand: `net/http` server and endpoints |
| `cmd/dotweavier/overview-prototype.html` | new | the whole page, about 700 lines of HTML and JS, embedded with `go:embed` |
| `cmd/dotweavier/overview_prototype_assets/` | new | three.js 0.187.0, vendored from jsDelivr: `three.module.js`, `three.core.js` (renamed from `*.min.js` to match the module's import), `OrbitControls.js`, `lines/*` (fat lines), `LICENSE-three` |
| `cmd/dotweavier/main.go` | modified | `case "overview"` |
| `internal/domain/graph.go` | modified | **real fix, not prototype**: `stubBroken` drops declarations whose `TypesInfo.Defs` entry is nil. Indexing `scoring-workspace` crashed `ssa.CreatePackage` without it. Belongs on `master`. |
| `docs/GLOSSARY.md` | modified | **Project** locked as "a single folder indexed on its own"; **Workspace** removed and added to _Avoid_. Belongs on `master`. |
| `README.md` | new | short README written on `master` before branching. Belongs on `master`. |
| `gofacon` | untracked, not committed | stale binary from before the rename. Not ours; leave it or ask. |
| `docs/handoffs/2026-11-20-overview-prototype.md` | new | this file |

## Endpoints

- `GET /data.json?show=P…`: layout of the shown projects. Packed parallel arrays (`name`, `id`, `proj`, `file`, `line`, `deg`, `pos`, `edges`, `grpc`, `methods`, `centers`, …), plus `total` and `limit`.
- `GET /route?a=ID&b=ID&k=5&show=P…`: up to k ranked paths, using the existing A* `graph.trace` (fewest guesses, then fewest hops), restricted to the drawn nodes.
- `GET /projects`: projects detected under `--workspace`. Each has `indexed`, `built`, `parent` (the nearest detected project folder containing it, which makes it a module), and `nodes`, `edges`, `grpc` (counts from the merged index, before the node cap).
- `POST /sync?project=P`: syncs P and every module under it. Each one is re-indexed only if a source file is newer than its own index; the graph is re-merged once if anything changed. Returns `results: [{project, upToDate, stats, error}]`. `POST /remove?project=P`: deletes the index files of P and its modules, then re-merges; returns `removed`. Both require the header `X-Dotweavier: 1`, which forces a CORS preflight so other sites can't call them. Both accept only names that `discover` returned.

## Decisions the user made in this session

- **No automatic projects.** The overview starts empty. The panel lists "projects detected", and the user presses **Sync** per project to add it. Added and hidden sets live in `localStorage` (`dw.added`, `dw.hidden`).
- **⋯ menu per added project:** Sync, which does nothing if no `.go`/`go.mod`/`go.sum`/directory mtime is newer than the index, and Remove index, which deletes `~/.dotweavier/proto/<name>.db`. Unsynced detected rows keep a plain Sync button. **Their visual feedback is undecided; ask the user.**
- **Node cap 20 000.** Keep functions with a cross-project edge first, then by degree. Deranked nodes are never sent to the page, so no objects are created for them. The UI shows "shown of total".
- **Hiding or removing a project re-lays-out everything** on the server, not just a client-side filter.
- **Mouse:** left-drag orbits and right-drag always pans. OrbitControls swaps them when ctrl/meta/shift is held, and macOS ctrl-click is a right click, so the page remaps the buttons on a capture-phase `pointerdown`. The wheel zooms to the cursor.
- **Clicking a star opens a context menu** to the left of the cursor: show callers/callees, directions from/to here, fly, search in project, hide project. It does not select directly.
- **Node focus** dims the rest; incoming edges are cyan and outgoing orange. **Clicking an edge** lights its two ends.
- **Directions, maps-style:** the A/B fields are typeahead. Every word must match the name or the project. Up to 5 routes: the chosen one in screen-width fat lines (blue sure, orange guess, magenta gRPC) with a dark casing, the alternatives grey and clickable. Route cards and a step list. Ends are stored as `project:file:name`, because node ids change on every sync.
- **Search** has a project dropdown, and clicking a hit's project label filters by it. The project panel has its own filter, and its all/none apply only to the matches.
- **Glossary:** "project" is the only grouping. The user wants no "workspace" concept. New term **Module**: a project detected inside another project's folder (in `scoring-workspace`, `apid` has 13, all generated SDKs; there are 29 outermost projects and 35 modules). A module has its own index, but it is synced, removed and listed together with its outermost project.
- **Modules in the panel:** they are grouped under their outermost project as "▸ N modules" (open state in `dw.openMods`). The project's Sync, ⋯ Sync and Remove cascade to its modules, and each module is still checked for staleness on its own. `/sync` returns per-project `results`, `/remove` returns `removed`. The project's checkbox hides the whole family; a module's checkbox hides only that module.
- **Panel:** per-project node, edge and gRPC counts; a resize grip at the bottom left (double-click to expand, size in `dw.ppSize`); collapsible sections (`dw.collapsed`).

## Open items

1. **RAM in Safari.** Safari exposes no memory API, so the corner shows `RAM: N/A` with the tooltip "not exposed by this browser". `performance.memory` (the JS heap) is shown only in Chromium. Next to it, **ERAM** (estimated RAM) is what the page allocated itself: geometry and instance buffers in the scene, deduplicated by buffer, plus the layout JSON and the picking arrays. The tooltip breaks it down. It does not include browser overhead. With all 59 indexed projects added (20 000 nodes) it reads about 5.7 MB: 3.6 MB of buffers and 2.1 MB of JSON. Option (b) below was not built.
2. **"workspace" still appears in code:** the `--workspace` flag on `index` and `overview`, `README.md` and the panel header. ADR 0004 talks about nested workspaces. The user hasn't answered whether to rename the code. Leave the ADR as history.
3. **Routes ignore capped-out nodes.** A path through an undrawn function is not found (`shortcut:` comment in `Routes`). The fix would be to return such nodes and their positions with the route.
4. **Alternative routes are near-duplicates.** k-shortest simple paths often differ by one hop. Maps show distinct alternatives.
5. **The layout** is anchored neighbour smoothing, with no Barnes-Hut repulsion (`shortcut:` comment in `layout`).
6. **Search ranking:** `scoring Server.Approve` lists `scoring-SCORE-4474` (a second copy of `scoring` in the workspace) first, because "scoring" is a substring of both.
7. **Finish capturing the prototype** as the prototype skill asks. It is committed to this branch; still to do: write the verdict into a `.scratch/gofacon-mvp/issues/` ticket, and move the three "belongs on master" items to `master`. Ask the user first.

## How to verify without a GPU browser

Safari is the only browser installed. Puppeteer's headless Chrome is cached (it came with mermaid-cli):

```js
const puppeteer = require('/opt/homebrew/lib/node_modules/@mermaid-js/mermaid-cli/node_modules/puppeteer');
const b = await puppeteer.launch({headless: 'shell', args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader']});
```

Example scripts are in `/tmp/dwtest/*.cjs` (they may be gone). They find a star by hovering a grid until `#tip` shows, then click and read the DOM. FPS under swiftshader is meaningless. To syntax-check the page's JS: extract the `<script type="module">` body and run `node --check`.

## Gotchas

- A session hook compresses `Read` output for some files (`/* caveman: body elided */`). Those files on disk are intact. Read exact text with `awk 'NR>=a && NR<=b'` before editing.
- Indexing `scoring-workspace` in full takes about 1m40s and 1.4 GB of memory. 59 projects index. `scoring-libs` has 0 functions because its `go.mod` replaces `../scoring-policy-schemas/gen/go/…/approve`, which is not generated. Five `*/tools` modules report `no packages`.
- `samples/golang` needs `go mod download` with the public proxy before `index` works.

## Session summary: RAM options for Safari

- No page API exists in WebKit for process memory. `performance.measureUserAgentSpecificMemory()` is Chromium-only and needs cross-origin isolation.
- What the prototype can do itself:
  - (a) Count what it allocated: typed arrays, geometry buffers uploaded to the GPU, and the JSON size. That is an estimate of the page's own data, which is the part that scales with nodes.
  - (b) Have the Go server report the RSS of Safari's `com.apple.WebKit.WebContent` processes. The catch is mapping the tab to its process, which isn't reliable with several tabs open.
- Manual and exact: Safari Web Inspector, Timelines, Memory. Activity Monitor also lists the WebContent process by site URL.
