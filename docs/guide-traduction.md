# Guide — Traduire un livre EN→FR

Guide pratique de bout en bout : du setup à un volume traduit, avec ou sans LLM.
Pour le *pourquoi* (architecture, choix), voir `docs/ARCHITECTURE.md` et
`docs/rfcs/`. Ici c'est le *comment*.

---

## 0. Les deux modes

| Mode | Moteur | Pour quoi | Coût |
|---|---|---|---|
| **`nmt`** (défaut) | NMT (LibreTranslate / dev) + mémoire de traduction | brouillon rapide, couverture, tests | ~gratuit en local |
| **`literary`** | LLM (Anthropic/OpenAI/DeepSeek) guidé par une **bible de série** + glossaire + contexte | traduction *fidèle à la narration* | payant (API LLM) |

La mémoire de traduction (TM) et le glossaire servent dans les deux modes.

---

## 1. Setup commun

```bash
cp .env.example .env.local        # puis éditer
just preflight                    # vérifie go, java, lint, etc.
```

Le binaire :
```bash
go build -o ./bin/epub-translator ./cmd/epub-translator
# ou: just run <input.epub> <output-dir>
```

Variables d'environnement (préfixe `EPUB_TRANSLATOR_`). Les recettes `just`
sourcent `.env.local`. **Ne mettez jamais une clé API en clair dans le dépôt** :
`.env.local` est gitignoré ; pour plus de sûreté, mettez les clés dans un fichier
hors dépôt et sourcez-le.

| Variable | Rôle | Défaut |
|---|---|---|
| `NMT_PROVIDER` | `http` (dev) \| `libretranslate` \| `google` | `http` |
| `NMT_ENDPOINT` | URL du service NMT ; requis par `run`/`batch` sauf pour `google` | — |
| `NMT_API_KEY` | clé optionnelle du NMT (requise pour `google`) | — |
| `NMT_FREE_CHARS_PER_MONTH` | quota gratuit mensuel (`500000` pour Google) | 0 |
| `TM_PATH` | mémoire de traduction (SQLite) | `./tm.sqlite` |
| `NARRATIVE_PATH` | bible de série (SQLite) | `./narrative.sqlite` |
| `LLM_CACHE_PATH` | cache des complétions LLM (SQLite) | `./llm_cache.sqlite` |
| `PER_BOOK_BUDGET_USD` / `DAILY_BUDGET_USD` / `MONTHLY_BUDGET_USD` | garde-fous de dépense (hard-stop) | 2 / 5 / 50 |
| `RUN_TIMEOUT_SECONDS` | borne un run ; 0 = illimité | 0 |
| `EPUBCHECK_JAR` / `EPUBCHECK_BIN` | gate epubcheck (sinon ignorée) | — |

---

## 2. Le NMT (brouillon)

Toujours nécessaire (même en mode littéraire, pour le brouillon de repli).

**Option A — serveur de dev intégré (gratuit, zéro install)** : dictionnaire
EN→FR, préserve les placeholders. Pour valider la chaîne, pas pour la qualité.
```bash
just nmt-dev      # écoute sur :8081
# .env.local : EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:8081/translate
```

**Option B — Google Cloud Translation v2 (recommandé)** : c'est le maillon
faible de la chaîne qui change ici. Le brouillon Argos était la plus mauvaise
étape du pipeline, et le remplacer coûte 0 $ au rythme d'un volume par mois.
```bash
# .env.local (NMT_ENDPOINT peut rester vide : l'adaptateur connaît l'hôte) :
#   EPUB_TRANSLATOR_NMT_PROVIDER=google
#   EPUB_TRANSLATOR_NMT_API_KEY=<clé API>
#   EPUB_TRANSLATOR_NMT_FREE_CHARS_PER_MONTH=500000
#   EPUB_TRANSLATOR_COST_PER_1000_CHARS_USD=0.02
```
Les segments d'un chapitre partent groupés (jusqu'à 128 textes ou 5000
caractères par requête) : le brouillon d'un volume passe d'environ 14 minutes à
une trentaine de secondes. Le retour en arrière tient en une variable
d'environnement (`NMT_PROVIDER=libretranslate`).

**Option C — LibreTranslate (vrai NMT, local)** :
```bash
docker compose --profile real-nmt up -d libretranslate    # 1er lancement: télécharge le modèle
# .env.local :
#   EPUB_TRANSLATOR_NMT_PROVIDER=libretranslate
#   EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:5050/translate
```

---

## 3. Le LLM (mode littéraire)

Un seul adaptateur OpenAI-compatible couvre **OpenAI** et **DeepSeek** ; Anthropic
a son propre adaptateur. Choix par config :

