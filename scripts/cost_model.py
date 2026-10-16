#!/usr/bin/env python3
"""Projette le coût d'un run littéraire (brouillon NMT + passe LLM) pour un EPUB.

Sert de source vérifiable aux chiffres de docs/rfcs/0007-*.md. Aucune dépendance
externe, aucun appel réseau : tout est dérivé de l'EPUB, de la bible SQLite et de
tarifs passés en paramètres.

Le point clé que le modèle rend visible : chaque appel LLM répète le préfixe
stable (bible + glossaire), donc le nombre d'appels — et non le nombre de
segments — pilote la part dominante du coût d'entrée.

Usage :
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

# Heuristiques de tokenisation. Volontairement grossières et explicites : pour un
# chiffre exact il faut le tokenizer du fournisseur (cf. count_tokens côté
# Anthropic). Elles reprennent charsPerToken=4.0 de internal/cost/estimate.go
# pour l'anglais, et une valeur plus basse pour le français (accents, élisions).
CHARS_PER_TOKEN_EN = 4.0
CHARS_PER_TOKEN_FR = 3.5
# Expansion FR/EN en sortie, alignée sur TOMESHIFT_LLM_OUTPUT_EXPANSION.
FR_EXPANSION = 1.15
# Surcoût JSON par segment dans la charge utile et dans la réponse (clés,
# guillemets, échappements), mesuré sur le schéma de internal/literary.
JSON_OVERHEAD_IN_CHARS = 40
JSON_OVERHEAD_OUT_CHARS = 30
# Fenêtre de contexte volatil, en runes, alignée sur literary.contextRunes.
# Elle n'existe que pour la passe littéraire : la requête du post-éditeur HTTP
# ne porte que le segment et son brouillon, donc context_windows=0 pour elle.
# Avant le lot 3 de la RFC 0007 chaque appel en portait deux (avant et après) ;
# depuis, la source du chapitre voyage dans le préfixe cachable et seule la
# fenêtre *précédente* subsiste — la traduction française du lot précédent, qui
# assure la couture entre lots et ne peut donc pas être mise en cache.
CONTEXT_RUNES_EACH = 1200


@dataclass(frozen=True)
class Rates:
    """Tarifs d'un fournisseur LLM, en USD par million de tokens."""

    name: str
    input_per_mtok: float
    output_per_mtok: float
    # 0 = le fournisseur ne cache pas : le préfixe est payé plein tarif à chaque
    # appel (cas des hôtes OpenAI-compatibles sans cache serveur).
    cache_read_per_mtok: float = 0.0
    # Prime d'écriture du cache (1.25x chez Anthropic).
    cache_write_multiplier: float = 1.25
    # Facteur de tokenisation relatif à la famille Sonnet 4.6 / Haiku 4.5, dont
    # les heuristiques CHARS_PER_TOKEN_* de ce fichier sont dérivées. Claude
    # Sonnet 5 et la famille Opus 4.7+ utilisent un tokenizer qui produit
    # davantage de tokens pour le même texte : à tarif affiché identique, le coût
    # réel augmente d'autant. Le facteur ne s'applique qu'aux tokens, jamais aux
    # tarifs.
    token_multiplier: float = 1.0

    def caches(self) -> bool:
        return self.cache_read_per_mtok > 0


DEFAULT_RATES = [
    Rates("Ollama local", 0.0, 0.0),
    Rates("DeepSeek V4-Flash", 0.14, 0.28, 0.0028),
    Rates("Mistral Small 4", 0.15, 0.60),
    Rates("Claude Haiku 4.5", 1.00, 5.00, 0.10),
    Rates("Claude Sonnet 4.6", 3.00, 15.00, 0.30),
    # Sonnet 5 : tarif affiché identique à Sonnet 4.6, mais ~30 % de tokens en
    # plus pour le même texte. Le tarif d'introduction court jusqu'au 31/08/2026.
    Rates("Claude Sonnet 5 (intro)", 2.00, 10.00, 0.20, token_multiplier=1.30),
    Rates("Claude Sonnet 5", 3.00, 15.00, 0.30, token_multiplier=1.30),
    Rates("Claude Opus 5", 5.00, 25.00, 0.50, token_multiplier=1.30),
]


