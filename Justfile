set shell := ["bash", "-cu"]

# Default env file sourced by recipes that need TOMESHIFT_* variables.
# Non-secret only: secrets live in a 1Password vault and are injected per
# command via `op run` (see secret_refs below).
env_file := ".env.local"

# 1Password secret references, resolved at run time into the launched process
# and nowhere else. Only the recipes that actually call a paid provider are
# wrapped, so `estimate`, `glossary-suggest` and `eval-ref` never prompt.
secret_refs := ".env.op"

# Shell snippet setting `runner` to the `op run` prefix, but only when the run
# genuinely needs a credential — asking for one it will not use trains you to
# approve prompts without reading them.
#
# Two configurations need none: a self-hosted NMT engine, and the CLI-backed
# chat provider, which authenticates through its own subscription session.
# A recipe that calls the LLM sets uses_llm=1 before expanding this.
#
# `${runner[@]+...}` rather than `${runner[@]}`: macOS still ships bash 3.2,
# where expanding an empty array under `set -u` is an error.
op_guard := '''
runner=()
need_secret=0
if [ "${TOMESHIFT_NMT_PROVIDER:-}" = "google" ]; then need_secret=1; fi
if [ "${uses_llm:-0}" = "1" ] && [ "${TOMESHIFT_LLM_CHAT_PROVIDER:-}" != "claude-cli" ]; then
  need_secret=1
fi
if [ "$need_secret" = "1" ]; then runner=(op run --env-file=.env.op --); fi
'''

# Show available commands.
default:
    @just --list

# ── Setup & build ───────────────────────────────────────────────────────────

