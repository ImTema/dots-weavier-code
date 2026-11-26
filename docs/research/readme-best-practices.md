# README best practices for dotweavier

Research date: 2026-11-27. Question: what do primary sources say a project README should contain and how it should relate to the rest of a project's docs, and how should dotweavier's current `README.md` change to match?

Sections 1 to 7 hold facts read from primary sources, each with its link. Section 8 audits the current README against them. Section 9 holds the recommendation. Statements marked *(inference)* are my reasoning, not something a source says.

Snapshots read:

- dotweavier at `8c4b5a4` (`README.md` 88 lines, `docs/cli.md` 96 lines, `go.mod`, `cmd/dotweavier/main.go`, `samples-monorepo/README.md`, `.scratch/dotweavier-ui/map.md`)
- READMEs of `junegunn/fzf`, `cli/cli`, `ondrajz/go-callvis`, `golangci/golangci-lint`, `sourcegraph/scip-go` and `charmbracelet/vhs`, default branches as of 2026-11-27

---

## 1. What GitHub does with a README

- A README "typically" tells readers what the project does, why it is useful, how to get started, where to get help, and who maintains it ([GitHub Docs, About READMEs](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-readmes)).
- GitHub picks the README from `.github/`, then the repository root, then `docs/`, in that order. Content beyond 500 KiB is truncated (same page).
- GitHub generates a table of contents from the headings, reachable from the Outline menu at the top of the rendered file (same page). Every heading gets an anchor: lowercased, spaces become hyphens, other punctuation is dropped, duplicates get `-1`, `-2` ([Basic writing and formatting syntax](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax)).
- GitHub recommends relative links for other files in the repository, because "absolute links may not work in clones of your repository". Images in the repository should also use relative paths (same two pages).
- Topics exist "to help other people find and contribute to your project" ([Classifying your repository with topics](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/classifying-your-repository-with-topics)).

## 2. What sections a README has

### Standard Readme spec

