# Guide — Translating a book EN→FR

A practical, end-to-end guide: from setup to a translated volume, with or without
an LLM. For the *why* (architecture, choices), see `docs/ARCHITECTURE.md` and
`docs/rfcs/`. This is the *how*.

---

## 0. The two modes

| Mode | Engine | What for | Cost |
|---|---|---|---|
| **`nmt`** (default) | NMT (LibreTranslate / dev) + translation memory | fast draft, coverage, tests | ~free locally |
| **`literary`** | LLM (Anthropic/OpenAI/DeepSeek) guided by a **series bible** + glossary + context | translation *faithful to the narrative* | paid (LLM API) |

The translation memory (TM) and the glossary are used in both modes.

---

## 1. Common setup

```bash
cp .env.example .env.local        # then edit
just preflight                    # checks go, java, lint, etc.
```

The binary:
```bash
go build -o ./bin/tomeshift ./cmd/tomeshift
# or: just run <input.epub> <output-dir>
```

Environment variables (prefix `TOMESHIFT_`). The `just` recipes source
`.env.local`. **Never put an API key in cleartext in the repo**: `.env.local` is
gitignored; for extra safety, keep the keys in a file outside the repo and source
it.

| Variable | Role | Default |
|---|---|---|
| `NMT_PROVIDER` | `http` (dev) \| `libretranslate` \| `google` | `http` |
| `NMT_ENDPOINT` | NMT service URL; required by `run`/`batch` except for `google` | — |
| `NMT_API_KEY` | optional NMT key (required for `google`) | — |
| `NMT_FREE_CHARS_PER_MONTH` | monthly free quota (`500000` for Google) | 0 |
| `TM_PATH` | translation memory (SQLite) | `./tm.sqlite` |
| `NARRATIVE_PATH` | series bible (SQLite) | `./narrative.sqlite` |
| `LLM_CACHE_PATH` | LLM completion cache (SQLite) | `./llm_cache.sqlite` |
| `PER_BOOK_BUDGET_USD` / `DAILY_BUDGET_USD` / `MONTHLY_BUDGET_USD` | spending guardrails (hard-stop) | 2 / 5 / 50 |
| `RUN_TIMEOUT_SECONDS` | bounds a run; 0 = unlimited | 0 |
| `EPUBCHECK_JAR` / `EPUBCHECK_BIN` | epubcheck gate (skipped otherwise) | — |

---

## 2. The NMT (draft)

Always required (even in literary mode, for the fallback draft).

**Option A — bundled dev server (free, zero install)**: EN→FR dictionary,
preserves placeholders. For validating the chain, not for quality.
```bash
just nmt-dev      # listens on :8081
# .env.local: TOMESHIFT_NMT_ENDPOINT=http://localhost:8081/translate
```

**Option B — Google Cloud Translation v2 (recommended)**: this is the weak link
in the chain that changes here. The Argos draft was the worst step of the
pipeline, and replacing it costs $0 at a rate of one volume per month.
```bash
# .env.local (NMT_ENDPOINT can stay empty: the adapter knows the host):
#   TOMESHIFT_NMT_PROVIDER=google
#   TOMESHIFT_NMT_API_KEY=<API key>
#   TOMESHIFT_NMT_FREE_CHARS_PER_MONTH=500000
#   TOMESHIFT_COST_PER_1000_CHARS_USD=0.02
```
A chapter's segments go out batched (up to 128 texts or 5000 characters per
request): a volume's draft drops from about 14 minutes to roughly thirty
seconds. Rolling back fits in a single environment variable
(`NMT_PROVIDER=libretranslate`).

**Option C — LibreTranslate (real NMT, local)**:
```bash
docker compose --profile real-nmt up -d libretranslate    # first run: downloads the model
# .env.local:
#   TOMESHIFT_NMT_PROVIDER=libretranslate
#   TOMESHIFT_NMT_ENDPOINT=http://localhost:5050/translate
```

---

## 3. The LLM (literary mode)

A single OpenAI-compatible adapter covers **OpenAI** and **DeepSeek**; Anthropic
has its own adapter. Choice by config:

| Provider | `LLM_CHAT_PROVIDER` | `LLM_BASE_URL` | `LLM_MODEL` (example) |
|---|---|---|---|
| Anthropic | `anthropic` | *(empty → api.anthropic.com)* | `claude-sonnet-5` |
| OpenAI | `openai` | `https://api.openai.com/v1` | `gpt-5.4` |
| DeepSeek | `deepseek` | `https://api.deepseek.com` | `deepseek-chat` |