# Validate local prerequisites and basic runtime configuration.
#
# The toolchain itself is mise's job (mise.toml): this checks what mise does not
# manage — the config file, the 1Password session, the tools with no usable
# backend — and pings the NMT endpoint.
preflight:
    #!/usr/bin/env bash
    set -uo pipefail
    fail=0

    # Sourced first, so every check below sees the same environment a recipe
    # would — OP_ACCOUNT included.
    if [ -f "{{env_file}}" ]; then
      set -a; source "{{env_file}}"; set +a
    else
      echo "missing {{env_file}} (cp .env.example {{env_file}})"
      fail=1
    fi

    # Toolchain: one check instead of the five hand-written ones that used to
    # miss python3, sqlite3, uv and docker entirely.
    if command -v mise >/dev/null; then
      if mise ls --missing --no-header 2>/dev/null | grep -q .; then
        echo "toolchain: missing versions — run 'mise install'"
        mise ls --missing --no-header 2>/dev/null | awk '{printf "  - %s %s\n", $1, $2}'
        fail=1
      else
        echo "toolchain: ok (mise)"
      fi
    else
      echo "mise not found: install it (https://mise.jdx.dev) or provide go, java>=21, just, golangci-lint, rg, python3 and uv yourself"
      fail=1
    fi

    # Not managed by mise, on purpose — see the comments in mise.toml.
    command -v sqlite3 >/dev/null || echo "sqlite3 not found: 'just bible' will not work (non-blocking)"
    command -v docker  >/dev/null || echo "docker not found: container recipes will not work (non-blocking)"

    # Secrets. A missing op session must say so here rather than surface as a
    # raw 'op' error in the middle of a paid run.
    if [ -f "{{secret_refs}}" ]; then
      if ! command -v op >/dev/null; then
        echo "1password cli not found: recipes that call a provider will fail (see {{secret_refs}})"
        fail=1
      elif ! op whoami >/dev/null 2>&1; then
        echo "1password: not signed in — run 'op signin', or unlock the desktop app"
        fail=1
      else
        echo "1password: ok ($(op whoami --format=json 2>/dev/null | sed -n 's/.*"url": "\([^"]*\)".*/\1/p'))"
        # A secret reference names a vault, never an account. With more than one
        # account signed in, an unpinned reference resolves against whichever op
        # considers current — which is how a personal key ends up looked up in a
        # work account, or not found at all.
        if [ -z "${OP_ACCOUNT:-}" ] && [ "$(op account list --format=json 2>/dev/null | grep -c '"url"')" -gt 1 ]; then
          echo "1password: several accounts signed in and OP_ACCOUNT is unset — pin it in {{env_file}} (op account list)"
        fi
      fi
    fi

    if [ -n "${TOMESHIFT_NMT_ENDPOINT:-}" ] && command -v curl >/dev/null; then
      curl -fsS --max-time 2 "${TOMESHIFT_NMT_ENDPOINT%/}/health" >/dev/null 2>&1 && \
        echo "nmt endpoint health: ok" || \
        echo "nmt endpoint health: unavailable (non-blocking)"
    else
      echo "nmt endpoint health: skipped (curl or endpoint missing)"
    fi

    exit $fail

# Build the CLI binary to ./bin/tomeshift.
build:
    go build -o ./bin/tomeshift ./cmd/tomeshift

# Print which env file will be sourced.
show-env:
    @echo "env file: {{env_file}}"

# Show the resolved TOMESHIFT_* variables.
print-config-vars:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    env | rg '^TOMESHIFT_' || true

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
    {{op_guard}}
    "${runner[@]+"${runner[@]}"}" go run ./cmd/tomeshift run \
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
    uses_llm=1
    {{op_guard}}
    "${runner[@]+"${runner[@]}"}" go run ./cmd/tomeshift "${args[@]}"

# Project the LLM cost of a book before paying (0 API calls). Arg: input EPUB.
# Deliberately NOT wrapped in `op run`: it makes no provider call, so it must
# not ask for a credential to do arithmetic.
estimate input:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/tomeshift estimate --input "{{input}}"

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
    {{op_guard}}
    "${runner[@]+"${runner[@]}"}" go run ./cmd/tomeshift "${args[@]}"

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
    uses_llm=1
    {{op_guard}}
    "${runner[@]+"${runner[@]}"}" go run ./cmd/tomeshift "${args[@]}"

# Build the series bible BEFORE translating, from every volume in a directory
# (RFC 0008). Style/voices/address are seeded from all volumes; summaries are one
# synopsis per volume, produced by map-reduce, and scoped to volumes 1..N.
# Writes one bible per volume under "<series>@<volume>", plus the full series
# under "<series>". Re-run only when a new volume is published.
# Cost: one call per non-empty chapter, plus one per volume, plus one for style
# — once, never replayed. Price it first with `just series-bible-cost`.
#
# samples defaults to 3, matching the CLI. It used to default to 8, which is a
# per-volume-looking number with a series-wide effect: the style pass sends
# every sampled chapter of every volume in ONE request, so 8 across 33 volumes
# is a 966,000-token call — past Haiku 4.5's context window entirely.
#   just series-bible "epub/lumen" lumen 3
#   just series-bible "epub/lumen" lumen 3 merge
series-bible input_dir series samples="3" merge="":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    args=(series-bible --input-dir "{{input_dir}}" --series "{{series}}" --samples "{{samples}}")
    if [ "{{merge}}" = "merge" ]; then args+=(--merge); fi
    uses_llm=1
    {{op_guard}}
    "${runner[@]+"${runner[@]}"}" go run ./cmd/tomeshift "${args[@]}"

# Project the cost of a bible build before paying (0 API calls, no credential).
# `estimate` prices translating a book and cannot price this; the bible build is
# the largest job here — one call per chapter across every volume.
#   just series-bible-cost ./epub/lumen lumen 3
series-bible-cost input_dir series samples="3":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/tomeshift series-bible \
      --input-dir "{{input_dir}}" --series "{{series}}" \
      --samples "{{samples}}" --dry-run

# Pretty-print a series bible from a SQLite DB. Args: db path, series id.
#   just bible ./data/lumen-series.sqlite lumen
bible db series:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "── version ──"
    sqlite3 "{{db}}" "SELECT series_id || ' v' || version FROM bibles WHERE series_id='{{series}}';"
    echo; echo "── style_guide ──"
    sqlite3 "{{db}}" "SELECT style_guide FROM bibles WHERE series_id='{{series}}';"
    echo; echo "── characters ──"
    sqlite3 "{{db}}" "SELECT characters_json FROM bibles WHERE series_id='{{series}}';" | python3 -m json.tool
    echo; echo "── address (tu/vous) ──"
    sqlite3 "{{db}}" "SELECT address_json FROM bibles WHERE series_id='{{series}}';" | python3 -m json.tool
    echo; echo "── summaries (one synopsis per volume) ──"
    sqlite3 "{{db}}" "SELECT summaries_json FROM bibles WHERE series_id='{{series}}';" | python3 -m json.tool
    echo; echo "── chapter summaries kept as a by-product ──"
    sqlite3 "{{db}}" "SELECT volume_id || ' ' || count(*) || ' chapters' FROM chapter_summaries WHERE series_id='{{series}}' GROUP BY volume_id;"

# Extract proper-noun glossary candidates from a book (free, heuristic).
# Args: input, optional output path, optional min occurrence count.
glossary-suggest input out="glossary.json" min_count="5":
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/tomeshift glossary-suggest \
      --input "{{input}}" --out "{{out}}" --min-count "{{min_count}}"

# ── Evaluation & review ───────────────────────────────────────────────────────

# chrF against a human reference EPUB (same edition required). Args: candidate, reference.
eval-ref candidate reference:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/tomeshift eval --candidate "{{candidate}}" --reference "{{reference}}"

# COMET-QE (reference-free) on a run's segments.jsonl — needs a COMET service up
# (just comet-dev / comet-up). Arg: path to segments.jsonl.
eval-qe segments:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    go run ./cmd/tomeshift eval --segments "{{segments}}"

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
    docker build -t tomeshift:local .

# Run the containerized NMT workflow (app + nmt).
# Wrapped in `op run` when a secret is needed, so Compose can interpolate the
# API keys into the app service; without it the container starts with empty
# credentials, which is correct for the default dev-NMT workflow and wrong for
# anything paid.
up:
    #!/usr/bin/env bash
    set -euo pipefail
    set -a; source "{{env_file}}"; set +a
    uses_llm=1
    {{op_guard}}
    "${runner[@]+"${runner[@]}"}" docker compose up --build app

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

# Check that the illustrated HTML docs still cover the code's user-facing
# surface: every command, flag, TOMESHIFT_* variable, recipe and exit
# code must be mentioned in docs/usage.html or
# docs/understanding-the-project.html. Fails when a feature shipped without the docs
# being reopened. See AGENTS.md § Living Documentation.
docs-audit:
    python3 scripts/docs_audit.py

# Full local validation.
check: fmt lint test docs-audit

# Run CI-equivalent checks locally.
ci-local: preflight fmt lint test docs-audit
