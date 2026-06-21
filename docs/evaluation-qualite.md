# Évaluer & relire une traduction

Comment mesurer la qualité d'une traduction et la **relire ligne par ligne**,
exactement comme dans la session de revue Lumen. Trois leviers :

1. **chrF** — score automatique *avec* référence (édition identique requise).
2. **COMET-QE** — score automatique *sans* référence.
3. **Revue humaine** — vue côte-à-côte EN / notre FR / réf.

Prérequis : un run terminé dans `runs/<run-id>/` (voir
[`guide-traduction.md`](guide-traduction.md)).

---

## 1. chrF (avec référence)

Compare deux EPUB caractère-n-grammes. **N'a de sens que si la référence est la
même édition** que la source traduite (sinon le score est trompeur :
web-novel ≠ light-novel).

```bash
env EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:8081/translate \
  ./bin/epub-translator eval \
    --candidate ./runs/<run-id>/translated.epub \
    --reference ./chemin/reference-fr.epub
# → chrF = 0.71 (candidate N chars vs reference M chars)
```

Lecture : deux traductions *indépendantes* d'un même texte tournent souvent
autour de 0,4–0,6 ; au-delà de 0,7 elles sont très proches. C'est un
**indicateur de proximité**, pas une note absolue (segmentations différentes).

---

## 2. COMET-QE (sans référence)

Score reference-free : il évalue la source contre la traduction. La bonne
métrique quand on n'a **pas** de référence alignée.

**a. Lancer le service COMET** (une fois, voir aussi
[`INSTALLATION.md`](INSTALLATION.md#6-optionnel-service-comet-qe-pour-lévaluation)) :
```bash
uv run --python 3.12 \
  --with "unbabel-comet>=2.2" --with "huggingface-hub<1.0" \
  --with "torch<2.3" --with "setuptools<81" \
  --with fastapi --with "uvicorn[standard]" \
  -- python -m uvicorn comet_service:app --host 127.0.0.1 --port 8000
```
Ou via Docker : `docker compose --profile eval up -d comet` (image isolée, modèle
persté dans un volume ; voir [`INSTALLATION.md`](INSTALLATION.md#6-optionnel-service-comet-qe-pour-lévaluation)).

**b. Scorer un run** :
```bash
env EPUB_TRANSLATOR_COMET_ENDPOINT=http://localhost:8000/score \
    EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:8081/translate \
  ./bin/epub-translator eval --segments ./runs/<run-id>/segments.jsonl
# → COMET-QE avg = 0.11 (min -0.49 over 67 segments) ; écrit qe-eval-report.json
```

**⚠️ Interprétation.** Le modèle non-gated `wmt20-comet-qe-da` produit des
valeurs **de type z-score centrées autour de 0** (négatifs normaux), pas une
note 0→1. Donc :
- la **moyenne** ≈ 0 = milieu d'échelle, ni bon ni mauvais dans l'absolu ;
- la métrique sert à **classer** les segments et **comparer** deux runs sur la
  même source, pas à juger dans l'absolu ;
- le **min** pointe les segments les moins sûrs → candidats à relire.
- biais connu : il **sous-note le registre oral et les emprunts culturels**
  (dialogue argotique, « konbini »…) — ce ne sont pas des erreurs. Sur du
  littéraire, traite COMET-QE comme un **signal faible** : utile pour repérer un
  segment cassé (placeholder perdu, phrase tronquée), pas pour juger le style.

---

## 3. Revue humaine ligne par ligne

Le plus fiable pour le littéraire. On réduit un run à **un chapitre**, puis on
génère une vue HTML côte-à-côte.

### a. Découper un chapitre (source EN + référence FR)

Un run traduit tout le volume ; pour économiser, on découpe le ch.1. Repère
d'abord les fichiers du chapitre dans le spine, puis :

```bash
# EN (souvent 1 fichier = 1 chapitre)
python scripts/carve_chapter.py "books/serie/vol1-en.epub" books/serie/en-ch1.epub chapter_1.xhtml

# Référence FR (ici un EPUB auto-splitté : ch.1 = plusieurs fichiers)
python scripts/carve_chapter.py "books/serie/vol1-fr.epub" books/serie/fr-ch1.epub \
  index_split_004 index_split_005 index_split_006 \
  index_split_007 index_split_008 index_split_009
```

Pour trouver les bons fichiers : `unzip -p vol1.epub '*/content.opf'` (ou
`content.opf`), lire l'ordre du `<spine>`, et identifier le(s) document(s) du
chapitre (titres dans les `<h1>`/`<h2>`).

### b. Traduire le mini-chapitre

```bash
env EPUB_TRANSLATOR_NARRATIVE_PATH=./data/<serie>-series.sqlite \
  ./bin/epub-translator run \
    --input books/serie/en-ch1.epub --output-dir ./runs --run-id ch1 \
    --source-lang en --target-lang fr \
    --mode literary --series <serie> --glossary ./data/<serie>-glossary.json
```

### c. Générer la vue côte-à-côte

```bash
# 2 colonnes (EN | notre FR), alignées par segment — vue fiable :
python scripts/compare_sidebyside.py runs/ch1/segments.jsonl ch1-2col.html

# 3 colonnes (EN | notre FR | réf), alignées par paragraphe :
python scripts/compare_sidebyside.py runs/ch1/segments.jsonl ch1-3col.html \
  --src books/serie/en-ch1.epub --our runs/ch1/translated.epub --keep chapter_1 \
  --ref books/serie/fr-ch1.epub --ref-keep index_split_00
open ch1-3col.html
```

**Lecture** : les colonnes EN→notre FR sont alignées **ligne à ligne** (on
préserve la structure). La colonne réf a sa **propre segmentation** → elle
**dérive** vers le bas ; lis-la par blocs, pas ligne à ligne.

### d. Classer ce que tu trouves

| Type d'écart | Exemple | Où corriger |
|---|---|---|
| Nom / terme | graphie d'un nom, d'un sort, d'un lieu | glossaire (`data/<serie>-glossary.json`) |
| Style / registre / ton | trop soutenu, filler ajouté, tics | `style_guide` de la bible |
| Tutoiement / vouvoiement | qui tutoie qui | `address_json` de la bible |
| Texte non traduit / cassé | anglais résiduel, placeholder perdu | bug → corriger le code |

On corrige la **recette** (glossaire/bible), pas le *plat* (le texte) : voir
[`ameliorer-traduction.md`](ameliorer-traduction.md).

---

## Artefacts d'un run (`runs/<run-id>/`)

| Fichier | Contenu |
|---|---|
| `translated.epub` | EPUB traduit (structure/markup préservés) |
| `report.json` | segments, TM hits, segments LLM, tokens (dont cache), coûts, QA |
| `segments.jsonl` | 1 ligne/segment (source, cible, placeholders, statut) — entrée de `eval --segments` et des scripts de revue |
| `metrics.json` | métriques (noms canoniques ARCHITECTURE §10) |
| `logs.ndjson` | logs structurés |
