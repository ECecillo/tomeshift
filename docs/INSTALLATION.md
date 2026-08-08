# Installation & setup

De zéro à un binaire qui tourne. Pour *traduire* un livre une fois installé,
enchaîne sur [`guide-traduction.md`](guide-traduction.md).

---

## 1. Prérequis

La chaîne d'outils est décrite par [`mise.toml`](../mise.toml) : **une seule
commande l'installe**, aux mêmes versions pour tout le monde.

```bash
brew install mise 1password-cli     # les deux seuls prérequis manuels (macOS)
```

| Outil | Pourquoi | Géré par |
|---|---|---|
| **Go** (1.26, aligné sur `go.mod`) | compiler/lancer le CLI | `mise` |
| **Java** (temurin 21) | gate `epubcheck` (optionnelle) | `mise` |
| **just** | lanceur de tâches | `mise` |
| **ripgrep** (`rg`) | sortie de certaines recettes | `mise` |
| **golangci-lint** | lint | `mise` |
| **Python** (3.12) | `scripts/*.py` (`carve`, `compare`, audit doc) | `mise` |
| **uv** | service COMET-QE — éval qualité | `mise` |
| **`op`** (1Password CLI) | injection des secrets au lancement | manuel |
| **`sqlite3`** | `just bible` | fourni par l'OS |
| **Docker** (option) | workflow conteneurisé | manuel |
| **`epubcheck`** (option) | validation EPUB | jar à télécharger |

> Sans Java/`epubcheck`, tout marche : la validation EPUB est simplement
> ignorée. Sans `uv`, tout marche sauf l'évaluation COMET-QE.

---

## 2. Récupérer le projet

```bash
git clone <url-du-dépôt> epub-translator
cd epub-translator
mise trust        # mise n'exécute pas la config d'un dépôt sans accord explicite
mise install      # go, java, just, golangci-lint, rg, python, uv
```

---

## 3. Configurer l'environnement

Deux fichiers, deux rôles — c'est la règle à retenir :

| Fichier | Contenu | Versionné ? |
|---|---|---|
| `.env.local` | configuration **non secrète** (fournisseur, chemins, plafonds, tarifs) | non (gitignoré) |
| `.env.op` | **références** `op://` vers les secrets, aucune valeur | **oui** |

```bash
cp .env.example .env.local      # config locale, gitignorée
```

`mise` charge `.env.local` dès que tu entres dans le répertoire ; son parseur
n'accepte que des lignes `KEY=VALUE`, ce qui applique la règle que
`docs/ARCHITECTURE.md` se contentait d'énoncer.

### Les secrets : 1Password, pas un fichier

Aucune clé API ne doit se trouver dans `.env.local`. Elles vivent dans un coffre
1Password et sont injectées **dans le processus, au lancement de la commande** :

```bash
# 1. Dans l'app 1Password : Réglages → Développeur → « Intégrer avec le CLI »
#    (c'est ce réglage qui déclenche la demande biométrique)
# 2. Vérifier qu'une référence résout :
op read "op://Dev/anthropic-epub-translator/credential"
# 3. Adapter les références de .env.op à ton coffre si besoin.
```

Les recettes qui appellent un fournisseur sont enveloppées dans
`op run --env-file=.env.op --` ; les autres non. Concrètement : `estimate`,
`glossary-suggest` et `eval-ref` ne demandent **jamais** de credential, parce
qu'elles ne passent aucun appel. Le secret n'existe que dans la mémoire du
processus lancé, pour sa durée de vie — ni dans un fichier, ni dans ton shell.

> ⚠️ Ce que ça garantit, exactement : la demande biométrique apparaît quand
> l'app 1Password est **verrouillée**. App déverrouillée, `op` autorise sans
> redemander. La garantie achetée est donc « le secret n'est jamais au repos sur
> le disque », pas « un humain a approuvé ce run précis ». Pour la seconde,
> règle le verrouillage automatique de l'app de façon agressive.

