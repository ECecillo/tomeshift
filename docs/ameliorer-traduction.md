# Améliorer une traduction

Principe directeur : **on corrige la recette, pas le plat.** Plutôt que de
retoucher le texte traduit (perdu au prochain run), on ajuste les *sources de
vérité* — glossaire, bible de série — qui guident le LLM à chaque chapitre et se
réutilisent d'un tome à l'autre.

La boucle :

```
traduire → relire (evaluation-qualite.md) → classer les écarts
        → ajuster glossaire / bible → relancer → comparer avant/après
```

---

## Les trois leviers

### 1. Glossaire — pour les noms et termes

Fichier JSON (`data/<serie>-glossary.json`), passé via `--glossary`. Verrouille
la graphie FR d'un nom/terme partout dans le livre.

```json
[
  {"term_en": "Elena",  "term_fr": "Elena",  "locked": true},
  {"term_en": "Kai",  "term_fr": "Kai",  "locked": true}
]
```

- `term_en` = graphie de la **source** à matcher ; `term_fr` = rendu **cible**.
- `locked: true` = imposé (non négociable par le LLM).
- Bootstrap : `glossary-suggest --input vol1.epub --out glossary.json --min-count 5`
  extrait les noms propres candidats → **cure** le fichier (garde les vrais
  noms, corrige `term_fr`, retire le bruit).
- Astuce : pour figer une graphie, compare aux noms d'une VF de référence
  (`glossary-suggest` sur l'EPUB FR, puis diff des `term_en`).

### 2. Bible de série — pour le style, les personnages, le tutoiement

SQLite scopée par `--series` (pas par volume). Trois colonnes utiles :

| Colonne | Contenu | Corrige… |
|---|---|---|
| `style_guide` | ton, registre, ponctuation, slang, consignes | la dérive stylistique |
| `characters_json` | personnages, voix, épithètes | l'incohérence de caractérisation |
| `address_json` | matrice tu/vous + évolution | le mauvais niveau d'adresse |

Amorcer / enrichir :
```bash
style-profile --input vol1.epub --series <serie> --samples 8        # amorce (remplace)
style-profile --input vol2.epub --series <serie> --samples 8 --merge # enrichit sans écraser
```
Relire la bible **avant** un run complet (une erreur s'y propage) :
```bash
sqlite3 ./data/<serie>-series.sqlite \
  "SELECT style_guide, characters_json, address_json FROM bibles WHERE series_id='<serie>';"
```

### 3. Garde-fous de style — borner les dérives récurrentes

Un « garde-fou » = une **consigne ajoutée au `style_guide`**, vue par le LLM à
chaque chapitre. Exemples réels tirés de la revue Lumen ch.1 :

| Dérive observée | Garde-fou ajouté au `style_guide` |
|---|---|
| filler inventé (« Ouais, mais… hein » là où la VO ne l'a pas) | *« Do not add oral fillers/interjections absent from the source; match its level of informality. »* |
| sur-emploi de « hein » | *« Use sentence-final « hein » sparingly — only for an explicit tag question / « huh »/« right ». »* |
| « survêtement » puis « survêt' » | *« Keep recurring concrete nouns lexically consistent across the volume. »* |

Application (réversible) :
```bash
sqlite3 ./data/<serie>-series.sqlite \
  "UPDATE bibles SET style_guide = style_guide || ' <ta consigne>' WHERE series_id='<serie>';"
```

> Un garde-fou **oriente** le modèle, il ne garantit pas un rendu phrase par
> phrase. Pour un terme précis, le glossaire (verrouillé) est le levier sûr.

---

## Figée vs évolutive, et le coût d'un re-run

- **Bible figée** (défaut) : tous les chapitres voient la même bible → prompts
  stables, re-runs reproductibles et **gratuits** (servis par le cache LLM).
- **`--update-bible`** : la bible évolue chapitre par chapitre dans le run
  (meilleure cohérence intra-tome, mais re-runs non gratuits).

**Important :** modifier le `style_guide`, le glossaire ou la bible **change le
préfixe de prompt de *tous* les segments** → invalide le cache → le prochain run
re-traduit (et re-paie) l'intégralité. C'est attendu. Sur un chapitre c'est
négligeable (~0,05–0,10 $) ; sur un volume entier, regroupe tes corrections
avant de relancer.

Toujours estimer avant de payer :
```bash
./bin/epub-translator estimate --input vol1.epub      # 0 appel API
```

---

## Continuité d'un tome à l'autre

La bible étant **scopée par `--series`**, le tome 2 hérite automatiquement du
`style_guide`, des `characters` et de la matrice `address` du tome 1. Pour que
l'**intrigue** du tome 1 informe aussi le tome 2, deux choses :

1. **Peupler la mémoire** : traduire le tome 1 avec `--update-bible`. À chaque
   chapitre, un résumé est ajouté à `summaries_json` (et voix/adresse évoluent).
2. **L'utiliser** : ces résumés sont injectés dans le prompt sous la section
   `STORY_SO_FAR` → le LLM sait « ce qui s'est passé avant » en traduisant.

```bash
# Tome 1 — traduire en faisant évoluer la bible (peuple STORY_SO_FAR)
./bin/epub-translator run --input vol1.epub --output-dir ./runs \
  --mode literary --series lumen --update-bible --glossary ./data/lumen-glossary.json

# Tome 2 — hérite de la bible (style + perso + adresse + résumés)
./bin/epub-translator style-profile --input vol2.epub --series lumen --samples 8 --merge
./bin/epub-translator run --input vol2.epub --output-dir ./runs \
  --mode literary --series lumen --glossary ./data/lumen-glossary.json
```

> Sans `--update-bible`, la bible reste **figée** : pas de nouveaux résumés, donc
> `STORY_SO_FAR` ne grandit pas. Vérifie l'état avec `just bible <db> <série>`
> (colonne résumés) avant de lancer le tome suivant.

## Workflow type d'amélioration

1. Traduire un **chapitre** ([`guide-traduction.md`](guide-traduction.md)).
2. Relire en côte-à-côte ([`evaluation-qualite.md`](evaluation-qualite.md)).
3. Classer les écarts : nom→glossaire, style→`style_guide`, adresse→`address_json`.
4. Appliquer les changements (glossaire JSON + `UPDATE` bible).
5. Relancer le chapitre (`--run-id …-v2`) et **comparer avant/après** avec la vue
   3 colonnes.
6. Satisfait → lancer le **volume entier** avec la même bible/glossaire figés.

---

## Voir aussi

- [`guide-traduction.md`](guide-traduction.md) — traduire pas à pas
- [`evaluation-qualite.md`](evaluation-qualite.md) — mesurer & relire
- [`rfcs/0006-faithful-multivolume-translation.md`](rfcs/0006-faithful-multivolume-translation.md) — le *pourquoi* de la bible