| Fournisseur | `LLM_CHAT_PROVIDER` | `LLM_BASE_URL` | `LLM_MODEL` (exemple) |
|---|---|---|---|
| Anthropic | `anthropic` | *(vide → api.anthropic.com)* | `claude-sonnet-5` |
| OpenAI | `openai` | `https://api.openai.com/v1` | `gpt-5.4` |
| DeepSeek | `deepseek` | `https://api.deepseek.com` | `deepseek-chat` |

```bash
# .env.local (exemple Anthropic) :
EPUB_TRANSLATOR_LLM_CHAT_PROVIDER=anthropic
EPUB_TRANSLATOR_LLM_MODEL=claude-sonnet-5
EPUB_TRANSLATOR_LLM_API_KEY=sk-ant-...

# Tarifs, pour que le garde-fou budgétaire ait quelque chose à comparer.
# Tarif standard Sonnet 5 (le tarif d'introduction 2 $/10 $ court jusqu'au
# 31/08/2026 ; calibrez le plafond sur le tarif d'après, sinon il se mettra à
# refuser des runs un mois plus tard sans raison visible).
EPUB_TRANSLATOR_LLM_INPUT_PER_MTOK_USD=3.00
EPUB_TRANSLATOR_LLM_OUTPUT_PER_MTOK_USD=15.00
EPUB_TRANSLATOR_LLM_CACHE_READ_PER_MTOK_USD=0.30
# Sonnet 5 utilise le tokenizer de la famille Opus 4.7 : ~30 % de tokens en plus
# pour le même texte, à tarif affiché identique. Sans ce facteur la projection
# sous-estime d'autant.
EPUB_TRANSLATOR_LLM_TOKEN_MULTIPLIER=1.30
```

> La réflexion adaptative est **désactivée explicitement** dans l'adaptateur
> Anthropic. L'omettre l'activerait sur Sonnet 5 : les tokens de réflexion sont
> facturés au tarif de sortie et partagent le plafond `max_tokens` avec la
> réponse JSON, donc une réponse tronquée serait absorbée en silence — et
> facturée — par le rattrapage en demi-lots.

> ⚠️ Données : le texte source + la bible sont envoyés au fournisseur. Respectez
> sa politique de rétention ; pour du contenu sensible, évitez les hébergeurs
> hors juridiction.

---

## 4. Traduire un livre en mode littéraire (recette complète)

```bash
SERIES=lumen
EN="books/.../Volume 01.epub"

# (a) Amorcer la bible de série (1 appel LLM, ~0,10 $). Plus d'échantillons = bible plus complète.
epub-translator style-profile --input "$EN" --series $SERIES --samples 8

# (b) Relire la bible (à faire AVANT un run complet — une erreur se propage)
sqlite3 ./narrative.sqlite \
  "SELECT style_guide, characters_json, address_json FROM bibles WHERE series_id='$SERIES';"

# (c) Glossaire des noms propres (gratuit, heuristique) → squelette JSON à curer
epub-translator glossary-suggest --input "$EN" --out ./glossary.json --min-count 5
#   éditer ./glossary.json : garder les vrais noms, term_fr correct, retirer le bruit

# (d) Estimer le coût AVANT de payer (dry-run, zéro appel API)
#   Même modèle de coût que le garde-fou du run : ce que `estimate` annonce est
#   ce que le plafond par livre applique. Renseigner les tarifs LLM ci-dessus,
#   sinon la projection vaut 0 $ et le plafond ne compare rien.
epub-translator estimate --input "$EN"

# (e) Traduire (bible figée → prompts stables + re-runs gratuits via cache)
epub-translator run --input "$EN" --output-dir ./runs \
  --source-lang en --target-lang fr \
  --mode literary --series $SERIES --glossary ./glossary.json
```

Le résultat est dans `./runs/<run-id>/translated.epub` + artefacts (voir §8).

---

## 5. Une série de plusieurs tomes

La bible est **scopée par `--series`**, pas par volume.

```bash
# Tome 2 : réutiliser la MÊME série → hérite de la bible du tome 1
epub-translator run --input vol2.epub --output-dir ./runs \
  --mode literary --series lumen --glossary ./glossary.json

# Enrichir la bible avec le nouveau matériel du tome 2 SANS écraser le tome 1 :
epub-translator style-profile --input vol2.epub --series lumen --samples 8 --merge
# (sans --merge, style-profile REMPLACE la bible)

# Compléter le glossaire avec les nouveaux noms du tome 2 :
epub-translator glossary-suggest --input vol2.epub --out glossary-vol2.json
```