```bash
# .env.local (Anthropic example):
TOMESHIFT_LLM_CHAT_PROVIDER=anthropic
TOMESHIFT_LLM_MODEL=claude-sonnet-5
TOMESHIFT_LLM_API_KEY=sk-ant-...

# Rates, so the budget guardrail has something to compare against.
# Standard Sonnet 5 rate (the $2/$10 introductory rate runs until
# 2026-08-31; size the cap on the rate that follows, otherwise it will start
# rejecting runs a month later for no visible reason).
TOMESHIFT_LLM_INPUT_PER_MTOK_USD=3.00
TOMESHIFT_LLM_OUTPUT_PER_MTOK_USD=15.00
TOMESHIFT_LLM_CACHE_READ_PER_MTOK_USD=0.30
# Sonnet 5 uses the Opus 4.7 family tokenizer: ~30% more tokens for the same
# text, at the same sticker price. Without this factor the projection
# underestimates by that much.
TOMESHIFT_LLM_TOKEN_MULTIPLIER=1.30
```

> Adaptive thinking is **explicitly disabled** in the Anthropic adapter.
> Omitting it would enable it on Sonnet 5: thinking tokens are billed at the
> output rate and share the `max_tokens` cap with the JSON response, so a
> truncated response would be silently absorbed — and billed — by the
> half-batch retry.

> ⚠️ Data: the source text + the bible are sent to the provider. Respect its
> retention policy; for sensitive content, avoid hosts outside your
> jurisdiction.

---

## 4. Translating a book in literary mode (full recipe)

```bash
SERIES=lumen
EN="books/.../Volume 01.epub"

# (a) Build the bible BEFORE any translation, from ALL volumes in the
#     directory. Style/voice/address form come from all volumes; the summaries
#     are ONE synopsis per volume, produced by map-reduce (1 summary per
#     chapter, then 1 reduction). Cost: ~$0.10 (sampling) + ~$0.55 per volume,
#     ONCE, never replayed.
tomeshift series-bible --input-dir ./books/lumen --series $SERIES --samples 8
#   → writes lumen@volume-01, lumen@volume-02, … (summaries of volumes 1..N
#     only, so as not to give away the ending of a story you are starting)
#     + lumen = the bible of the most recent volume.

# (b) Review the bible (do this BEFORE a full run — an error propagates)
just bible ./narrative.sqlite $SERIES

# (c) Proper-noun glossary (free, heuristic) → JSON skeleton to curate
tomeshift glossary-suggest --input "$EN" --out ./glossary.json --min-count 5
#   edit ./glossary.json: keep the real names, correct term_fr, drop the noise

# (d) Estimate the cost BEFORE paying (dry-run, zero API calls)
#   Same cost model as the run guardrail: what `estimate` reports is what the
#   per-book cap enforces. Fill in the LLM rates above, otherwise the
#   projection is $0 and the cap compares nothing.
tomeshift estimate --input "$EN"

# (e) Translate (frozen bible → stable prompts + free re-runs via cache).
#     Target the scoped bible of the volume being translated.
tomeshift run --input "$EN" --output-dir ./runs \
  --source-lang en --target-lang fr \
  --mode literary --series $SERIES@volume-01 --glossary ./glossary.json
```

The result is in `./runs/<run-id>/translated.epub` + artifacts (see §8).

---

## 5. A multi-volume series

`series-bible` produces **one bible per volume**. The style, the voices and the
address-form rules come from **all** available volumes — that is where
completeness pays off and is nearly free. Only the **summaries** are scoped: the
bible that translates volume N contains only the synopses of volumes 1..N.

Why this bound: the series' style relies on factual description within the
*Return by Ash* loops, an effect that depends on not knowing. A model aware of
a character's true nature colors their dialogue from volume 1.

```bash
# A new volume is published → regenerate, once, from the full directory
tomeshift series-bible --input-dir ./books/lumen --series lumen --samples 8

# Translate volume 2 with the bible bounded to volumes 1 and 2
tomeshift run --input vol2.epub --output-dir ./runs \
  --mode literary --series lumen@volume-02 --glossary ./glossary.json

# Extend the glossary with the new names from volume 2:
tomeshift glossary-suggest --input vol2.epub --out glossary-vol2.json
```

Regenerating changes the prefix, so it invalidates `llm_cache.sqlite` **once** —
a one-off cost per regeneration, not per chapter. Already-translated volumes are
not re-translated automatically: leaving them as-is is cheaper and reproducible.

