# tomeshift

tomeshift is a Go CLI pipeline that translates DRM-free EPUB files from English to French while preserving EPUB structure and formatting.

The application is built as a **Go CLI project** with:
- CLI entrypoint (`tomeshift`)
- env-backed typed configuration (`github.com/kelseyhightower/envconfig`)
- translation pipeline path for V1 (`TM exact match -> HTTP NMT adapter -> EPUB rebuild`)
- local workflow orchestration via `just`

# General Guidelines

## Answering Behaviour

- Do NOT do anything unless explicitly asked.
- If something is unclear, ask for clarification instead of guessing.
- Keep answers direct and technical.

## Code Changes

- ALWAYS run `gofmt -w -s .` after Go code changes.
- ALWAYS run `golangci-lint run` after code changes.
- ALWAYS prefix local shell commands with `env -u GOROOT` (for example `env -u GOROOT go test ./...`), and NEVER add `env -u GOROOT` inside `Justfile` recipes.
- Keep changes minimal and scoped to the request.
- Avoid unrelated refactors unless explicitly requested.

# Project Structure

```
├── AGENTS.md                         - canonical agent instructions
├── cmd/
│   └── tomeshift/              - CLI entrypoint (`main.go`)
├── internal/                         - implementation packages (config, epub, segment, tm, translate, qa)
├── tests/
│   └── integration/                  - integration tests and fixtures
├── docs/
│   ├── ARCHITECTURE.md               - architecture and constraints (source of truth)
│   ├── DEPENDENCIES.md               - dependency policy
│   ├── understanding-the-project.html     - illustrated architecture & contributor guide
│   ├── usage.html              - illustrated usage & command reference
│   ├── milestones/                   - milestone plans and delivery sequencing
│   └── rfcs/                         - implementation/infrastructure proposals
├── scripts/
│   └── docs_audit.py                 - checks the illustrated docs still cover the CLI surface
├── Justfile                          - local development commands
├── go.mod / go.sum                   - Go module dependencies
├── README.md                         - local dev and env setup
└── Dockerfile / docker-compose.yml   - local container workflow
```

# Toolchain and Secrets

- The toolchain is declared in `mise.toml`. `mise trust && mise install` provides
  Go, Java 21, `just`, `golangci-lint`, `ripgrep`, Python and `uv`. Adding a tool
  a recipe calls means adding it there, not to a prose list.
- `.env.local` holds **non-secret** configuration only.
- API keys live in a 1Password vault. `.env.op` is committed and holds only
  `op://` references; recipes that call a provider are wrapped in
  `op run --env-file=.env.op --`.
- A command that makes no provider call must NOT be wrapped, and must not
  require credentials to load its configuration. `estimate` is the reference
  case: it prices a book with zero API calls, so `translate.RequiresAPIKey` is
  checked where the provider is built, never in `config.Load`.

# Development Commands

## Primary Commands

```bash
# Build all packages
go build ./...

# Run all tests
go test ./...

# Run CLI
just run ./books/source.epub ./runs

# Run tests via task runner
just test

# Run validation checks (fmt + lint + test)
just check
```

If your shell has a broken `GOROOT`, use:

```bash
env -u GOROOT /usr/local/go/bin/go test ./...
```

## Linting and Formatting

```bash
# Format Go code
gofmt -w -s .

# Lint project
golangci-lint run
```

## Task Automation (using just)

```bash
# List available commands
just

# Validate local prerequisites
just preflight

# Run app
just run ./books/source.epub ./runs

# Build Docker image
just docker-build

# Start local container workflow
just up

# Stop local container workflow
just down

# Run local CI-equivalent checks
just ci-local
just check
```

# Git Workflow

## Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):
- `feat: ...`
- `fix: ...`
- `chore: ...`
- `refactor: ...`
- `docs: ...`

## Pre-commit Checklist

Before committing:
1. `go build ./...`
2. `go test ./...`
3. `gofmt -w -s .`
4. `golangci-lint run`

# Go Coding Guidelines

## Imports

- Use Go-standard grouping: stdlib, third-party, internal.
- Let `gofmt` handle ordering.

## Naming

- Package names: lowercase, short, descriptive.
- Exported symbols: `CamelCase`.
- Unexported symbols: `lowerCamelCase`.

## Error Handling

