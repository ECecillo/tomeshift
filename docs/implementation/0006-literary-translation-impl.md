# Implementation Plan — RFC 0006 Faithful Multi-Volume Literary Translation

- Status: `ready for execution`
- Implements: `docs/rfcs/0006-faithful-multivolume-translation.md`
- Audience: an executor LLM/agent implementing the work; reviewed by Claude.
- Created: 2026-06-20

This plan is **prescriptive and ordered**. Execute phases in sequence; each
phase is independently buildable, tested, and committed before the next.
Do not skip the RED step. Do not begin a phase before its predecessor is green
and committed. If a proposed signature conflicts with reality, stop and flag it
for review rather than inventing an alternative.

---

## 0. Ground rules (read before any code)

**House conventions (match existing code exactly):**
- Module path: `github.com/ECecillo/tomeshift`. Go 1.26.
- One package per directory; every package has a `doc.go` and, where it returns
  domain errors, an `errors.go` with sentinel `var Err... = errors.New(...)`.
- Wrap errors as `fmt.Errorf("unable to <context>, err: %w", err)`.
- Tests: table-driven, `t.Parallel()`, `testify/require`, `t.Context()`.
  **No live network in unit/integration tests** — use mock servers.
- After every Go change, run **all three**:
  `env -u GOROOT gofmt -w -s .` · `env -u GOROOT go test ./...` ·
  `golangci-lint run` (must be `0 issues`).
  Never put `env -u GOROOT` inside Justfile recipes.
- Commits: Conventional Commits, one logical change per commit, trailer
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Do **not** commit `books/`, `runs/`, `data/`, `*.sqlite` (already gitignored).
- At the end of the work, add a session entry to `docs/journal.html`.

**Reference implementations to imitate** (study these first):
- Provider factory + adapter + reusable mock:
  `internal/translate/provider.go`, `internal/translate/libretranslate.go`,
  `internal/translate/nmttest/server.go`.
- LLM contract + strict placeholder guard + mock:
  `internal/llm/http.go`, `internal/llm/llmtest/server.go`.
- Placeholder tokenize/restore (and the `<x></x>` paired-form handling):
  `internal/segment/placeholder.go` (+ `_test.go`).
- SQLite store style: `internal/tm/store.go`.
- CLI flag parsing + command dispatch: `internal/cli/*.go`,
  `internal/commandexec/execute.go`, `cmd/tomeshift/{config,main}.go`.

**Global definition of done:** all phases green, `golangci-lint` 0 issues,
integration test exercises literary mode end-to-end against mocks, no live API
in CI, journal updated.

---

## Phase 1 — `internal/llm`: generic chat client + OpenAI-compatible adapter + mock

**Goal:** a provider-agnostic chat-completion client. One OpenAI-compatible
adapter reaches OpenAI **and** DeepSeek (base URL + model from config); the
Anthropic Messages adapter is a follow-up (Phase 1b, optional for the bake-off).

**Files:**
- `internal/llm/chat.go` — interface + types.
- `internal/llm/openai_compatible.go` — adapter.
- `internal/llm/openaitest/server.go` — reusable mock (`/v1/chat/completions`).
- `internal/llm/openai_compatible_test.go`, `internal/llm/chat_test.go`.

**Proposed signatures:**
```go
// chat.go
type ChatMessage struct{ Role, Content string } // role: "system"|"user"|"assistant"
type ChatRequest struct {
    Model       string
    Messages    []ChatMessage
    MaxTokens   int
}
type ChatResponse struct {
    Text         string
    InputTokens  int
    OutputTokens int
}
type ChatClient interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// openai_compatible.go
type OpenAICompatibleClient struct {
    BaseURL string // e.g. https://api.openai.com/v1 or https://api.deepseek.com
    APIKey  string
    Client  *http.Client
}
func NewOpenAICompatibleClient(baseURL, apiKey string) *OpenAICompatibleClient
func (c *OpenAICompatibleClient) Chat(ctx, req) (ChatResponse, error)
```
- Wire contract: `POST {BaseURL}/chat/completions`, body
  `{"model":...,"messages":[{"role","content"}],"max_tokens":...}`, header
  `Authorization: Bearer <key>`. Parse `choices[0].message.content` and
  `usage.{prompt_tokens,completion_tokens}`.
