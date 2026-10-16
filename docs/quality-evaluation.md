# Evaluating & reviewing a translation

How to measure the quality of a translation and **review it line by line**,
exactly as in the Lumen review session. Three levers:

1. **chrF** — automatic score *with* a reference (identical edition required).
2. **COMET-QE** — automatic score *without* a reference.
3. **Human review** — side-by-side view EN / our FR / reference.

Prerequisite: a completed run in `runs/<run-id>/` (see
[`translation-guide.md`](translation-guide.md)).

---

## 1. chrF (with reference)

Compares two EPUBs by character n-grams. **Only meaningful if the reference is
the same edition** as the translated source (otherwise the score is misleading:
web-novel ≠ light-novel).

```bash
env TOMESHIFT_NMT_ENDPOINT=http://localhost:8081/translate \
  ./bin/tomeshift eval \
    --candidate ./runs/<run-id>/translated.epub \
    --reference ./path/reference-fr.epub
# → chrF = 0.71 (candidate N chars vs reference M chars)
```

Reading it: two *independent* translations of the same text often land around
0.4–0.6; above 0.7 they are very close. It is a **proximity indicator**, not an
absolute grade (different segmentations).

---

## 2. COMET-QE (reference-free)

Reference-free score: it evaluates the source against the translation. The right
metric when you do **not** have an aligned reference.

**a. Launch the COMET service** (once, see also
[`INSTALLATION.md`](INSTALLATION.md#6-optional-comet-qe-service-for-evaluation)):
```bash
uv run --python 3.12 \
  --with "unbabel-comet>=2.2" --with "huggingface-hub<1.0" \
  --with "torch<2.3" --with "setuptools<81" \
  --with fastapi --with "uvicorn[standard]" \
  -- python -m uvicorn comet_service:app --host 127.0.0.1 --port 8000
```
Or via Docker: `docker compose --profile eval up -d comet` (isolated image, model
persisted in a volume; see [`INSTALLATION.md`](INSTALLATION.md#6-optional-comet-qe-service-for-evaluation)).

**b. Score a run**:
```bash
env TOMESHIFT_COMET_ENDPOINT=http://localhost:8000/score \
    TOMESHIFT_NMT_ENDPOINT=http://localhost:8081/translate \
  ./bin/tomeshift eval --segments ./runs/<run-id>/segments.jsonl
# → COMET-QE avg = 0.11 (min -0.49 over 67 segments) ; writes qe-eval-report.json
```

**⚠️ Interpretation.** The non-gated `wmt20-comet-qe-da` model produces
**z-score-like values centered around 0** (negatives are normal), not a 0→1
grade. So:
- the **mean** ≈ 0 = mid-scale, neither good nor bad in absolute terms;
- the metric serves to **rank** segments and **compare** two runs on the same
  source, not to judge in absolute terms;
- the **min** points to the least confident segments → candidates for review.
- known bias: it **under-scores oral register and cultural borrowings**
  (slang dialogue, « konbini »…) — these are not errors. On literary content,
  treat COMET-QE as a **weak signal**: useful for spotting a broken segment
  (lost placeholder, truncated sentence), not for judging style.

---

## 3. Line-by-line human review

The most reliable for literary work. We reduce a run to **one chapter**, then
generate a side-by-side HTML view.

### a. Carve out a chapter (EN source + FR reference)

A run translates the whole volume; to save money, we carve out ch.1. First
locate the chapter's files in the spine, then:

```bash
# EN (often 1 file = 1 chapter)
python scripts/carve_chapter.py "books/serie/vol1-en.epub" books/serie/en-ch1.epub chapter_1.xhtml

# FR reference (here an auto-split EPUB: ch.1 = several files)
python scripts/carve_chapter.py "books/serie/vol1-fr.epub" books/serie/fr-ch1.epub \
  index_split_004 index_split_005 index_split_006 \
  index_split_007 index_split_008 index_split_009
```

To find the right files: `unzip -p vol1.epub '*/content.opf'` (or
`content.opf`), read the order of the `<spine>`, and identify the chapter's
document(s) (titles in the `<h1>`/`<h2>`).

### b. Translate the mini-chapter

```bash
env TOMESHIFT_NARRATIVE_PATH=./data/<serie>-series.sqlite \
  ./bin/tomeshift run \
    --input books/serie/en-ch1.epub --output-dir ./runs --run-id ch1 \
    --source-lang en --target-lang fr \
    --mode literary --series <serie> --glossary ./data/<serie>-glossary.json
```

### c. Generate the side-by-side view

```bash
# 2 columns (EN | our FR), aligned by segment — the reliable view:
python scripts/compare_sidebyside.py runs/ch1/segments.jsonl ch1-2col.html

# 3 columns (EN | our FR | ref), aligned by paragraph:
python scripts/compare_sidebyside.py runs/ch1/segments.jsonl ch1-3col.html \
  --src books/serie/en-ch1.epub --our runs/ch1/translated.epub --keep chapter_1 \
  --ref books/serie/fr-ch1.epub --ref-keep index_split_00
open ch1-3col.html
```

**Reading it**: the EN→our FR columns are aligned **line by line** (we preserve
the structure). The reference column has its **own segmentation** → it **drifts**
downward; read it in blocks, not line by line.

### d. Classify what you find

| Deviation type | Example | Where to fix |
|---|---|---|
| Name / term | spelling of a name, a spell, a place | glossary (`data/<serie>-glossary.json`) |
| Style / register / tone | too formal, added filler, tics | bible `style_guide` |
| Address form (tu/vous) | who addresses whom informally | bible `address_json` |
| Untranslated / broken text | residual English, lost placeholder | bug → fix the code |

We fix the **recipe** (glossary/bible), not the *dish* (the text): see
[`improving-translations.md`](improving-translations.md).

---

## Artifacts of a run (`runs/<run-id>/`)

| File | Contents |
|---|---|
| `translated.epub` | translated EPUB (structure/markup preserved) |
| `report.json` | segments, TM hits, LLM segments, tokens (including cache), costs, QA |
| `segments.jsonl` | 1 line/segment (source, target, placeholders, status) — input to `eval --segments` and the review scripts |
| `metrics.json` | metrics (canonical names, ARCHITECTURE §10) |
| `logs.ndjson` | structured logs |
