# Field trace runs are kept by the domain as plain JSON, whole, with every option

A field trace run is traced, followed, kept, listed and read back through one domain module (`TraceField`, `FollowFieldRun`, `LoadFieldRun`, `FieldRuns`). The CLI and the overview server each make one call into it, so a run started from either behaves the same.

- **The run keeps both trees and `Only`.** `--only` is a view: both walks run anyway, so the run stores both trees and `Only`, and each interface hides the tree it names. Trimming the tree before saving made a `--only origin` run unable to follow any hop.
- **Every option the run ran with is kept** (`Dir`, `IncludeTests`, `MaxProjects`, `Only`), so following a kept run continues it the same way. `Follow` is not kept: it is an action, and its grafts are already in the trees.
- **Storage is presentation-neutral JSON**, `<time>_<address>.json` in `~/.dotweavier/trace-field-history`. How many levels a page opens is page state, carried in the page's URL (`open=N`) or inlined in an exported page, never in the run.
- **The field page loads runs from the server** (`GET /trace-field/runs`, `GET /trace-field/run`), not by `<script src>` from the history folder, so it no longer depends on the domain's folder layout. An exported page carries its run inline.
- **A handler already traced in a run is not traced again** by a later follow: a follow takes the handlers already followed from the run's tree.

## Consequences

- Runs saved by earlier versions (`<name>.js`, a `window.TRACE = ...` script) still list and load. A run saved with `--only` before runs kept `Only` lost a tree; it loads with `Only` set from the tree it lacks, and cannot follow hops from the tree it lost.
- The page cannot open a kept run from `file://` any more. Nothing wrote a page that did: `--format ui` serves the page, and `--format html` inlines its run.
- `index.js` and `latest.js` are no longer written; old copies in the history folder are ignored.