**Bible figée vs évolutive :**
- défaut **figée** : tous les chapitres voient la même bible → prompts stables,
  re-runs reproductibles/gratuits (cache), idéal qualité homogène + coût.
- `--update-bible` : la bible évolue chapitre par chapitre dans le run (meilleure
  cohérence intra-tome, mais re-runs non gratuits).

---

## 6. Coût & caches

- **`estimate`** : projection pire-cas (sans réutilisation) avant tout appel. 0 $.
- **Garde-fous** : `PER_BOOK_/DAILY_/MONTHLY_BUDGET_USD` → hard-stop avant de payer
  si la projection dépasse le cap (exit code 7).
- **Cache SQLite LLM** (`LLM_CACHE_PATH`) : une requête identique = réponse
  réutilisée → **re-run gratuit et instantané** (tant que la bible est figée).
- **Prompt cache Anthropic** : le préfixe stable (bible+style+glossaire) est mis
  en cache (`cache_control`) → chapitres 2…N à ~0,1× l'input. S'active quand le
  préfixe dépasse ~2048 tokens (bible seedée + glossaire).
- **Mémoire de traduction (TM)** : phrases identiques réutilisées entre runs/tomes.

---

## 7. Évaluer la qualité

- **chrF (avec référence)** — nécessite une **vraie référence FR de la même
  édition** (attention : web-novel ≠ light-novel donnent des scores trompeurs) :
  ```bash
  epub-translator eval --candidate ./runs/<id>/translated.epub --reference ref-fr.epub
  ```
- **COMET-QE (sans référence)** — la bonne métrique quand on n'a pas de référence
  alignée. Nécessite un service COMET (CometKiwi via FastAPI — lancer avec `uv`,
  Python 3.12, `huggingface-hub<1.0` ; recette dans le README) :
  ```bash
  EPUB_TRANSLATOR_COMET_ENDPOINT=http://localhost:8000/score \
    epub-translator eval --segments ./runs/<id>/segments.jsonl
  # → COMET-QE avg = 0.xx ; écrit qe-eval-report.json
  ```
  En mode `run`, COMET-QE peut aussi scorer chaque brouillon et escalader les plus
  faibles vers le LLM (`COMET_ENDPOINT` + `ESCALATE_BELOW_SCORE`).

---

## 8. Artefacts d'un run (`./runs/<run-id>/`)

| Fichier | Contenu |
|---|---|
| `translated.epub` | l'EPUB traduit (structure/markup préservés) |
| `report.json` | résumé : segments, TM hits, segments LLM, **tokens** (dont `llm_cache_read_tokens`), coûts estimé/réel, QA |
| `segments.jsonl` | une ligne par segment (source, cible, hash, placeholders, statut) — entrée de `eval --segments` |
| `metrics.json` | métriques (noms canoniques ARCHITECTURE §10) |
| `logs.ndjson` | logs structurés du run |

---

## 9. Traiter plusieurs livres d'un coup

```bash
epub-translator batch --input-dir ./books/serie --output-dir ./runs \
  --glossary ./glossary.json [--reference-dir ./refs]
# TM + glossaire partagés entre les livres ; rapport agrégé (+ chrF si --reference-dir)
```

---

## 10. Aide-mémoire des commandes

| Commande | Rôle |
|---|---|
| `run --mode nmt\|literary [--series] [--glossary] [--update-bible] [--resume]` | traduire un livre |
| `style-profile --series <id> [--samples N] [--merge]` | amorcer/enrichir la bible |
| `glossary-suggest [--out f] [--min-count N]` | extraire les noms propres |
| `estimate` | projeter le coût (0 appel) |
| `eval --candidate/--reference` \| `--segments` | chrF \| COMET-QE |
| `batch --input-dir [--reference-dir]` | traiter un dossier de livres |

> Toutes les commandes prennent `--input <epub>` et `--source-lang en
> --target-lang fr` (V1). Les chemins de DB et le fournisseur LLM viennent de
> l'environnement (`.env.local`).

**Raccourcis `just`** — chaque étape a une recette qui source `.env.local` pour
toi. `just --list` affiche tout avec ses arguments documentés. Exemples :
```bash
just nmt-dev                                            # service NMT de dev
just comet-up                                           # service COMET (Docker)
just seed-bible "books/vol1.epub" lumen 8              # amorcer la bible
just literary "books/vol1.epub" lumen ./data/lumen-glossary.json wn-ch1
just eval-ref runs/wn-ch1/translated.epub ref-fr.epub   # chrF
just eval-qe  runs/wn-ch1/segments.jsonl                # COMET-QE
just carve "books/vol1.epub" en-ch1.epub chapter_1.xhtml
just compare runs/wn-ch1/segments.jsonl wn-ch1.html
```
