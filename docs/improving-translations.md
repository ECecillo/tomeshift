# Improving a translation

Guiding principle: **fix the recipe, not the dish.** Rather than touching up the
translated text (lost on the next run), adjust the *sources of truth* — glossary,
series bible — that guide the LLM at every chapter and get reused from one volume
to the next.

The loop:

```
translate → review (quality-evaluation.md) → classify the deviations
         → adjust glossary / bible → re-run → compare before/after
```

---

## The three levers

### 1. Glossary — for names and terms

A JSON file (`data/<series>-glossary.json`), passed via `--glossary`. Locks the
FR spelling of a name/term everywhere in the book.

```json
[
  {"term_en": "Elena",  "term_fr": "Elena",  "locked": true},
  {"term_en": "Kai",  "term_fr": "Kai",  "locked": true}
]
```

- `term_en` = the **source** spelling to match; `term_fr` = the **target**
  rendering.
- `locked: true` = enforced (not negotiable by the LLM).
- Bootstrap: `glossary-suggest --input vol1.epub --out glossary.json --min-count 5`
  extracts candidate proper nouns → **curate** the file (keep the real names,
  fix `term_fr`, drop the noise).
- Tip: to pin a spelling, compare against the names of a reference FR edition
  (`glossary-suggest` on the FR EPUB, then diff the `term_en`).

### 2. Series bible — for style, characters, address form

SQLite scoped by `--series` (not by volume). Three useful columns:

| Column | Contents | Fixes… |
|---|---|---|
| `style_guide` | tone, register, punctuation, slang, instructions | stylistic drift |
| `characters_json` | characters, voices, epithets | characterization inconsistency |
| `address_json` | tu/vous matrix + evolution | the wrong level of address |

Build (once per published volume, before any translation):
```bash
series-bible --input-dir ./books/<series> --series <series> --samples 8
```
Style, voices and address form are drawn from **all** volumes; the summaries are
one synopsis per volume, bounded to volumes 1..N in the `<series>@volume-NN`
bible.

`style-profile` remains available to seed from a single volume (`--merge` to
enrich instead of replace).

Review the bible **before** a full run (an error there propagates):
```bash
just bible ./data/<series>-series.sqlite <series>
```

### 3. Style guardrails — bounding recurring drift

A "guardrail" = an **instruction added to the `style_guide`**, seen by the LLM at
every chapter. Real examples drawn from the Lumen ch.1 review:

| Observed drift | Guardrail added to the `style_guide` |
|---|---|
| invented filler (« Ouais, mais… hein » where the original has none) | *« Do not add oral fillers/interjections absent from the source; match its level of informality. »* |
| overuse of « hein » | *« Use sentence-final « hein » sparingly — only for an explicit tag question / « huh »/« right ». »* |
| « survêtement » then « survêt' » | *« Keep recurring concrete nouns lexically consistent across the volume. »* |

Applying it (reversible):
```bash
sqlite3 ./data/<series>-series.sqlite \
  "UPDATE bibles SET style_guide = style_guide || ' <your instruction>' WHERE series_id='<series>';"
```

> A guardrail **steers** the model; it does not guarantee a sentence-by-sentence
> rendering. For a precise term, the glossary (locked) is the reliable lever.

---

## Frozen vs evolving, and the cost of a re-run

- **Frozen bible** (default): every chapter sees the same bible → stable
  prompts, reproducible and **free** re-runs (served by the LLM cache).
- **`--update-bible`**: discouraged. The bible evolves chapter by chapter within
  the run, which rewrites the cacheable prefix at every chapter and makes re-runs
  paid and non-reproducible. Build the bible ahead of time with `series-bible`.

**Important:** modifying the `style_guide`, the glossary or the bible **changes
the prompt prefix of *all* segments** → invalidates the cache → the next run
re-translates (and re-pays for) everything. This is expected. On a single chapter
it is negligible (~$0.05–0.10); on a full volume, batch your fixes before
re-running.

Always estimate before paying:
```bash
./bin/tomeshift estimate --input vol1.epub      # 0 API calls
```

---

## Continuity from one volume to the next

The plot memory is built **before** translating, not during. A `series-bible`
reads every chapter of every volume, draws a summary from each, then reduces
those summaries into one synopsis per volume. These synopses feed the
`RESUME_DE_L_HISTOIRE` section of the prefix → the LLM knows "what happened
before".

```bash
# Once, for the whole series
./bin/tomeshift series-bible --input-dir ./books/lumen --series lumen --samples 8

# Volume 1 — bible bounded to volume 1
./bin/tomeshift run --input vol1.epub --output-dir ./runs \
  --mode literary --series lumen@volume-01 --glossary ./data/lumen-glossary.json

# Volume 2 — bible bounded to volumes 1 and 2
./bin/tomeshift run --input vol2.epub --output-dir ./runs \
  --mode literary --series lumen@volume-02 --glossary ./data/lumen-glossary.json
```

> `--update-bible` did this work during the run. It is **discouraged**: the
> bible changes at every chapter, so the cacheable prefix is rewritten 68 times
> per volume ($4.33 vs $0.25–0.41) and every re-run pays for everything again.
> Check the state with `just bible <db> <series>` (summaries column) before
> launching the next volume.

## Typical improvement workflow

1. Translate a **chapter** ([`translation-guide.md`](translation-guide.md)).
2. Review side by side ([`quality-evaluation.md`](quality-evaluation.md)).
3. Classify the deviations: name→glossary, style→`style_guide`,
   address→`address_json`.
4. Apply the changes (JSON glossary + bible `UPDATE`).
5. Re-run the chapter (`--run-id …-v2`) and **compare before/after** with the
   3-column view.
6. Satisfied → launch the **full volume** with the same frozen bible/glossary.

---

## See also

- [`translation-guide.md`](translation-guide.md) — translating step by step
- [`quality-evaluation.md`](quality-evaluation.md) — measuring & reviewing
- [`rfcs/0006-faithful-multivolume-translation.md`](rfcs/0006-faithful-multivolume-translation.md) — the *why* of the bible