- Errors (new in `internal/llm/errors.go`, reuse names where they exist):
  `ErrEmptyBaseURL`, reuse `ErrProviderStatus`, `ErrEmptyResponse`.

**RED tests first (`openai_compatible_test.go`, package `llm_test`):**
1. `TestOpenAICompatible_Chat` — mock returns a known string → `resp.Text` matches; usage parsed.
2. `TestOpenAICompatible_SendsBearer` — asserts `Authorization: Bearer k`.
3. `TestOpenAICompatible_EmptyBaseURL` → `ErrEmptyBaseURL`.
4. `TestOpenAICompatible_NonOKStatus` (mock 500) → `ErrProviderStatus`.
5. `TestOpenAICompatible_EmptyChoices` → `ErrEmptyResponse`.
- `openaitest.NewServer(reply func(req) string)`: minimal httptest server
  speaking the OpenAI chat contract (mirror `nmttest`/`llmtest`).

**DoD:** tests pass; lint 0. **Commit:** `feat(llm): OpenAI-compatible chat client + mock`.

**Reviewer checklist (Claude):** wire contract matches OpenAI spec; DeepSeek
reachable by base-URL swap only; mock not linked into the binary; errors follow
sentinel style; usage tokens parsed for the cost model.

### Phase 1b (optional, before bake-off) — Anthropic chat adapter
`internal/llm/anthropic.go` implementing `ChatClient` against the Messages API
(`/v1/messages`, `x-api-key`, `anthropic-version` header, model from config,
adaptive thinking omitted/default). Mock + tests mirroring Phase 1.
**Commit:** `feat(llm): Anthropic Messages chat adapter`.

---

## Phase 2 — `internal/narrative`: the series bible

**Goal:** persisted, versioned, series-scoped narrative state, including the
tu/vous address matrix with an evolution log.

**Files:** `internal/narrative/{doc,errors,types,store}.go` + `store_test.go`.
Storage: SQLite via `modernc.org/sqlite` (imitate `internal/tm/store.go`); use a
fresh DB file per test (`t.TempDir()`), `SetMaxOpenConns(1)`.

**Proposed types:**
```go
type CharacterVoice struct {
    Name, Register, Notes string
    SampleFR []string
}
type AddressChange struct { AtChapter, Note string } // evolution log entry
type AddressRule struct {
    From, To string        // ordered character pair
    Form     string        // "tu" | "vous"
    History  []AddressChange
}
type ChapterSummary struct { ChapterID, Summary string }
type Bible struct {
    SeriesID    string
    StyleGuide  string
    Characters  []CharacterVoice
    Address     []AddressRule
    Summaries   []ChapterSummary
    Version     int
}
type Store interface {
    Load(ctx, seriesID string) (Bible, bool, error)
    Save(ctx, b Bible) error                 // bumps Version
    AppendSummary(ctx, seriesID string, s ChapterSummary) error
    SetAddress(ctx, seriesID string, r AddressRule) error // append to History on change
    Close() error
}
```
- Glossary stays in `internal/glossary` (the bible references it; don't
  duplicate locked terms here).

**RED tests:** save/load round-trip; version bump on save; `SetAddress` appends
to `History` only when `Form` changes (no-op when unchanged); `AppendSummary`
ordering; load-miss returns `found=false`; empty `SeriesID` → `ErrEmptySeriesID`.
Add `TestAddressMatrix_EvolutionLogged` and `TestTuVousConsistency` (a helper
`func Consistent(b Bible, from,to,form string) bool`).

**DoD/Commit:** `feat(narrative): series bible store with address matrix`.

**Reviewer checklist:** address evolution actually logged (not overwritten);
versioning monotonic; series-scoped; no glossary duplication; SQLite single-conn.

---

## Phase 3 — Bible seeding (`style-profile`) + bible-update pass

**Goal:** bootstrap the bible from volume 1, and update it after each chapter.