def chapter_char_counts(epub_path: str) -> list[int]:
    """Longueur du texte de chaque document XHTML du spine, dans l'ordre.

    Reproduit le filtrage de Orchestrator.segmentBookByChapter : documents du
    spine en application/xhtml+xml, hors document de navigation.
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
    """Taille du préfixe stable réellement composé par literary.stablePrefix.

    Somme le guide de style, les voix de personnages, les règles d'adresse et les
    résumés de la bible, plus les termes de glossaire et l'instruction système.
    Retourne une valeur par défaut si aucune bible n'est fournie.
    """
    # Longueur de systemInstruction dans internal/literary/prompt.go, plus les
    # en-têtes de section (GUIDE_DE_STYLE:, GLOSSAIRE:, ...).
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
            # Les JSON sont rendus en prose : on retire ~25 % de ponctuation de
            # structure (accolades, guillemets, noms de champs).
            total += int(len(characters or "") * 0.75)
            total += int(len(address or "") * 0.65)
            total += int(len(summaries or "") * 0.85)

    if glossary_path and os.path.exists(glossary_path):
        with open(glossary_path, encoding="utf-8") as handle:
            terms = json.load(handle)
        # Une ligne "- EN => FR\n" par terme verrouillé.
        total += sum(len(t.get("term_en", "")) + len(t.get("term_fr", "")) + 7 for t in terms if t.get("locked"))

    return total


def segments_per_chapter(chapter_chars: list[int], total_segments: int, total_chars: int) -> list[int]:
    """Répartit total_segments sur les chapitres, au prorata de leur longueur.

    total_segments et total_chars viennent de `tomeshift estimate`, qui
    applique la vraie segmentation. On ne fait ici que la distribution.
    """
    chars_per_segment = total_chars / total_segments
    raw = [max(1, round(count / chars_per_segment)) for count in chapter_chars]
    scale = total_segments / sum(raw)
    return [max(1, round(value * scale)) for value in raw]


def llm_calls(segments: list[int], batch_size: int) -> int:
    """Nombre d'appels LLM : chaque chapitre est découpé indépendamment.

    C'est literary.chunk appliqué par chapitre depuis
    Orchestrator.translateLiteraryChapters. Un chapitre de 37 segments consomme
    un appel entier même avec batch_size=50 — d'où un total supérieur au naïf
    ceil(total_segments / batch_size).
    """
    return sum(math.ceil(count / batch_size) for count in segments)


@dataclass(frozen=True)
class Tokens:
    calls: int
    # (prefixe par appel, nombre d'appels) pour chaque chapitre. Une liste, pas
    # une moyenne : un chapitre long a un préfixe plus gros ET plus d'appels
    # pour le répéter, donc multiplier un chapitre moyen par le total des
    # appels ignore cette covariance et sous-estime là où le coût se concentre.
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
    """Décompose la consommation de tokens d'une passe LLM complète.

    La charge utile et la sortie ne dépendent PAS de la taille de lot : chaque
    segment est envoyé et retraduit exactement une fois. Seuls le préfixe et le
    contexte volatil sont répétés par appel — c'est là qu'agit batch_size.
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

    # Le préfixe qui porte la source du chapitre remplace la fenêtre *suivante*,
    # pas la précédente : celle-ci transporte le français déjà produit, qui
    # change à chaque lot et n'est donc pas cachable.
    context = calls * context_windows * CONTEXT_RUNES_EACH / CHARS_PER_TOKEN_FR

    if prefix_per_chapter:
        # Chaque chapitre écrit son propre préfixe puis le relit à chaque appel
        # suivant. extra_prefix_tokens vaut alors la source du chapitre courant.
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
    """Coût USD d'une passe LLM, cache de prompt et tokenizer pris en compte.

    thinking_output_multiplier modélise la réflexion adaptative : les tokens de
    réflexion sont facturés au tarif de SORTIE et partagent le plafond max_tokens
    avec la réponse. 1.0 = réflexion désactivée. Toute autre valeur est une
    hypothèse à mesurer, pas une projection fiable.
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
    """Coût du brouillon NMT après application du quota gratuit mensuel.

    Reproduit la règle décidée dans la RFC 0007 : seul le dépassement du palier
    gratuit est facturé, et le palier est partagé entre Cloud Translation Basic
    et Advanced, par projet.
    """
    billable = max(0, total_chars - free_chars_remaining)
    return billable / 1_000_000 * per_mtok_chars_usd


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--epub", required=True, help="EPUB source à projeter")
    parser.add_argument("--segments", type=int, required=True, help="total de segments (cf. tomeshift estimate)")
    parser.add_argument("--chars", type=int, required=True, help="total de caractères source (idem)")
    parser.add_argument("--bible", help="SQLite de la bible narrative")
    parser.add_argument("--series-id", default="", help="series_id dans la bible")
    parser.add_argument("--glossary", help="glossaire JSON")
    parser.add_argument("--batch-sizes", default="10,50", help="tailles de lot à comparer")
    parser.add_argument("--google-per-mchars-usd", type=float, default=20.0, help="tarif Cloud Translation")
    parser.add_argument("--free-chars", type=int, default=500_000, help="quota gratuit mensuel restant")
    args = parser.parse_args()

    chapter_chars = chapter_char_counts(args.epub)
    segments = segments_per_chapter(chapter_chars, args.segments, args.chars)
    prefix = prefix_chars(args.bible, args.series_id, args.glossary)
    prefix_tokens = prefix / CHARS_PER_TOKEN_FR
    book_tokens = sum(chapter_chars) / CHARS_PER_TOKEN_EN
    median_chapter_tokens = sorted(chapter_chars)[len(chapter_chars) // 2] / CHARS_PER_TOKEN_EN
    # Conservé pour l'affichage seulement : le coût du préfixe est calculé
    # chapitre par chapitre (cf. token_profile), pas depuis une moyenne.
    mean_chapter_tokens = sum(chapter_chars) / len(chapter_chars) / CHARS_PER_TOKEN_EN
    batch_sizes = [int(value) for value in args.batch_sizes.split(",")]

    print(f"EPUB                : {os.path.basename(args.epub)}")
    print(f"chapitres (spine)   : {len(chapter_chars)}")
    print(f"segments / car.     : {args.segments:,} / {args.chars:,}")
    print(f"prefixe stable      : {prefix:,} car. ~ {prefix_tokens:,.0f} tokens")
    print(f"chapitre median     : ~{median_chapter_tokens:,.0f} tokens")
    print(f"chapitre moyen      : ~{mean_chapter_tokens:,.0f} tokens (indicatif)")
    print(f"livre entier        : ~{book_tokens:,.0f} tokens")
    print()
    print(f"brouillon NMT (Google, {args.free_chars:,} car. gratuits restants) : "
          f"{nmt_cost(args.chars, args.google_per_mchars_usd, args.free_chars):.2f} $")
    print()

    variants = [
        ("avant lot 3 (2 x 1200 runes)", dict(context_windows=2)),
        ("+ source du chapitre (livre)", dict(prefix_per_chapter=True, context_windows=1)),
        ("+ source du livre entier", dict(extra_prefix_tokens=book_tokens, context_windows=1)),
    ]

    for batch_size in batch_sizes:
        calls = llm_calls(segments, batch_size)
        naive = math.ceil(args.segments / batch_size)
        print(f"--- lot = {batch_size} : {calls} appels LLM (naif {naive}, ecart du a un chunking par chapitre)")
        for label, kwargs in variants:
            tokens = token_profile(segments, args.segments, args.chars, batch_size, prefix_tokens, **kwargs)
            share = 100 * tokens.prefix_total() / (tokens.prefix_total() + tokens.volatile)
            print(f"    {label} — prefixe = {share:.0f}% de l'input")
            for rates in DEFAULT_RATES:
                if not rates.input_per_mtok:
                    continue
                print(f"        {rates.name:28s} {llm_cost(tokens, rates):6.2f} $")
        print()

    # Sensibilité à la réflexion adaptative. Sur Sonnet 5, omettre le champ
    # `thinking` l'active : les tokens de réflexion sont facturés en sortie, donc
    # le coût dépend d'un facteur qu'aucune projection ne connaît. À mesurer via
    # usage.output_tokens sur un chapitre réel.
    sonnet5 = next(r for r in DEFAULT_RATES if r.name == "Claude Sonnet 5")
    tokens = token_profile(segments, args.segments, args.chars, batch_sizes[-1], prefix_tokens,
                           prefix_per_chapter=True, context_windows=1)
    print(f"--- sensibilite a la reflexion adaptative ({sonnet5.name}, lot {batch_sizes[-1]})")
    for multiplier, label in ((1.0, "thinking desactive"), (1.5, "+50% de sortie"), (2.0, "+100%"), (3.0, "+200%")):
        print(f"    {label:22s} {llm_cost(tokens, sonnet5, multiplier):6.2f} $")
    print("    (1.0 est le seul chiffre projetable ; les autres sont des hypotheses)")
    print()


if __name__ == "__main__":
    main()