Édite `.env.local`. Variables clés (préfixe `EPUB_TRANSLATOR_`) :

| Variable | Rôle | Exemple |
|---|---|---|
| `NMT_PROVIDER` | `http` (dev) \| `libretranslate` \| `google` | `http` |
| `NMT_ENDPOINT` | service NMT ; requis par `run`/`batch` sauf pour `google`, qui fournit son hôte | `http://localhost:8081/translate` |
| `NMT_API_KEY` | clé API du NMT — **dans `.env.op`, pas ici** | `op://Dev/gcp…` |
| `NMT_FREE_CHARS_PER_MONTH` | quota gratuit mensuel du fournisseur (`500000` pour Google) | `0` |
| `TM_PATH` | mémoire de traduction (SQLite) | `./data/tm.sqlite` |
| `NARRATIVE_PATH` | bible de série (SQLite) | `./data/lumen-series.sqlite` |
| `LLM_CACHE_PATH` | cache des complétions LLM | `./data/llm-cache.sqlite` |
| `LLM_CHAT_PROVIDER` | `anthropic` \| `openai` \| `deepseek` \| `claude-cli` | `anthropic` |
| `LLM_CLI_BIN` | exécutable des fournisseurs CLI (vide = `claude` du `PATH`) | — |
| `LLM_MODEL` | modèle LLM (mode littéraire) | `claude-sonnet-5` |
| `LLM_TOKEN_MULTIPLIER` | densité du tokenizer (`1.30` pour `claude-sonnet-5`) | `1.0` |
| `LLM_CACHE_READ_PER_MTOK_USD` | tarif de lecture du cache de prompt (`0` = pas de cache) | `0.30` |
| `LLM_BATCH_SIZE` | segments par appel LLM littéraire (`0` = défaut interne) | `0` |
| `LLM_API_KEY` | clé du fournisseur LLM — **dans `.env.op`, pas ici** | `op://Dev/anthropic…` |
| `COMET_ENDPOINT` | service COMET-QE (éval) | `http://localhost:8000/score` |

> 🔐 **Les deux `*_API_KEY` n'ont plus leur place dans `.env.local`.** Elles sont
> résolues depuis 1Password au lancement (voir ci-dessus). Si tu tiens à ne pas
> utiliser 1Password, exporte-les dans ton shell avant la commande — mais alors
> elles vivent dans l'environnement de tout ce que tu lances depuis ce dossier,
> ce que le montage `op run` existe précisément pour éviter.

---

## 4. Construire & vérifier

```bash
just preflight          # config, chaîne d'outils (mise), session 1Password, santé NMT
go build -o ./bin/epub-translator ./cmd/epub-translator
just check              # gofmt -s + golangci-lint + go test ./... + audit doc
```

Si ton shell a un `GOROOT` cassé (`go: cannot find GOROOT directory`) :
```bash
env -u GOROOT /usr/local/go/bin/go test ./...
```

---

## 5. Lancer un service de traduction (NMT)

Le pipeline a **toujours** besoin d'un endpoint NMT (brouillon, et repli en mode
littéraire). Quatre options :

**A. Serveur de dev intégré** (gratuit, zéro install ; qualité jouet) :
```bash
just nmt-dev            # dictionnaire EN→FR sur :8081
```

**B. Google Cloud Translation v2** (recommandé — meilleure qualité de brouillon) :
```bash
# .env.local (NMT_ENDPOINT peut rester vide : l'adaptateur connaît l'hôte) :
#   EPUB_TRANSLATOR_NMT_PROVIDER=google
#   EPUB_TRANSLATOR_NMT_API_KEY=<clé API Cloud Translation>
#   EPUB_TRANSLATOR_NMT_FREE_CHARS_PER_MONTH=500000
#   EPUB_TRANSLATOR_COST_PER_1000_CHARS_USD=0.02        # 20 $ / M caractères
```
Le projet GCP doit avoir la facturation activée, **même pour le palier
gratuit**. Ce palier vaut 500 000 caractères par mois, partagé entre Basic et
Advanced, par projet : environ un volume. Au-delà, seul le dépassement est
facturé, et le compteur est le ledger de runs (`nmt_chars`) — s'il a été
consommé hors pipeline ce mois-ci, la projection sous-estime d'autant.