**Files:** `internal/narrative/profiler.go` (+ test), CLI wiring in
`internal/cli/` (`ParseStyleProfileArgs`) and `internal/commandexec/execute.go`.

**Design:**
- `Profiler{ Chat llm.ChatClient }` with
  `Seed(ctx, samples []string) (Bible, error)` — prompts the chat client to
  produce style guide + character voices from sample chapters (returns parsed
  JSON; define a strict prompt + `encoding/json` parse, validate non-empty).
- `Update(ctx, b Bible, chapterID, sourceText, targetText string) (Bible, error)`
  — extracts new terms/voice notes/address changes from the just-translated
  chapter; appends to the bible.
- Tests use `llm/openaitest` (or a `ChatClient` stub returning canned JSON) —
  **offline**. Assert: seed produces a populated bible; update appends a summary
  and logs an address change when the canned response indicates one; malformed
  JSON → wrapped error (no panic).
- CLI: `tomeshift style-profile --input <epub> --series <id> [--samples N]`
  → writes/updates the bible; prints a summary. Mirror `glossary-suggest` wiring.

**DoD/Commit:** `feat(narrative): style-profile seeding + bible-update pass`.

**Reviewer checklist:** prompts are deterministic-friendly; JSON parse is strict
with actionable errors; no live API in tests; CLI flags validated like the
existing commands; human-reviewable output.

---

## Phase 4 — `internal/literary`: the literary translator

**Goal:** translate a chapter's segments with bible + adjacent context +
glossary, preserving placeholders, behind the `ChatClient` interface.

**Files:** `internal/literary/{doc,errors,translator,prompt}.go` + tests.

