# epub-translator

Go-based EPUB EN->FR translation pipeline.

## Prerequisites
- Go (latest stable)
- Java runtime (`>=21`, tested with Java 25) for `epubcheck`
- `just` command runner
- `ripgrep` (`rg`) for helper recipe output
- `golangci-lint`

## Local Setup
1. Copy env template:
   ```bash
   cp .env.example .env.local
   ```
2. Edit `.env.local` with your NMT endpoint and API keys.
3. Keep `.env.local` as trusted local config only (`KEY=VALUE` lines).

## Run
```bash
just preflight
just run ./books/source.epub ./runs
```

## Docker Local Run
Prepare local folders and input file:
```bash
mkdir -p books runs data
# Place your source file at: ./books/source.epub
```

Build image:
```bash
just docker-build
```

Run translation in container:
```bash
just up
```

What happens:
- `docker compose` starts the `app` service from `docker-compose.yml`.
- `.env.local` is loaded into the container runtime.
- Local folders are mounted:
  - `./books` -> `/app/books`
  - `./runs` -> `/app/runs`
  - `./data` -> `/app/data`
- The container executes:
  - `epub-translator run --input /app/books/source.epub --output-dir /app/runs --source-lang en --target-lang fr`

View logs:
```bash
docker compose logs -f app
```

Stop and clean compose resources:
```bash
just down
```

## Test
```bash
just test
just fuzz
```

## Code Validation
```bash
just preflight
just fmt
just lint
just fuzz
just ci-local
just check
```

`just fuzz` runs all local fuzz tests across packages and is intentionally not included in CI to keep pipeline runtime predictable.

## CI and PR Workflow
- GitHub Actions workflow: `.github/workflows/ci.yml`
- Pull request template: `.github/pull_request_template.md`
- CI runs format check, lint, and tests automatically once `go.mod` exists.

## Notes
- Runtime config is env-only via `github.com/kelseyhightower/envconfig`.
- `Justfile` recipes source `.env.local` before running commands.
- Architecture decisions live in `docs/ARCHITECTURE.md`.
- Milestone plans live in `docs/milestones/`.
- RFCs live in `docs/rfcs/`.
