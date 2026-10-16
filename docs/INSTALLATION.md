# Installation & setup

From zero to a running binary. To *translate* a book once installed, continue
with [`translation-guide.md`](translation-guide.md).

---

## 1. Prerequisites

The toolchain is described by [`mise.toml`](../mise.toml): **a single command
installs it**, at the same versions for everyone.

```bash
brew install mise 1password-cli     # the only two manual prerequisites (macOS)
```

| Tool | Why | Managed by |
|---|---|---|
| **Go** (1.26, aligned with `go.mod`) | compile/run the CLI | `mise` |
| **Java** (temurin 21) | `epubcheck` gate (optional) | `mise` |
| **just** | task runner | `mise` |
| **ripgrep** (`rg`) | output of some recipes | `mise` |
| **golangci-lint** | lint | `mise` |
| **Python** (3.12) | `scripts/*.py` (`carve`, `compare`, doc audit) | `mise` |
| **uv** | COMET-QE service — quality eval | `mise` |
| **`op`** (1Password CLI) | secret injection at launch (optional, see §3) | manual |
| **`sqlite3`** | `just bible` | provided by the OS |
| **Docker** (option) | containerized workflow | manual |
| **`epubcheck`** (option) | EPUB validation | jar to download |

> Without Java/`epubcheck`, everything works: EPUB validation is simply skipped.
> Without `uv`, everything works except COMET-QE evaluation. The 1Password CLI is
> only needed if you choose the 1Password route for secrets (§3).

---

## 2. Get the project

```bash
git clone <repo-url> tomeshift
cd tomeshift
mise trust        # mise does not run a repo's config without explicit consent
mise install      # go, java, just, golangci-lint, rg, python, uv
```

---

## 3. Configure the environment

Two files, two roles — that is the rule to remember:

| File | Contents | Versioned? |
|---|---|---|
| `.env.local` | **non-secret** configuration (provider, paths, caps, rates) | no (gitignored) |
| `.env.op` | `op://` **references** to the secrets, no values | **yes** |

```bash
cp .env.example .env.local      # local config, gitignored
```

`mise` loads `.env.local` as soon as you enter the directory; its parser only
accepts `KEY=VALUE` lines, which enforces the rule that `docs/ARCHITECTURE.md`
merely states.

### Secrets: 1Password or environment variables

API keys must never sit in cleartext in a versioned file. There are two supported
ways to feed them to a command — pick one:

**Option A — 1Password (recommended).** The keys live in a 1Password vault and
are injected **into the process, when the command launches**:

```bash
# 1. In the 1Password app: Settings → Developer → "Integrate with the CLI"
#    (this setting is what triggers the biometric prompt)
# 2. Find your vaults and accounts — there is no default name:
op account list
op vault list
# 3. Several accounts connected? Pin the one to use in .env.local:
#      OP_ACCOUNT=my.1password.eu
#    An op:// reference names a vault, never an account: without pinning,
#    a personal key may be looked up in the employer's vault, and the error
#    talks about the vault ("X isn't a vault in this account") when it is the
#    account that is wrong.
# 4. Check that a reference resolves, and adjust .env.op as needed. --account
#    is explicit: .env.local is only sourced by the `just` recipes, not by
#    your terminal.
op read --account my.1password.eu "op://Personal/anthropic-epub-translator/credential"
```

> Personal project, personal account: a key stored in an employer-managed vault
> is subject to its administration and disappears when you leave.

The recipes that call a provider are wrapped in `op run --env-file=.env.op --`;
the others are not. Concretely: `estimate`, `glossary-suggest` and `eval-ref`
**never** ask for a credential, because they make no call. The secret only
exists in the memory of the launched process, for its lifetime — neither in a
file nor in your shell.

> ⚠️ What this guarantees, exactly: the biometric prompt appears when the
> 1Password app is **locked**. App unlocked, `op` authorizes without asking
> again. The guarantee bought is therefore "the secret is never at rest on
> disk", not "a human approved this specific run". For the latter, set the app's
> auto-lock aggressively.

**Option B — environment variables.** If you would rather not use 1Password,
provide the keys as environment variables instead. Export them in your shell
before the command, or set them in your gitignored `.env.local` (which `mise`
loads when you enter the directory). This is simpler, but the key then lives in
the environment of everything you launch from that folder — which is exactly what
the `op run` wrapper exists to avoid.

Edit `.env.local`. Key variables (prefix `TOMESHIFT_`):

| Variable | Role | Example |
|---|---|---|
| `NMT_PROVIDER` | `http` (dev) \| `libretranslate` \| `google` | `http` |
| `NMT_ENDPOINT` | NMT service; required by `run`/`batch` except for `google`, which supplies its host | `http://localhost:8081/translate` |
| `NMT_API_KEY` | NMT API key — **in `.env.op`, or an env var; not here** | `op://Dev/gcp…` |
| `NMT_FREE_CHARS_PER_MONTH` | provider's monthly free quota (`500000` for Google) | `0` |
| `TM_PATH` | translation memory (SQLite) | `./data/tm.sqlite` |
| `NARRATIVE_PATH` | series bible (SQLite) | `./data/lumen-series.sqlite` |
| `LLM_CACHE_PATH` | LLM completion cache | `./data/llm-cache.sqlite` |
| `LLM_CHAT_PROVIDER` | `anthropic` \| `openai` \| `deepseek` \| `claude-cli` | `anthropic` |
| `LLM_CLI_BIN` | executable for CLI providers (empty = `claude` from `PATH`) | — |
| `LLM_MODEL` | LLM model (literary mode) | `claude-sonnet-5` |
| `LLM_TOKEN_MULTIPLIER` | tokenizer density (`1.30` for `claude-sonnet-5`) | `1.0` |
| `LLM_CACHE_READ_PER_MTOK_USD` | prompt-cache read rate (`0` = no cache) | `0.30` |
| `LLM_BATCH_SIZE` | segments per literary LLM call (`0` = internal default) | `0` |
| `LLM_API_KEY` | LLM provider key — **in `.env.op`, or an env var; not here** | `op://Dev/anthropic…` |
| `COMET_ENDPOINT` | COMET-QE service (eval) | `http://localhost:8000/score` |