- Handle errors explicitly.
- Wrap with context using `%w`.
- When wrapping an error, use the format: `unable to <context>, err: %w` (for example: `unable to parse cli flags, err: %w`).
- Prefer actionable messages (`unable to ...`).
- For reusable/domain validation errors, define sentinel errors in an `errors.go` file and return/wrap them so upper layers can check with `errors.Is`.

## Logging

- Use structured logging (`zap`) when adding runtime logging.
- Include run context fields (`run_id`, `book_id`, `chapter_id`, `segment_id`) where relevant.
- Avoid logging sensitive payload data or full book content.

## Testing

- Keep unit tests near code in `internal/*`.
- Keep integration tests under `tests/integration`.
- Prefer table-driven tests for multi-scenario logic.
- Cover both happy path and error path.

# Troubleshooting

## Common Issues

- **`GOROOT` misconfiguration**:
  - Symptom: `go: cannot find GOROOT directory ...`
  - Fix: `env -u GOROOT /usr/local/go/bin/go test ./...`

- **Missing env config**:
  - Symptom: config load errors for `TOMESHIFT_*` variables.
  - Fix: copy `.env.example` to `.env.local`, source it, and rerun commands.

- **Local container workflow issues**:
  - Verify Docker/Compose is available.
  - Verify input/output directories are present and mounted as expected.

## Useful References

- Local development setup: `README.md`
- Architecture and constraints: `docs/ARCHITECTURE.md`
- Milestone execution plan: `docs/milestones/M1-core-mvp.md`
- Dependency policy: `docs/DEPENDENCIES.md`

# Living Documentation

Two illustrated HTML documents are the entry point for anyone discovering or
extending this project. They are part of the feature, not a follow-up:

- `docs/understanding-the-project.html` — how the pipeline works and how to extend it
  (concepts, Mermaid diagrams, animated walkthroughs, package map, invariants).
- `docs/usage.html` — how to run it (setup, services, every command and
  flag with its consequence on processing/output/cost, exit codes).

## When you ship something, update them in the same change

| You added / changed | Update in `understanding-the-project.html` | Update in `usage.html` |
|---|---|---|
| A CLI command | §11 package map if a new package appears | §6 command reference, §5 workflow if it belongs in the standard path |
| A flag | only if it changes the pipeline's logic | §6 flag table **and** the `FLAGS` array in the flag explorer (processing / output / cost) |
| An `TOMESHIFT_*` variable | only if it changes routing or cost logic | §8 variables table, and the config generator's `renderCfg()` if it belongs in a recommended setup |
| An NMT or LLM provider | §12 recipe 1 or 2 if the contract changed | §4 services table, §2 config generator, and §3 if it needs a credential |
| A service needing an account or an API key | — | §3 credential setup: the service table, how to obtain the key, the 1Password item |
| A pipeline stage or translation mode | §1 pipeline widget, the relevant flowchart, §12 recipe 3 | §6 `--mode` values |
| A QA check or a metric | §10 QA table (also `docs/ARCHITECTURE.md §10` — it is the contract) | §9 artifacts, if it surfaces in a report |
| An exit code | — | §10 exit-code explorer (`EXITS` array) and the flowchart |
| A `just` recipe | — | §11 cheat sheet |
| An architectural invariant learned the hard way | §13 invariants table | — |

Rules that keep them trustworthy:

- **Verify before writing.** Every claim must come from the code as it is now,
  not from memory or from an older doc. Both documents pin the commit they were
  written against in their header — refresh that SHA when you revise them.
- **Mermaid must parse.** `npx -y -p @mermaid-js/mermaid-cli@11 mmdc -i x.mmd -o x.svg`
  on any diagram you touch.
- **Keep them self-contained.** Vanilla JS, no dependency beyond the Mermaid CDN
  import already present, and honour `prefers-reduced-motion`.
- **Run `just docs-audit`** (included in `just check` and `just ci-local`). It
  fails when a command, flag, variable, recipe or exit code exists in the code
  but is mentioned in neither document. It proves coverage, not correctness —
  a mentioned flag still needs an explanation worth reading.

# Dev Journal

At the end of every development session, update `docs/journal.html` with a new `<div class="session">` entry containing:
- Date and session title
- What was done (done items)
- What remains to do (todo items)
- The immediate next step

The journal is the source of truth for resuming work across sessions. Do not duplicate milestone details already in `docs/milestones/` — the journal adds the temporal dimension (when, in what order).

`docs/journal.html` is git-ignored: it is a personal working log and is never committed. Create it locally if it does not exist. Never copy book text into it, and never `git add -f` it.