Le texte du livre est envoyé à Google. C'est la même classe d'exposition que
l'appel Anthropic déjà en place, mais Argos était local : le changement est réel.

**C. LibreTranslate via Docker** (vrai NMT local) :
```bash
docker compose --profile real-nmt up -d libretranslate   # 1er lancement: télécharge le modèle
# .env.local :
#   EPUB_TRANSLATOR_NMT_PROVIDER=libretranslate
#   EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:5050/translate
```

**D. Tout en Docker** (`app` + `nmt`) — voir [§ Docker](#7-docker) plus bas.

---

## 6. (Optionnel) Service COMET-QE pour l'évaluation

Reference-free quality estimation. Lancé via `uv` (pins dans
[`requirements-comet.txt`](../requirements-comet.txt)) :

```bash
uv run --python 3.12 \
  --with "unbabel-comet>=2.2" --with "huggingface-hub<1.0" \
  --with "torch<2.3" --with "setuptools<81" \
  --with fastapi --with "uvicorn[standard]" \
  -- python -m uvicorn comet_service:app --host 127.0.0.1 --port 8000
```

Le 1er lancement télécharge le modèle (~2,3 Go). Détails et interprétation :
[`evaluation-qualite.md`](evaluation-qualite.md).

**Ou via Docker** (profil `eval`, image lourde mais isolée ; modèle persté dans
un volume nommé) :
```bash
docker compose --profile eval up -d comet     # 1er boot: build + téléchargement du modèle (long)
docker compose --profile eval logs -f comet    # attendre "Application startup complete"
# puis pointer l'éval sur :  EPUB_TRANSLATOR_COMET_ENDPOINT=http://localhost:8000/score
```

> Deux voies, au choix : `uv` (rapide à itérer, rien à builder) ou Docker
> (`--profile eval`, isolé du système). Le compose par défaut reste dédié à la
> *traduction* ; COMET n'est tiré que si tu actives explicitement le profil `eval`.

---

## 7. Docker

Le `docker-compose.yml` couvre la **traduction** de bout en bout :

| Service | Profil | Rôle |
|---|---|---|
| `app` | (défaut) | exécute le CLI (`run … source.epub` en NMT par défaut) |
| `nmt` | (défaut) | NMT de dev intégré (:8081), autonome |
| `libretranslate` | `real-nmt` | vrai NMT local EN→FR |
| `comet` | `eval` | service COMET-QE (:8000) pour l'évaluation qualité |

```bash
mkdir -p books runs data
cp <ton-livre>.epub books/source.epub
just up         # build + run app (+ nmt) ; sortie dans ./runs
just down       # arrêt + nettoyage
```

**Ce que le compose ne couvre pas** (par conception) :
- **Mode littéraire (LLM)** : fonctionne via `app`, mais l'LLM est une **API
  externe**, pas un service hébergé. Pour l'activer en conteneur, adapte la
  `command:` du service `app` (`--mode literary --series … --glossary …`). La
  clé n'arrive plus par `.env.local` (Compose le lit depuis le disque) mais par
  interpolation depuis l'environnement de Compose — c'est pourquoi `just up`
  est enveloppé dans `op run`.
- **COMET-QE** : disponible via le profil `eval` (`docker compose --profile eval
  up comet`) ou via `uv` (voir §6) — pas dans le profil par défaut (image lourde).

---

## Et après ?

- Traduire un livre, pas à pas → [`guide-traduction.md`](guide-traduction.md)
- Évaluer + relire ligne par ligne → [`evaluation-qualite.md`](evaluation-qualite.md)
- Améliorer la qualité (bible, glossaire) → [`ameliorer-traduction.md`](ameliorer-traduction.md)
