set shell := ["bash", "-cu"]

# Default env file sourced by recipes that need EPUB_TRANSLATOR_* variables.
env_file := ".env.local"

# Show available commands.
default:
    @just --list

# ── Setup & build ───────────────────────────────────────────────────────────

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

# Build the CLI binary to ./bin/epub-translator.
build:
    go build -o ./bin/epub-translator ./cmd/epub-translator

# Print which env file will be sourced.
show-env:
    @echo "env file: {{env_file}}"

# Show the resolved EPUB_TRANSLATOR_* variables.
print-config-vars:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    env | rg '^EPUB_TRANSLATOR_' || true

# ── Translation services ──────────────────────────────────────────────────────

# Run the bundled dev NMT (dictionary EN→FR) in the foreground on :8081.
nmt-dev:
    go run ./cmd/nmt-devserver

# Start a real local NMT (LibreTranslate) via Docker on :5050 (real-nmt profile).
libretranslate-up:
    docker compose --profile real-nmt up -d libretranslate

# Run the COMET-QE eval service locally via uv (foreground, :8000).
comet-dev:
    uv run --python 3.12 \
      --with "unbabel-comet>=2.2" --with "huggingface-hub<1.0" \
      --with "torch<2.3" --with "setuptools<81" \
      --with fastapi --with "uvicorn[standard]" \
      -- python -m uvicorn comet_service:app --host 127.0.0.1 --port 8000

# Start the COMET-QE eval service via Docker on :8000 (eval profile).
comet-up:
    docker compose --profile eval up -d comet

# Stop the dockerized COMET-QE service (keeps the model-cache volume).
comet-down:
    docker compose --profile eval stop comet

# ── Translate a book ──────────────────────────────────────────────────────────

# NMT translation (fast draft). Args: input EPUB, optional output dir.
#   just run ./books/source.epub ./runs
run input output_dir="./runs":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/epub-translator run \
      --input "{{input}}" --output-dir "{{output_dir}}" \
      --source-lang en --target-lang fr

# Literary translation (LLM + series bible + glossary). Bible path & LLM provider
# come from {{env_file}} (NARRATIVE_PATH, LLM_*). Args: input, series,
# optional glossary path, optional run-id.
#   just literary "books/vol1.epub" lumen ./data/lumen-glossary.json wn-ch1
literary input series glossary="" run_id="":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    args=(run --input "{{input}}" --output-dir ./runs --source-lang en --target-lang fr --mode literary --series "{{series}}")
    if [ -n "{{glossary}}" ]; then args+=(--glossary "{{glossary}}"); fi
    if [ -n "{{run_id}}" ]; then args+=(--run-id "{{run_id}}"); fi
    go run ./cmd/epub-translator "${args[@]}"

# Project the LLM cost of a book before paying (0 API calls). Arg: input EPUB.
estimate input:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/epub-translator estimate --input "{{input}}"

# Translate every EPUB in a directory (shared TM + glossary). Args: input dir,
# optional glossary, optional reference dir (enables chrF in the aggregate report).
#   just batch ./books/serie ./data/glossary.json ./refs
batch input_dir glossary="" reference_dir="":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    args=(batch --input-dir "{{input_dir}}" --output-dir ./runs)
    if [ -n "{{glossary}}" ]; then args+=(--glossary "{{glossary}}"); fi
    if [ -n "{{reference_dir}}" ]; then args+=(--reference-dir "{{reference_dir}}"); fi
    go run ./cmd/epub-translator "${args[@]}"

# ── Series bible & glossary ───────────────────────────────────────────────────

# Seed/refresh the series bible from a volume (1 LLM call). Args: input, series,
# optional sample count. Add a 4th arg "merge" to enrich without overwriting.
#   just seed-bible "books/vol1.epub" lumen 8
#   just seed-bible "books/vol2.epub" lumen 8 merge
seed-bible input series samples="8" merge="":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    args=(style-profile --input "{{input}}" --series "{{series}}" --samples "{{samples}}")
    if [ "{{merge}}" = "merge" ]; then args+=(--merge); fi
    go run ./cmd/epub-translator "${args[@]}"

# Extract proper-noun glossary candidates from a book (free, heuristic).
# Args: input, optional output path, optional min occurrence count.
glossary-suggest input out="glossary.json" min_count="5":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/epub-translator glossary-suggest \
      --input "{{input}}" --out "{{out}}" --min-count "{{min_count}}"

# ── Evaluation & review ───────────────────────────────────────────────────────

# chrF against a human reference EPUB (same edition required). Args: candidate, reference.
eval-ref candidate reference:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/epub-translator eval --candidate "{{candidate}}" --reference "{{reference}}"

# COMET-QE (reference-free) on a run's segments.jsonl — needs a COMET service up
# (just comet-dev / comet-up). Arg: path to segments.jsonl.
eval-qe segments:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/epub-translator eval --segments "{{segments}}"

# Carve a single chapter out of an EPUB. Args: src, out, then one+ href substrings.
#   just carve "books/vol1.epub" en-ch1.epub chapter_1.xhtml
carve src out +hrefs:
    python3 scripts/carve_chapter.py "{{src}}" "{{out}}" {{hrefs}}

# Build a 2-column (EN | our FR) HTML review from a run's segments.jsonl.
#   just compare runs/wn-ch1/segments.jsonl wn-ch1.html
compare segments out:
    python3 scripts/compare_sidebyside.py "{{segments}}" "{{out}}"

# ── Docker (full local workflow) ──────────────────────────────────────────────

# Build app container image.
docker-build:
    docker build -t epub-translator:local .

# Run the containerized NMT workflow (app + nmt).
up:
    docker compose up --build app

# Stop and remove compose resources.
down:
    docker compose down --remove-orphans

# ── Dev / CI ──────────────────────────────────────────────────────────────────

# Format Go code.
fmt:
    gofmt -w -s $(find . -name '*.go' -not -path './vendor/*')

# Lint project.
lint:
    golangci-lint run

# Run unit and integration tests after sourcing env vars.
test:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go test ./...

# Run all local fuzz tests across packages (not part of CI).
fuzz fuzz_time="10s":
    pkgs=$(go list ./...); \
    ran=0; \
    for pkg in $pkgs; do \
      if go test "$pkg" -list '^Fuzz' | rg -q '^Fuzz'; then \
        echo "fuzzing $pkg"; \
        go test "$pkg" -run=^$ -fuzz=. -fuzztime={{fuzz_time}}; \
        ran=1; \
      fi; \
    done; \
    if [ $ran -eq 0 ]; then \
      echo "no fuzz tests found"; \
    fi

# Full local validation.
check: fmt lint test

# Run CI-equivalent checks locally.
ci-local: preflight fmt lint test