The [Standard Readme spec](https://github.com/RichardLitt/standard-readme/blob/main/spec.md) fixes an order: Title, Banner, Badges, Short Description, Long Description, Table of Contents, Security, Background, Install, Usage, Extra Sections, API, Maintainers, Thanks, Contributing, License. Its rules include:

- The short description must be under 120 characters and should match the GitHub repository description.
- A table of contents is required, except for READMEs under 100 lines.
- Install and Usage each need a code block. Usage gets a CLI subsection if the project has a CLI.
- Contributing and License are required. License must be the last section and must give the SPDX identifier and the owner.

### Make a README

[Make a README](https://www.makeareadme.com/) lists Name, Description, Badges, Visuals, Installation, Usage, Support, Roadmap, Contributing, Authors, License and Project status. Points that matter here:

- Description: if alternatives exist, say what sets the project apart.
- Visuals: screenshots or a video, "you'll frequently see GIFs rather than actual videos". For terminal recordings it names Asciinema as a more polished option.
- Installation: assume a novice reader. Add a Requirements subsection for specific runtime or dependency needs.
- Usage: "Use examples liberally, and show the expected output if you can." Keep the smallest example inline and link to longer ones.
- Project status: if development has slowed, say so at the top.
- Length: "too long is better than too short", but when it grows, move detail to other docs instead of deleting it.

### Readme Driven Development

Tom Preston-Werner's [Readme Driven Development](https://tom.preston-werner.com/2010/08/23/readme-driven-development.html) argues "Write your Readme first", because "a perfect implementation of the wrong specification is worthless", and calls the README "the single most important document in your codebase". He separates it from full documentation-driven development: the README is a limited subset, not the whole manual.

## 3. How the README relates to other docs

[Diátaxis](https://diataxis.fr/start-here/) sorts docs into four kinds: tutorials ("a lesson, that takes a student by the hand"), how-to guides ("a real-world goal or problem"), reference ("the technical description") and explanation ("context and background"). The site says nothing about READMEs specifically.

- Reference "should be austere. One hardly reads reference material; one consults it", should be consistent, and its structure "should mirror the structure of the product". Instruction and explanation belong in other documents that the reference links to ([Diátaxis, Reference](https://diataxis.fr/reference/)).
- A tutorial "ought to be so well constructed that things can't go wrong", every step should "produce a comprehensible result, however small", and "a tutorial is not the place for explanation" ([Diátaxis, Tutorials](https://diataxis.fr/tutorials/)).

*(inference)* Mapped onto dotweavier: the README's Quick start and Demo are a small tutorial; `docs/cli.md` is reference and mirrors the command tree; `docs/adr/` is explanation; `docs/GLOSSARY.md` is reference for terms. Each fact should live in one of them, and the others should link to it.

## 4. Formatting features GitHub renders

- **Alerts.** `> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`. GitHub says to use them "only when they are crucial for user success", to limit them to one or two per article, and not to place them back to back ([Basic writing and formatting syntax](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax)).
- **Collapsed sections.** `<details><summary>…</summary>…</details>` lets the reader expand content on demand; `open` shows it expanded by default ([Organizing information with collapsed sections](https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/organizing-information-with-collapsed-sections)).
- The `<picture>` element is supported (formatting syntax page above).

## 5. Go-specific facts

- **`go install` with a version.** "If the arguments have version suffixes (like `@latest` or `@v1.0.0`), `go install` builds packages in module-aware mode, ignoring the `go.mod` file in the current directory." Arguments "must not be … relative or absolute file paths", and the target module's `go.mod` must not contain `replace` or `exclude` directives that would change its meaning as a main module ([Go Modules Reference, go install](https://go.dev/ref/mod#go-install)).
- **Downloadable module paths.** For a module the `go` command downloads, the first path element "must contain at least one dot". A module that is never fetched "may use any valid package path" ([Go Modules Reference, module paths](https://go.dev/ref/mod#module-path)). The `go` command locates source "using a URL derived from the module path" ([Managing module source](https://go.dev/doc/modules/managing-source)).
- **Command package comments.** A command's package comment "describes the behavior of the program rather than the Go symbols", and its first sentence "conventionally begins with the name of the program itself", for example "Gofmt formats Go programs." ([Go Doc Comments, Commands](https://go.dev/doc/comment)).
- **pkg.go.dev.** It offers a badge tool that produces Markdown for a README, and adds modules on request, through proxy.golang.org, or after `go get example.com/my/module@v1.0.0` ([pkg.go.dev/about](https://pkg.go.dev/about)). Without a detected license, "only limited package and module information will be made available". It looks for files such as `LICENSE`, `LICENSE.md`, `COPYING` ([License policy](https://pkg.go.dev/license-policy)).
- **License.** Without one, "the default copyright laws apply, meaning that you retain all rights to your source code". GitHub recommends a license file at the repository root ([Licensing a repository](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/licensing-a-repository)). Go's own initial-commit table lists `LICENSE` as "The module's license" ([Managing module source](https://go.dev/doc/modules/managing-source)).
- **Community health files.** GitHub recognises `CONTRIBUTING.md`, `SECURITY.md`, `SUPPORT.md`, `CODE_OF_CONDUCT.md` and others in the root, `.github/` or `docs/`, and issue templates in `.github/ISSUE_TEMPLATE` ([Creating a default community health file](https://docs.github.com/en/communities/setting-up-your-project-for-healthy-contributions/creating-a-default-community-health-file)).

## 6. Badges and demos

- Shields.io is "a service for concise, consistent, and legible badges" covering CI services, registries and code analysis tools. It gives no advice on which badges to use ([shields.io docs](https://shields.io/docs)).
- A GitHub Actions badge lives at `https://github.com/OWNER/REPOSITORY/actions/workflows/WORKFLOW-FILE/badge.svg` and "shows whether a workflow is currently failing or passing" ([Adding a workflow status badge](https://docs.github.com/en/actions/how-tos/monitor-workflows/add-a-status-badge)).
- VHS lets you "write terminal GIFs as code". A `.tape` file is a list of commands (`Output demo.gif`, `Type "…"`, `Enter`, `Sleep 2s`), rendered with `vhs demo.tape` to GIF, MP4, WebM or PNG frames. The official `vhs-action` keeps GIFs up to date in CI, and `.txt`/`.ascii` output can serve as golden files ([charmbracelet/vhs README](https://github.com/charmbracelet/vhs/blob/main/README.md)).

## 7. How well-regarded CLIs structure their READMEs

| Project | Opening | Install | Reference lives in | Length |
|---|---|---|---|---|
| [fzf](https://github.com/junegunn/fzf/blob/master/README.md) | logo, badges, one-sentence tagline, screenshot | long, per package manager and OS | man page, `ADVANCED.md`, `BUILD.md`, wiki | about 600 lines, with ToC |
| [cli/cli](https://github.com/cli/cli/blob/trunk/README.md) | one sentence ("GitHub CLI, or the gh CLI, is GitHub on the command line."), screenshot | per platform, each in its own `docs/install_*.md` | manual at cli.github.com | about 1 100 words |
| [go-callvis](https://github.com/ondrajz/go-callvis/blob/master/README.md) | logo, 4 badges, tagline, "Output preview" image | `go install github.com/ofabry/go-callvis@latest`, requirements first | full flag list inline | medium |
| [golangci-lint](https://github.com/golangci/golangci-lint/blob/main/README.md) | logo, tagline, 11 badges | links only | docs site golangci-lint.run | about 100 lines |
| [scip-go](https://github.com/sourcegraph/scip-go/blob/main/README.md) | "SCIP indexer for Golang." | `go install github.com/scip-code/scip-go/cmd/scip-go@latest` | `--help` | about 550 words, Quick Start, Common Problems, Development, Contributing |

Common to all five: a one-line statement of what the tool is, then something to look at or run within the first screen. Reference material is either short enough to inline (go-callvis) or moved out (fzf, gh, golangci-lint). The two Go analysis tools (go-callvis, scip-go) install with one `go install …@latest` line.

---

## 8. Audit of the current README

The current file follows the model of section 7 well: one-paragraph description, quick start, real output, a demo against a known target, links to deeper docs. It stays under 100 lines, so Standard Readme does not require a ToC. Findings, top to bottom:

**Title and description.**
- The first paragraph says what dotweavier does and what sets it apart (cross-project, gRPC joining, sure/guess). That matches Make a README's "what sets it apart" advice.
- It is 300+ characters. There is no sentence under 120 characters that could also be the GitHub repository description, and the GitHub description is currently empty with no topics (`gh repo view`). *(inference)* A one-line tagline first, then the current paragraph, would serve both.
- "**sure**" and "**guess**" are glossary terms. *(inference)* Link them to `docs/GLOSSARY.md` or ADR 0005 rather than explain inline.
- Gap: no project status. *(inference)* The map and issues show an MVP in progress; one line ("early, interfaces change") follows Make a README's status advice.

**Quick start.**
- "Requires Go and the Go toolchain on your `PATH`" says the same thing twice. It also misses a real requirement: `github.com/smacker/go-tree-sitter` uses cgo (`import "C"` in its `bindings.go`), so a build needs a C compiler and `CGO_ENABLED=1`. Target Go projects also need their modules downloadable (`docs/cli.md`: "fails with `no packages`").
- `go install ./cmd/dotweavier` only works from a clone. `go install …@latest` cannot work today because the module path is `dotweavier`, which has no dot in its first element and does not match `github.com/ImTema/dots-weavier-code` (section 5). The README does not say to clone first.
- The quick start indexes only `orders` and then traces into `pricing::Server.Quote`. *(inference)* A cross-project path needs `pricing` indexed too; as written the step may produce no path, which breaks Diátaxis's "things can't go wrong" rule. Using `init … --index` (already supported) removes the per-project loop.
- The quick start uses a hypothetical `~/code`, so a reader cannot check the output. *(inference)* The samples are a better first run because the expected hops are listed in `samples-monorepo/golang/README.md`.

**Usage.**
- It repeats the quick-start `trace path` command verbatim and the `--workspace` example from `docs/cli.md`. That is the duplication Diátaxis warns against for reference material.
- Address formats differ across examples: `orders::…`, `golang/orders:…:81` with `--project samples-monorepo`, and `orders:…:94`. *(inference)* The project name depends on which folder was registered (Glossary, Workspace); the README never says so, so readers will copy a form that fails for their layout. Either use one workspace throughout or state the rule in one sentence and link `docs/cli.md#trace-a-call-path`.
- The sample text output for `trace path` is good; Make a README asks for expected output.
- `dotweavier overview` is mentioned only at the end of Demo. It is a top-level command (`usageText` in `cmd/dotweavier/main.go`). There is no `dotweavier ui` command today: the browser UI is `overview` plus `--format ui`, and the map plans to rename it to `serve` (`.scratch/dotweavier-ui/map.md`). *(inference)* Name it once in the README under a "Browser UI" heading so the later rename touches one place.

**Demo.**
- Strong section: known target, real command, annotated excerpt, a stated known limit. This is the "show expected output" advice done well.
- The for-loop over four project paths can be `dotweavier init samples-monorepo --index`. It also skips `fulfillment` and `payments`, which the samples README lists.
- There is no visual. dotweavier has a browser UI with a 3D overview and trace pages, so a screenshot is the cheapest high-value addition (Make a README, Visuals; every README in section 7 opens with an image).
- "Known limit" is the right place for limits. *(inference)* Keep one known-limits list in one place; the HTTP gap also matters for `trace path`, not only the demo.

**Docs.**
- Good: relative links, as GitHub recommends.
- Missing: a link to `samples-monorepo/README.md`, and to `CONTRIBUTING`/development info (`go test ./...`), which Standard Readme requires and Make a README suggests.

**Missing entirely.**
- License. The repository is public and has no `LICENSE`, so by default nobody may reuse the code (section 5), and pkg.go.dev would show limited information.
- Contributing, even one line ("issues and PRs welcome" or "not accepting PRs yet").
- The `cmd/dotweavier` package comment starts "The dotweavier CLI: …" and lists only four of the five commands (no `overview`). It does not follow the "Dotweavier indexes …" convention (section 5). Not a README issue, but it is what `go doc` and pkg.go.dev show.

---

## 9. Recommendation for dotweavier

**Keep the README short and runnable; move every flag and every address rule to `docs/cli.md`; fix install; add one image, a license and a contributing line.**

### Proposed outline

```
# dotweavier
<one-line tagline, under 120 characters, same text as the GitHub description>
<status line: early development, commands and storage format may change>

<screenshot of the overview or a trace-field page, relative path under docs/images/>

<current first paragraph, with sure/guess linked to the glossary>
<one sentence: never writes inside analyzed projects; state in ~/.dotweavier>

## Install
  Requirements: Go (version from go.mod), a C compiler (cgo, for tree-sitter)
  git clone … && cd … && go install ./cmd/dotweavier
  (later, once the module path is fixed: go install github.com/ImTema/dots-weavier-code/cmd/dotweavier@latest)
  Link: PATH setup and uninstall in docs/cli.md#install

## Try it on the samples
  dotweavier init samples-monorepo --index
  dotweavier trace path -A … -B …        + real output
  dotweavier trace field … --follow 2    + current annotated excerpt
  one sentence on how project names are formed, link to docs/cli.md

## Use it on your code
  dotweavier init ~/code --index
  the three address forms in one line, link to docs/cli.md#trace-a-call-path

## Browser UI
  dotweavier overview, --format ui, --format html (one line each), screenshot if not at top

## Known limits
  Go HTTP clients not linked; Java/Kotlin traced from source within one project (ADR 0007); ...

## Docs
  cli.md, GLOSSARY.md, adr/, samples-monorepo/README.md

## Contributing
  go test ./..., where issues live, whether PRs are accepted

## License
  SPDX id and owner (last section)
```

*(inference)* This stays near 100 lines. If it grows past that, Standard Readme asks for a ToC; GitHub's Outline menu already provides one, so a hand-written ToC is optional.

### Install

- Short term: say "clone, then `go install ./cmd/dotweavier`", list the C compiler requirement, and link `docs/cli.md` for `PATH` and uninstall. Drop the `PATH` line from the README.
- Longer term *(inference)*: change the module path in `go.mod` to `github.com/ImTema/dots-weavier-code` (and the internal imports with it). Then `go install github.com/ImTema/dots-weavier-code/cmd/dotweavier@latest` works without a clone, as for go-callvis and scip-go. Check first that `go.mod` has no `replace` directives (it has none today), since those block `go install …@version`. The binary name comes from the `cmd/dotweavier` directory, so it stays `dotweavier`.

### What goes where

| Content | Home | README does |
|---|---|---|
| What it is, why it differs, status | README | states it |
| First run against samples, with output | README | states it (the one tutorial) |
| Install requirements, one install command | README | states it |
| PATH, uninstall, build | `docs/cli.md` | links |
| Every command, flag, default, address form | `docs/cli.md` | one example per command, links |
| Project/workspace/module naming, sure/guess | `docs/GLOSSARY.md` | one sentence, links |
| Why extractors, why no LSP, why from source | `docs/adr/` | links from Known limits only |
| Expected hops of the samples | `samples-monorepo/README.md` | links |

*(inference)* Rule of thumb: if a fact has a default value or a flag name, it belongs in `docs/cli.md`; the README may show it in an example but should not be its only home.

### Demo

- **Screenshot first.** One PNG of the overview galaxy or a followed trace-field page, committed under `docs/images/` and linked relatively. It is cheap and shows the part of dotweavier the text output cannot.
- **Terminal GIF with VHS, optional.** A `demo.tape` that runs `dotweavier init samples-monorepo --index` and the `trace field … --follow 2` command, rendered to `docs/images/demo.gif`. *(inference)* VHS's `.txt` output can double as a golden file, so the README excerpt and a test would come from the same run; that keeps the excerpt from drifting. Defer it until the output format is stable, or the GIF will go stale.
- **Asciinema** (Make a README's suggestion) needs an external player or host; *(inference)* VHS fits better because the GIF lives in the repository and renders inline on GitHub.
- Keep the annotated text excerpt either way: it is copyable and searchable, an image is not.

### Badges

- *(inference)* Worth having once they are true:
  - a CI status badge, once a test workflow exists (today only `renovate.yml` runs)
  - a license badge, once `LICENSE` exists
  - a Go version badge is redundant with the Requirements line; skip it
- Not worth having now *(inference)*:
  - pkg.go.dev: the module path is not fetchable, and the code is a CLI with `internal/` packages, so there is no public API to browse
  - release/version: there are no tags
  - Go Report Card, stars, downloads, social links: they say nothing about whether dotweavier works for the reader
- Standard Readme puts badges right after the title, one per line.

### Smaller items

- Set the GitHub repository description to the tagline and add topics such as `go`, `call-graph`, `static-analysis`, `grpc`, `cli`.
- Add `LICENSE` at the root (choice of license is the owner's), and a License section as the last README section.
- Use at most one alert, for example `> [!NOTE]` for "never writes inside analyzed projects", if it should stand out. GitHub advises one or two per page.
- Rewrite the `cmd/dotweavier` package comment to start "Dotweavier indexes …" and include `overview`, so `go doc` matches the README.
- When `overview` is renamed to `serve`, the README's Browser UI section and `docs/cli.md` are the only two places to update.