**Frozen bible (default) vs `--update-bible`:**
- **frozen**: every chapter sees the same bible → stable cacheable prefix,
  reproducible and free re-runs, $0.25–0.41 of prefix per volume.
- `--update-bible`: **discouraged, incompatible with a cached prefix**. The
  bible changes at every chapter, so the prefix is rewritten 68 times instead of
  once ($4.33 per volume), and the bible on disk differs from the previous run's
  → every re-run pays for everything again. The option remains available but is
  not part of any recommended workflow.

---

## 6. Cost & caches

- **`estimate`**: worst-case projection (no reuse) before any call. $0.
- **Guardrails**: `PER_BOOK_/DAILY_/MONTHLY_BUDGET_USD` → hard-stop before paying
  if the projection exceeds the cap (exit code 7).
- **SQLite LLM cache** (`LLM_CACHE_PATH`): an identical request = reused response
  → **free and instant re-run** (as long as the bible is frozen).
- **Anthropic prompt cache**: the stable prefix (bible+style+glossary) is cached
  (`cache_control`) → chapters 2…N at ~0.1× the input. Kicks in when the prefix
  exceeds ~2048 tokens (seeded bible + glossary).
- **Translation memory (TM)**: identical sentences reused across runs/volumes.

---

## 7. Evaluating quality

- **chrF (with reference)** — requires a **real FR reference of the same
  edition** (beware: web-novel ≠ light-novel give misleading scores):
  ```bash
  tomeshift eval --candidate ./runs/<id>/translated.epub --reference ref-fr.epub
  ```
- **COMET-QE (reference-free)** — the right metric when you have no aligned
  reference. Requires a COMET service (CometKiwi via FastAPI — launch with `uv`,
  Python 3.12, `huggingface-hub<1.0`; recipe in the README):
  ```bash
  TOMESHIFT_COMET_ENDPOINT=http://localhost:8000/score \
    tomeshift eval --segments ./runs/<id>/segments.jsonl
  # → COMET-QE avg = 0.xx ; writes qe-eval-report.json
  ```
  In `run` mode, COMET-QE can also score each draft and escalate the weakest
  ones to the LLM (`COMET_ENDPOINT` + `ESCALATE_BELOW_SCORE`).

---

## 8. Artifacts of a run (`./runs/<run-id>/`)

| File | Contents |
|---|---|
| `translated.epub` | the translated EPUB (structure/markup preserved) |
| `report.json` | summary: segments, TM hits, LLM segments, **tokens** (including `llm_cache_read_tokens`), estimated/actual costs, QA |
| `segments.jsonl` | one line per segment (source, target, hash, placeholders, status) — input to `eval --segments` |
| `metrics.json` | metrics (canonical names, ARCHITECTURE §10) |
| `logs.ndjson` | structured logs of the run |

---

## 9. Processing several books at once

```bash
tomeshift batch --input-dir ./books/serie --output-dir ./runs \
  --glossary ./glossary.json [--reference-dir ./refs]
# TM + glossary shared across books; aggregate report (+ chrF if --reference-dir)
```

---

## 10. Command cheat sheet

| Command | Role |
|---|---|
| `run --mode nmt\|literary [--series] [--glossary] [--resume]` | translate a book |
| `series-bible --input-dir <dir> --series <id> [--samples N] [--merge]` | build the bible for the whole series (before translating) |
| `style-profile --input <f> --series <id> [--samples N] [--merge]` | seed/enrich the bible from a single volume |
| `glossary-suggest [--out f] [--min-count N]` | extract proper nouns |
| `estimate` | project the cost (0 calls) |
| `eval --candidate/--reference` \| `--segments` | chrF \| COMET-QE |
| `batch --input-dir [--reference-dir]` | process a directory of books |

> Every command takes `--input <epub>` and `--source-lang en --target-lang fr`
> (V1). The DB paths and the LLM provider come from the environment
> (`.env.local`).

**`just` shortcuts** — each step has a recipe that sources `.env.local` for you.
`just --list` shows everything with its documented arguments. Examples:
```bash
just nmt-dev                                            # dev NMT service
just comet-up                                           # COMET service (Docker)
just seed-bible "books/vol1.epub" lumen 8              # seed the bible
just literary "books/vol1.epub" lumen ./data/lumen-glossary.json wn-ch1
just eval-ref runs/wn-ch1/translated.epub ref-fr.epub   # chrF
just eval-qe  runs/wn-ch1/segments.jsonl                # COMET-QE
just carve "books/vol1.epub" en-ch1.epub chapter_1.xhtml
just compare runs/wn-ch1/segments.jsonl wn-ch1.html
```
