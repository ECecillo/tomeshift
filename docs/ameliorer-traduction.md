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

Construire (une fois par tome publié, avant toute traduction) :
```bash
series-bible --input-dir ./books/<serie> --series <serie> --samples 8
```
Style, voix et adresse sont tirés de **tous** les tomes ; les résumés sont un
synopsis par tome, bornés aux tomes 1..N dans la bible `<serie>@volume-NN`.

`style-profile` reste disponible pour amorcer depuis un seul tome
(`--merge` pour enrichir au lieu de remplacer).

Relire la bible **avant** un run complet (une erreur s'y propage) :
```bash
just bible ./data/<serie>-series.sqlite <serie>
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
- **`--update-bible`** : déconseillé. La bible évolue chapitre par chapitre dans
  le run, ce qui réécrit le préfixe cacheable à chaque chapitre et rend les
  re-runs payants et non reproductibles. Construire la bible en amont avec
  `series-bible`.

**Important :** modifier le `style_guide`, le glossaire ou la bible **change le
préfixe de prompt de *tous* les segments** → invalide le cache → le prochain run
re-traduit (et re-paie) l'intégralité. C'est attendu. Sur un chapitre c'est
négligeable (~0,05–0,10 $) ; sur un volume entier, regroupe tes corrections
avant de relancer.

Toujours estimer avant de payer :
```bash
./bin/tomeshift estimate --input vol1.epub      # 0 appel API
```

---

## Continuité d'un tome à l'autre

La mémoire d'intrigue est construite **avant** de traduire, pas pendant. Un
`series-bible` lit chaque chapitre de chaque tome, en tire un résumé, puis réduit
ces résumés en un synopsis par tome. Ces synopsis alimentent la section
`RESUME_DE_L_HISTOIRE` du préfixe → le LLM sait « ce qui s'est passé avant ».

```bash
# Une fois, pour toute la série
./bin/tomeshift series-bible --input-dir ./books/lumen --series lumen --samples 8

# Tome 1 — bible bornée au tome 1
./bin/tomeshift run --input vol1.epub --output-dir ./runs \
  --mode literary --series lumen@volume-01 --glossary ./data/lumen-glossary.json

# Tome 2 — bible bornée aux tomes 1 et 2
./bin/tomeshift run --input vol2.epub --output-dir ./runs \
  --mode literary --series lumen@volume-02 --glossary ./data/lumen-glossary.json
```

> `--update-bible` faisait ce travail pendant le run. C'est **déconseillé** : la
> bible change à chaque chapitre, donc le préfixe cacheable est réécrit 68 fois
> par tome (4,33 $ contre 0,25–0,41 $) et chaque re-run repaie tout. Vérifie
> l'état avec `just bible <db> <série>`.
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
