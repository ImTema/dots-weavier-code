## Project context

- This repo builds **dotweavier**, a CLI that indexes Go projects and traces call paths between symbols. See `GLOSSARY.md`, `docs/adr/` and `.scratch/dotweavier-mvp/map.md`.
- "DDD" describes dotweavier's own codebase only. Target projects that dotweavier analyzes have no assumed architecture and may be messy. Justify analysis decisions with measured facts about the targets, never with an assumed architecture.
- dotweavier never writes inside a target project. All state lives in `~/.dotweavier`.

## Agent skills

### Issue tracker

Issues live as local markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default canonical labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

### Sample projects

Adding a sample service under `samples/` in any language: follow `docs/agents/scaffold-sample-project.md`.