**Design:**
```go
type Translator struct {
    Chat       llm.ChatClient
    Model      string
    Glossary   *glossary.Glossary // optional
}
// TranslateChapter returns segments with TargetText filled; placeholders intact.
func (t *Translator) TranslateChapter(
    ctx context.Context, b narrative.Bible, chapterID string,
    segs []segment.Segment, prevCtx, nextCtx string,
) ([]segment.Segment, error)
```
- `prompt.go`: compose the system+user messages — **stable prefix first**
  (style guide + relevant bible slice = characters present + address rules +
  glossary) so it can be prompt-cached, **volatile content last** (this
  chapter's segments + adjacent context). Reuse glossary `Protect`/`Apply`
  around the call so locked terms are guaranteed.
- Placeholder safety: the LLM must return each segment's text with the
  `<x id="N"/>` tokens intact; reuse `segment.InlinePlaceholderManager.Restore`
  (already tolerant of the `<x></x>` paired form). On a per-segment placeholder
  mismatch, **fall back to the NMT draft for that segment** (mirror
  `llm.SafePostEdit` semantics) and flag it — never emit broken markup.

**RED tests (offline, stub `ChatClient`):**
1. Composed prompt contains the style guide, the present characters' address
   rules, and the glossary (assert substrings).
2. Stable prefix precedes volatile content (cache-friendliness).
3. Round-trip: stub echoes tokens → placeholders restored, markup valid.
4. Stub drops a token → that segment falls back to its NMT draft, run continues.
5. Glossary locked term enforced in output.

**DoD/Commit:** `feat(literary): bible+context literary translator`.

**Reviewer checklist:** voice consistency (single model per book); prompt prefix
stable for caching; placeholder fallback never produces broken markup; glossary
100%; no live API in tests.

---

## Phase 5 — Pipeline literary mode + config + CLI

**Goal:** wire literary mode into the orchestrator: sequential chapters, bible
carry-over, bible-update stage, provider/model config, `--mode literary`.

**Files:** `internal/pipeline/orchestrator.go` (add a literary path or a
`LiteraryOrchestrator`), `internal/pipeline/types.go` (RunConfig: `Mode`,
`SeriesID`), `cmd/tomeshift/config.go` + `main.go` +
`internal/commandexec/execute.go` (LLM provider/base-URL/key/model env +
`--mode` flag), `internal/cli/flags.go`.

**Design:**
- New env (mirror existing): `TOMESHIFT_LLM_CHAT_PROVIDER`
  (`anthropic|openai`), `..._LLM_BASE_URL`, `..._LLM_MODEL`, reuse `..._LLM_API_KEY`.
  Add a chat-client factory in `internal/llm` (`NewChatClient(provider,baseURL,key)`),
  mirroring `translate.NewProvider`.
- Literary path: extract → parse → **for each spine chapter in order**: segment
  → TM lookup (reuse) → `literary.TranslateChapter` (bible+context) → glossary
  enforce → restore → bible-update (`narrative.Update`) → carry bible forward →
  QA. Recompose. Emit artifacts (reuse existing) + bible version in the report.
- Budget: reuse M2 cost model + caps + `estimate` (extend the estimator to count
  bible/context overhead so the dry-run stays honest).

**RED tests:** integration test in `tests/integration` — fixture multi-chapter
EPUB + `openaitest`/stub chat → asserts: chapters processed in spine order,
bible carried across chapters (a term learned in ch.1 present for ch.2),
placeholders 100%, QA gate green, artifacts written, `--mode literary` selected.

**DoD/Commit:** `feat(pipeline): literary translation mode wired end-to-end`.

**Reviewer checklist:** chapters strictly sequential; bible threaded forward and
persisted; provider/model purely config; budget caps honored; integration test
offline; V1 NMT mode unchanged.

---

## Phase 6 — Fidelity judge (LLM-as-judge) + escalation

**Goal:** score translated segments on a fidelity rubric; flag/escalate low
scores (reuse the M3 quality-driven escalation seam).

**Files:** `internal/literary/judge.go` (+ test), wire into the pipeline
literary path and into `internal/qa` report (`FidelityScore`, low-score count).

**Design:** `Judge{ Chat llm.ChatClient; Model string }` with
`Score(ctx, b Bible, source, target string) (score float64, notes string, err error)`
against a rubric (voice match, register, naturalness, faithfulness). Low scores
→ escalate that segment to the stronger model (reuse `EscalateMinChars`-style
hook) or flag for human review. Judge/bible-update may use a **cheaper model**
than the translator (config) — mixing is allowed for auxiliary passes only.

**RED tests:** stub judge returns a low score → segment flagged/escalated; high
score → kept; rubric prompt contains the bible voice context; offline.

**DoD/Commit:** `feat(literary): fidelity judge + quality escalation`.

**Reviewer checklist:** judge model configurable and separate from translator;
escalation reuses existing seam; report surfaces fidelity metrics; offline tests.

---

## Phase 7 — Bake-off harness + docs (manual, real API)

**Goal:** a repeatable way to translate one chapter across providers and compare
— the decision tool, run by a human with real keys.

**Files:** `cmd/tomeshift` `bakeoff` subcommand (or a documented `just`
recipe) that runs `literary.TranslateChapter` for N configured
provider/model combos on the same chapter + bible, writing each output + the
judge score to `runs/bakeoff/<model>/`. README section documenting accounts
needed (Anthropic / OpenAI / DeepSeek) and the `estimate` dry-run first.

**DoD/Commit:** `feat(cli): provider bake-off harness` + `docs: bake-off + accounts`.

**Reviewer checklist:** no real API in CI; bake-off is opt-in and budget-gated;
README states which account each provider needs (verify current pricing/free
credits on each provider's own page — do not assert a free tier from secondary
sources); `estimate` runnable before any spend. **Primary/default provider:
Anthropic** (the account we have credit on); OpenAI/DeepSeek are optional
bake-off comparators added only if the user opens those accounts.

---

## Execution checklist (run after every phase)

```bash
env -u GOROOT gofmt -w -s .
env -u GOROOT go build ./...
env -u GOROOT go test ./...        # all green
golangci-lint run                  # 0 issues
```
Then commit with the conventional message above. After Phase 7, update
`docs/journal.html` with a session entry (done / todo / next step) and, if the
bible/provider design shifted during execution, bump RFC 0006's Decision Log.

## What to flag for review instead of guessing
- Any proposed signature that doesn't fit the existing interfaces.
- Provider wire-contract surprises (OpenAI/DeepSeek field differences).
- Any place where prompt-cache prefix stability would be broken by the design.
- Any test that would require a live API key to pass.
