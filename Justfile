set shell := ["bash", "-cu"]

# Default env file used by recipes that need environment variables.
env_file := ".env.local"

# Show available commands.
default:
    @just --list

# Print which env file will be sourced.
show-env:
    @echo "env file: {{env_file}}"

# Validate environment loading quickly.
print-config-vars:
    set -a
    source "{{env_file}}"
    set +a
    env | rg '^EPUB_TRANSLATOR_' || true

# Run the app after sourcing env vars from env_file.
run input output_dir:
    set -a
    source "{{env_file}}"
    set +a
    go run ./cmd/epub-translator run \
      --input "{{input}}" \
      --output-dir "{{output_dir}}" \
      --source-lang en \
      --target-lang fr

# Build app container image.
docker-build:
    docker build -t epub-translator:local .

# Run local container workflow (V1 profile).
up:
    docker compose up --build app

# Stop and remove compose resources.
down:
    docker compose down --remove-orphans

# Validate local prerequisites and basic runtime configuration.
preflight:
    test -f "{{env_file}}" || (echo "missing {{env_file}} (copy .env.example first)"; exit 1)
    command -v go >/dev/null || (echo "missing dependency: go"; exit 1)
    command -v java >/dev/null || (echo "missing dependency: java"; exit 1)
    command -v golangci-lint >/dev/null || (echo "missing dependency: golangci-lint"; exit 1)
    command -v rg >/dev/null || (echo "missing dependency: rg"; exit 1)
    set -a; source "{{env_file}}"; set +a; \
      if [ -n "${EPUB_TRANSLATOR_NMT_ENDPOINT:-}" ] && command -v curl >/dev/null; then \
        curl -fsS --max-time 2 "${EPUB_TRANSLATOR_NMT_ENDPOINT%/}/health" >/dev/null && \
        echo "nmt endpoint health: ok" || \
        echo "nmt endpoint health: unavailable (non-blocking)"; \
      else \
        echo "nmt endpoint health: skipped (curl or endpoint missing)"; \
      fi

# Format Go code.
fmt:
    gofmt -w -s $(find . -name '*.go' -not -path './vendor/*')

# Lint project.
lint:
    golangci-lint run

# Run unit and integration tests after sourcing env vars.
test:
    set -a
    source "{{env_file}}"
    set +a
    go test ./...

# Full local validation.
check: fmt lint test

# Run CI-equivalent checks locally.
ci-local: preflight fmt lint test
