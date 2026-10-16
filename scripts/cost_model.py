#!/usr/bin/env python3
"""Projects the cost of a literary run (NMT draft + LLM pass) for an EPUB.

Serves as a verifiable source for the figures in docs/rfcs/0007-*.md. No external
dependency, no network call: everything is derived from the EPUB, the SQLite
bible and rates passed as parameters.

The key point the model makes visible: each LLM call repeats the stable prefix
(bible + glossary), so the number of calls — not the number of segments — drives
the dominant share of the input cost.

Usage:
    python3 scripts/cost_model.py \
        --epub "books/lumen/en/Lumen Web Novel - Volume 1.epub" \
        --bible data/lumen-series.sqlite --series-id lumen \
        --glossary data/lumen-glossary.json
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import sqlite3
import zipfile
from dataclasses import dataclass
from xml.etree import ElementTree as ET

# Tokenization heuristics. Deliberately coarse and explicit: for an exact figure
# you need the provider's tokenizer (cf. count_tokens on the Anthropic side).
# They reuse charsPerToken=4.0 from internal/cost/estimate.go for English, and a
# lower value for French (accents, elisions).
CHARS_PER_TOKEN_EN = 4.0
CHARS_PER_TOKEN_FR = 3.5
# FR/EN output expansion, aligned with TOMESHIFT_LLM_OUTPUT_EXPANSION.
FR_EXPANSION = 1.15
# Per-segment JSON overhead in the payload and in the response (keys, quotes,
# escapes), measured on the internal/literary schema.
JSON_OVERHEAD_IN_CHARS = 40
JSON_OVERHEAD_OUT_CHARS = 30
# Volatile context window, in runes, aligned with literary.contextRunes.
# It only exists for the literary pass: the HTTP post-editor request carries
# only the segment and its draft, so context_windows=0 for it.
# Before batch 3 of RFC 0007 each call carried two (before and after); since
# then, the chapter source travels in the cacheable prefix and only the
# *previous* window remains — the French translation of the previous batch,
# which ensures the seam between batches and therefore cannot be cached.
CONTEXT_RUNES_EACH = 1200


@dataclass(frozen=True)
class Rates:
    """LLM provider rates, in USD per million tokens."""

    name: str
    input_per_mtok: float
    output_per_mtok: float
    # 0 = the provider does not cache: the prefix is paid at full rate on every
    # call (the case for OpenAI-compatible hosts without a server-side cache).
    cache_read_per_mtok: float = 0.0
    # Cache write premium (1.25x at Anthropic).
    cache_write_multiplier: float = 1.25
    # Tokenizer factor relative to the Sonnet 4.6 / Haiku 4.5 family, from which
    # this file's CHARS_PER_TOKEN_* heuristics are derived. Claude Sonnet 5 and
    # the Opus 4.7+ family use a tokenizer that produces more tokens for the same
    # text: at the same sticker price, the real cost rises by that much. The
    # factor applies only to tokens, never to rates.
    token_multiplier: float = 1.0

    def caches(self) -> bool:
        return self.cache_read_per_mtok > 0


DEFAULT_RATES = [
    Rates("Ollama local", 0.0, 0.0),
    Rates("DeepSeek V4-Flash", 0.14, 0.28, 0.0028),
    Rates("Mistral Small 4", 0.15, 0.60),
    Rates("Claude Haiku 4.5", 1.00, 5.00, 0.10),
    Rates("Claude Sonnet 4.6", 3.00, 15.00, 0.30),
    # Sonnet 5: same sticker price as Sonnet 4.6, but ~30% more tokens for the
    # same text. The introductory rate runs until 2026-08-31.
    Rates("Claude Sonnet 5 (intro)", 2.00, 10.00, 0.20, token_multiplier=1.30),
    Rates("Claude Sonnet 5", 3.00, 15.00, 0.30, token_multiplier=1.30),
    Rates("Claude Opus 5", 5.00, 25.00, 0.50, token_multiplier=1.30),
]


def chapter_char_counts(epub_path: str) -> list[int]:
    """Text length of each XHTML document in the spine, in order.

    Reproduces the filtering of Orchestrator.segmentBookByChapter: spine
    documents in application/xhtml+xml, excluding the navigation document.
    """
    with zipfile.ZipFile(epub_path) as archive:
        opf_name = next(n for n in archive.namelist() if n.endswith(".opf"))
        root = ET.fromstring(archive.read(opf_name))
        manifest = {
            item.get("id"): (item.get("href"), item.get("media-type"), item.get("properties") or "")
            for item in root.iter()
            if item.tag.endswith("item")
        }
        spine = [ref.get("idref") for ref in root.iter() if ref.tag.endswith("itemref")]
        base = opf_name.rsplit("/", 1)[0] + "/" if "/" in opf_name else ""

        counts: list[int] = []
        for idref in spine:
            href, media_type, properties = manifest.get(idref, (None, None, ""))
            if media_type != "application/xhtml+xml" or "nav" in properties:
                continue
            try:
                raw = archive.read(base + href).decode("utf-8", "ignore")
            except KeyError:
                continue
            text = re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", raw)).strip()
            if text:
                counts.append(len(text))
    return counts


def prefix_chars(bible_db: str | None, series_id: str, glossary_path: str | None) -> int:
    """Size of the stable prefix actually composed by literary.stablePrefix.

    Sums the style guide, character voices, address rules and bible summaries,
    plus the glossary terms and the system instruction. Returns a default value
    if no bible is provided.
    """
    # Length of systemInstruction in internal/literary/prompt.go, plus the
    # section headers (GUIDE_DE_STYLE:, GLOSSAIRE:, ...).
    total = 1000

    if bible_db and os.path.exists(bible_db):
        with sqlite3.connect(bible_db) as conn:
            row = conn.execute(
                "SELECT style_guide, characters_json, address_json, summaries_json "
                "FROM bibles WHERE series_id = ?",
                (series_id,),
            ).fetchone()
        if row:
            style_guide, characters, address, summaries = row
            total += len(style_guide or "")
            # The JSON is rendered as prose: we strip ~25% of structural
            # punctuation (braces, quotes, field names).
            total += int(len(characters or "") * 0.75)
            total += int(len(address or "") * 0.65)
            total += int(len(summaries or "") * 0.85)

    if glossary_path and os.path.exists(glossary_path):
        with open(glossary_path, encoding="utf-8") as handle:
            terms = json.load(handle)
        # One "- EN => FR\n" line per locked term.
        total += sum(len(t.get("term_en", "")) + len(t.get("term_fr", "")) + 7 for t in terms if t.get("locked"))

    return total


def segments_per_chapter(chapter_chars: list[int], total_segments: int, total_chars: int) -> list[int]:
    """Distributes total_segments across chapters, proportional to their length.

    total_segments and total_chars come from `tomeshift estimate`, which applies
    the real segmentation. Here we only do the distribution.
    """
    chars_per_segment = total_chars / total_segments
    raw = [max(1, round(count / chars_per_segment)) for count in chapter_chars]
    scale = total_segments / sum(raw)
    return [max(1, round(value * scale)) for value in raw]


def llm_calls(segments: list[int], batch_size: int) -> int:
    """Number of LLM calls: each chapter is chunked independently.

    This is literary.chunk applied per chapter from
    Orchestrator.translateLiteraryChapters. A 37-segment chapter consumes a whole
    call even with batch_size=50 — hence a total higher than the naive
    ceil(total_segments / batch_size).
    """
    return sum(math.ceil(count / batch_size) for count in segments)


@dataclass(frozen=True)
class Tokens:
    calls: int
    # (prefix per call, number of calls) for each chapter. A list, not an
    # average: a long chapter has a bigger prefix AND more calls to repeat it, so
    # multiplying an average chapter by the total number of calls ignores this
    # covariance and underestimates where the cost concentrates.
    prefix_by_chapter: list[tuple[float, int]]
    volatile: float
    output: float

    def prefix_total(self) -> float:
        return sum(prefix * calls for prefix, calls in self.prefix_by_chapter)


def token_profile(
    segments: list[int],
    total_segments: int,
    total_chars: int,
    batch_size: int,
    prefix_tokens: float,
    extra_prefix_tokens: float = 0.0,
    prefix_per_chapter: bool = False,
    context_windows: int = 2,
) -> Tokens:
    """Breaks down the token consumption of a full LLM pass.

    The payload and the output do NOT depend on batch size: each segment is sent
    and re-translated exactly once. Only the prefix and the volatile context are
    repeated per call — that is where batch_size acts.
    """
    calls = llm_calls(segments, batch_size)
    chars_per_segment = total_chars / total_segments

    payload = total_segments * (
        chars_per_segment / CHARS_PER_TOKEN_EN
        + chars_per_segment * FR_EXPANSION / CHARS_PER_TOKEN_FR
        + JSON_OVERHEAD_IN_CHARS / CHARS_PER_TOKEN_EN
    )
    output = total_segments * (
        chars_per_segment * FR_EXPANSION / CHARS_PER_TOKEN_FR
        + JSON_OVERHEAD_OUT_CHARS / CHARS_PER_TOKEN_FR
    )

    # The prefix that carries the chapter source replaces the *next* window, not
    # the previous one: that one carries the French already produced, which
    # changes with every batch and is therefore not cacheable.
    context = calls * context_windows * CONTEXT_RUNES_EACH / CHARS_PER_TOKEN_FR

    if prefix_per_chapter:
        # Each chapter writes its own prefix then re-reads it on each subsequent
        # call. extra_prefix_tokens is then the source of the current chapter.
        per_chapter_source = [count / total_segments * total_chars / CHARS_PER_TOKEN_EN for count in segments]
        prefix_by_chapter = [
            (prefix_tokens + source, math.ceil(count / batch_size))
            for source, count in zip(per_chapter_source, segments)
        ]
    else:
        prefix_by_chapter = [(prefix_tokens + extra_prefix_tokens, calls)]

    return Tokens(
        calls=calls,
        prefix_by_chapter=prefix_by_chapter,
        volatile=payload + context,
        output=output,
    )


def llm_cost(tokens: Tokens, rates: Rates, thinking_output_multiplier: float = 1.0) -> float:
    """USD cost of an LLM pass, accounting for prompt cache and tokenizer.

    thinking_output_multiplier models adaptive thinking: thinking tokens are
    billed at the OUTPUT rate and share the max_tokens cap with the response.
    1.0 = thinking disabled. Any other value is an assumption to measure, not a
    reliable projection.
    """
    factor = rates.token_multiplier

    prefix_cost = 0.0
    for prefix, calls in tokens.prefix_by_chapter:
        per_call = prefix * factor
        if not rates.caches():
            prefix_cost += calls * per_call / 1e6 * rates.input_per_mtok
            continue
        prefix_cost += per_call / 1e6 * rates.input_per_mtok * rates.cache_write_multiplier
        prefix_cost += (calls - 1) * per_call / 1e6 * rates.cache_read_per_mtok
    return (
        prefix_cost
        + tokens.volatile * factor / 1e6 * rates.input_per_mtok
        + tokens.output * factor * thinking_output_multiplier / 1e6 * rates.output_per_mtok
    )


def nmt_cost(total_chars: int, per_mtok_chars_usd: float, free_chars_remaining: int) -> float:
    """NMT draft cost after applying the monthly free quota.

    Reproduces the rule decided in RFC 0007: only the overflow beyond the free
    tier is billed, and the tier is shared between Cloud Translation Basic and
    Advanced, per project.
    """
    billable = max(0, total_chars - free_chars_remaining)
    return billable / 1_000_000 * per_mtok_chars_usd


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--epub", required=True, help="source EPUB to project")
    parser.add_argument("--segments", type=int, required=True, help="total segments (cf. tomeshift estimate)")
    parser.add_argument("--chars", type=int, required=True, help="total source characters (idem)")
    parser.add_argument("--bible", help="narrative bible SQLite")
    parser.add_argument("--series-id", default="", help="series_id in the bible")
    parser.add_argument("--glossary", help="JSON glossary")
    parser.add_argument("--batch-sizes", default="10,50", help="batch sizes to compare")
    parser.add_argument("--google-per-mchars-usd", type=float, default=20.0, help="Cloud Translation rate")
    parser.add_argument("--free-chars", type=int, default=500_000, help="monthly free quota remaining")
    args = parser.parse_args()

    chapter_chars = chapter_char_counts(args.epub)
    segments = segments_per_chapter(chapter_chars, args.segments, args.chars)
    prefix = prefix_chars(args.bible, args.series_id, args.glossary)
    prefix_tokens = prefix / CHARS_PER_TOKEN_FR
    book_tokens = sum(chapter_chars) / CHARS_PER_TOKEN_EN
    median_chapter_tokens = sorted(chapter_chars)[len(chapter_chars) // 2] / CHARS_PER_TOKEN_EN
    # Kept for display only: the prefix cost is computed chapter by chapter
    # (cf. token_profile), not from an average.
    mean_chapter_tokens = sum(chapter_chars) / len(chapter_chars) / CHARS_PER_TOKEN_EN
    batch_sizes = [int(value) for value in args.batch_sizes.split(",")]

    print(f"EPUB                : {os.path.basename(args.epub)}")
    print(f"chapters (spine)    : {len(chapter_chars)}")
    print(f"segments / chars    : {args.segments:,} / {args.chars:,}")
    print(f"stable prefix       : {prefix:,} chars ~ {prefix_tokens:,.0f} tokens")
    print(f"median chapter      : ~{median_chapter_tokens:,.0f} tokens")
    print(f"mean chapter        : ~{mean_chapter_tokens:,.0f} tokens (indicative)")
    print(f"whole book          : ~{book_tokens:,.0f} tokens")
    print()
    print(f"NMT draft (Google, {args.free_chars:,} free chars remaining) : "
          f"{nmt_cost(args.chars, args.google_per_mchars_usd, args.free_chars):.2f} $")
    print()

    variants = [
        ("before batch 3 (2 x 1200 runes)", dict(context_windows=2)),
        ("+ chapter source (book)", dict(prefix_per_chapter=True, context_windows=1)),
        ("+ whole-book source", dict(extra_prefix_tokens=book_tokens, context_windows=1)),
    ]

    for batch_size in batch_sizes:
        calls = llm_calls(segments, batch_size)
        naive = math.ceil(args.segments / batch_size)
        print(f"--- batch = {batch_size} : {calls} LLM calls (naive {naive}, gap due to per-chapter chunking)")
        for label, kwargs in variants:
            tokens = token_profile(segments, args.segments, args.chars, batch_size, prefix_tokens, **kwargs)
            share = 100 * tokens.prefix_total() / (tokens.prefix_total() + tokens.volatile)
            print(f"    {label} — prefix = {share:.0f}% of input")
            for rates in DEFAULT_RATES:
                if not rates.input_per_mtok:
                    continue
                print(f"        {rates.name:28s} {llm_cost(tokens, rates):6.2f} $")
        print()

    # Adaptive-thinking sensitivity. On Sonnet 5, omitting the `thinking` field
    # enables it: thinking tokens are billed as output, so the cost depends on a
    # factor no projection knows. Measure via usage.output_tokens on a real
    # chapter.
    sonnet5 = next(r for r in DEFAULT_RATES if r.name == "Claude Sonnet 5")
    tokens = token_profile(segments, args.segments, args.chars, batch_sizes[-1], prefix_tokens,
                           prefix_per_chapter=True, context_windows=1)
    print(f"--- adaptive-thinking sensitivity ({sonnet5.name}, batch {batch_sizes[-1]})")
    for multiplier, label in ((1.0, "thinking disabled"), (1.5, "+50% output"), (2.0, "+100%"), (3.0, "+200%")):
        print(f"    {label:22s} {llm_cost(tokens, sonnet5, multiplier):6.2f} $")
    print("    (1.0 is the only projectable figure; the others are hypotheses)")
    print()


if __name__ == "__main__":
    main()
