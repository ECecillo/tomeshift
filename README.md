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

### Literary mode with a series bible
Seed or refresh the series bible first:
```bash
epub-translator style-profile --input ./books/volume-01.epub --series my-series --samples 3
```

Then run the sequential literary pipeline:
```bash
epub-translator run \
  --input ./books/volume-01.epub \
  --output-dir ./runs \
  --source-lang en \
  --target-lang fr \
  --mode literary \
  --series my-series
```

Literary mode still uses NMT/translation memory for drafts, then calls the
configured chat model with the persisted bible, glossary, and adjacent chapter
context. Configure `EPUB_TRANSLATOR_NARRATIVE_PATH`,
`EPUB_TRANSLATOR_LLM_CHAT_PROVIDER`, `EPUB_TRANSLATOR_LLM_BASE_URL`,
`EPUB_TRANSLATOR_LLM_MODEL`, and `EPUB_TRANSLATOR_LLM_API_KEY`.

### Self-hosted local NMT (no external service)
The default `EPUB_TRANSLATOR_NMT_ENDPOINT` points at a bundled local NMT dev
server so the pipeline runs end-to-end with no external dependency. Start it in
another terminal:
```bash
just nmt-dev        # serves the dev NMT on :8081 (dictionary EN→FR)
```
This dev server is for local development only — it does dictionary-based,
word-level substitution and preserves placeholder tokens. With Docker, `just up`
starts the `nmt` service and the `app` together automatically.

### Quality estimation with COMET-QE (self-hosted)
The pipeline can score each NMT draft (reference-free) and escalate only the
weakest segments to the LLM. It expects a COMET scoring service with this
contract:
```
POST <COMET_ENDPOINT>
  { "samples": [ {"src": "...", "mt": "...", "ref": "..."} ] }   # ref optional (QE)
→ { "scores": [0.83, ...] }
```
A minimal CometKiwi (reference-free) service:
```python
# pip install "unbabel-comet>=2.2" fastapi uvicorn
from comet import download_model, load_from_checkpoint
from fastapi import FastAPI
model = load_from_checkpoint(download_model("Unbabel/wmt22-cometkiwi-da"))
app = FastAPI()
@app.post("/score")
async def score(body: dict):
    data = [{"src": s["src"], "mt": s["mt"]} for s in body["samples"]]
    out = model.predict(data, batch_size=8, gpus=0)
    return {"scores": out.scores}
```
Enable it:
```bash
EPUB_TRANSLATOR_COMET_ENDPOINT=http://localhost:8000/score
EPUB_TRANSLATOR_ESCALATE_BELOW_SCORE=0.80   # escalate drafts scoring below this
```
The per-book average score lands in `report.json` (`avg_quality_score`) and
`metrics.json` (`translation_quality_score`).

### Real translations with LibreTranslate (self-hosted)
For actual EN→FR translation, run a real local engine. LibreTranslate is the
simplest to self-host:
```bash
# Docker (first start downloads the en→fr model):
docker compose --profile real-nmt up libretranslate
# or standalone:
docker run -ti -p 5000:5000 libretranslate/libretranslate --load-only en,fr
```
Then point the app at it:
```bash
EPUB_TRANSLATOR_NMT_PROVIDER=libretranslate
EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:5000/translate
```
The adapter sends `format=html`, so inline placeholder tokens survive
translation. Heavier engines (OPUS-MT, NLLB-200) can be used behind the same
`libretranslate`-compatible contract or by adding a small adapter.

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