> 🔐 **Keep the two `*_API_KEY` out of a versioned file.** With Option A they are
> resolved from 1Password at launch (see above). With Option B you set them as
> environment variables — in your shell or in the gitignored `.env.local` —
> accepting that they then live in the environment of everything you launch from
> that folder, which is the trade-off `op run` exists to remove.

---

## 4. Build & verify

```bash
just preflight          # config, toolchain (mise), 1Password session, NMT health
go build -o ./bin/tomeshift ./cmd/tomeshift
just check              # gofmt -s + golangci-lint + go test ./... + doc audit
```

If your shell has a broken `GOROOT` (`go: cannot find GOROOT directory`):
```bash
env -u GOROOT /usr/local/go/bin/go test ./...
```

---

## 5. Run a translation service (NMT)

The pipeline **always** needs an NMT endpoint (draft, and fallback in literary
mode). Four options:

**A. Bundled dev server** (free, zero install; toy quality):
```bash
just nmt-dev            # EN→FR dictionary on :8081
```

**B. Google Cloud Translation v2** (recommended — best draft quality):
```bash
# .env.local (NMT_ENDPOINT can stay empty: the adapter knows the host):
#   TOMESHIFT_NMT_PROVIDER=google
#   TOMESHIFT_NMT_API_KEY=<Cloud Translation API key>
#   TOMESHIFT_NMT_FREE_CHARS_PER_MONTH=500000
#   TOMESHIFT_COST_PER_1000_CHARS_USD=0.02        # $20 / M characters
```
The GCP project must have billing enabled, **even for the free tier**. That tier
is worth 500,000 characters per month, shared between Basic and Advanced, per
project: roughly one volume. Beyond that, only the overflow is billed, and the
counter is the run ledger (`nmt_chars`) — if it was consumed outside the pipeline
this month, the projection underestimates by that much.

The book's text is sent to Google. This is the same class of exposure as the
Anthropic call already in place, but Argos was local: the change is real.

**C. LibreTranslate via Docker** (real local NMT):
```bash
docker compose --profile real-nmt up -d libretranslate   # first run: downloads the model
# .env.local:
#   TOMESHIFT_NMT_PROVIDER=libretranslate
#   TOMESHIFT_NMT_ENDPOINT=http://localhost:5050/translate
```

**D. Everything in Docker** (`app` + `nmt`) — see [§ Docker](#7-docker) below.

---

## 6. (Optional) COMET-QE service for evaluation

Reference-free quality estimation. Launched via `uv` (pins in
[`requirements-comet.txt`](../requirements-comet.txt)):

```bash
uv run --python 3.12 \
  --with "unbabel-comet>=2.2" --with "huggingface-hub<1.0" \
  --with "torch<2.3" --with "setuptools<81" \
  --with fastapi --with "uvicorn[standard]" \
  -- python -m uvicorn comet_service:app --host 127.0.0.1 --port 8000
```

The first run downloads the model (~2.3 GB). Details and interpretation:
[`quality-evaluation.md`](quality-evaluation.md).

**Or via Docker** (`eval` profile, heavy but isolated image; model persisted in a
named volume):
```bash
docker compose --profile eval up -d comet     # first boot: build + model download (long)
docker compose --profile eval logs -f comet    # wait for "Application startup complete"
# then point the eval at:  TOMESHIFT_COMET_ENDPOINT=http://localhost:8000/score
```

> Two routes, your choice: `uv` (fast to iterate, nothing to build) or Docker
> (`--profile eval`, isolated from the system). The default compose stays
> dedicated to *translation*; COMET is only pulled if you explicitly enable the
> `eval` profile.

---

## 7. Docker

The `docker-compose.yml` covers **translation** end to end:

| Service | Profile | Role |
|---|---|---|
| `app` | (default) | runs the CLI (`run … source.epub` in NMT by default) |
| `nmt` | (default) | bundled dev NMT (:8081), standalone |
| `libretranslate` | `real-nmt` | real local EN→FR NMT |
| `comet` | `eval` | COMET-QE service (:8000) for quality evaluation |

```bash
mkdir -p books runs data
cp <your-book>.epub books/source.epub
just up         # build + run app (+ nmt); output in ./runs
just down       # stop + cleanup
```

**What the compose does not cover** (by design):
- **Literary mode (LLM)**: works via `app`, but the LLM is an **external API**,
  not a hosted service. To enable it in a container, adjust the `command:` of the
  `app` service (`--mode literary --series … --glossary …`). The key no longer
  arrives via `.env.local` (Compose reads it from disk) but by interpolation from
  Compose's environment — which is why `just up` is wrapped in `op run`.
- **COMET-QE**: available via the `eval` profile (`docker compose --profile eval
  up comet`) or via `uv` (see §6) — not in the default profile (heavy image).

---

## What next?

- Translate a book, step by step → [`translation-guide.md`](translation-guide.md)
- Evaluate + review line by line → [`quality-evaluation.md`](quality-evaluation.md)
- Improve quality (bible, glossary) → [`improving-translations.md`](improving-translations.md)
