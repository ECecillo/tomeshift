# EPUB Translator

EPUB Translator is a Go CLI pipeline that translates DRM-free EPUB files from English to French while preserving EPUB structure and formatting.

The application is built as a **Go CLI project** with:
- CLI entrypoint (`epub-translator`)
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
│   └── epub-translator/              - CLI entrypoint (`main.go`)
├── internal/                         - implementation packages (epub, segment, tm, translate, qa)
├── tests/
│   └── integration/                  - integration tests and fixtures
├── docs/
│   ├── ARCHITECTURE.md               - architecture and constraints (source of truth)
│   ├── DEPENDENCIES.md               - dependency policy
│   ├── milestones/                   - milestone plans and delivery sequencing
│   └── rfcs/                         - implementation/infrastructure proposals
├── Justfile                          - local development commands
├── go.mod / go.sum                   - Go module dependencies
├── README.md                         - local dev and env setup
└── Dockerfile / docker-compose.yml   - local container workflow
```

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
  - Symptom: config load errors for `EPUB_TRANSLATOR_*` variables.
  - Fix: copy `.env.example` to `.env.local`, source it, and rerun commands.

- **Local container workflow issues**:
  - Verify Docker/Compose is available.
  - Verify input/output directories are present and mounted as expected.

## Useful References

- Local development setup: `README.md`
- Architecture and constraints: `docs/ARCHITECTURE.md`
- Milestone execution plan: `docs/milestones/M1-core-mvp.md`
- Dependency policy: `docs/DEPENDENCIES.md`

# Dev Journal

At the end of every development session, update `docs/journal.html` with a new `<div class="session">` entry containing:
- Date and session title
- What was done (done items)
- What remains to do (todo items)
- The immediate next step

The journal is the source of truth for resuming work across sessions. Do not duplicate milestone details already in `docs/milestones/` — the journal adds the temporal dimension (when, in what order).
