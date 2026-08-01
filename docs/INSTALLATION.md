# Installation & setup

De zéro à un binaire qui tourne. Pour *traduire* un livre une fois installé,
enchaîne sur [`guide-traduction.md`](guide-traduction.md).

---

## 1. Prérequis

| Outil | Pourquoi | Vérifier |
|---|---|---|
| **Go** (stable récent) | compiler/lancer le CLI | `go version` |
| **Java** (≥ 21) | gate `epubcheck` (optionnelle) | `java -version` |
| **just** | lanceur de tâches | `just --version` |
| **ripgrep** (`rg`) | sortie de certaines recettes | `rg --version` |
| **golangci-lint** | lint | `golangci-lint version` |
| **uv** | service COMET-QE (Python) — éval qualité | `uv --version` |
| **Docker** (option) | workflow conteneurisé | `docker --version` |

macOS (Homebrew) :
```bash
brew install go openjdk just ripgrep golangci-lint uv
```

> Sans Java/`epubcheck`, tout marche : la validation EPUB est simplement
> ignorée. Sans `uv`, tout marche sauf l'évaluation COMET-QE.

---

## 2. Récupérer le projet

```bash
git clone <url-du-dépôt> epub-translator
cd epub-translator
```

---

## 3. Configurer l'environnement

```bash
cp .env.example .env.local      # config locale, gitignorée
```

Édite `.env.local`. Variables clés (préfixe `EPUB_TRANSLATOR_`) :

| Variable | Rôle | Exemple |
|---|---|---|
| `NMT_PROVIDER` | `http` (dev) \| `libretranslate` | `http` |
| `NMT_ENDPOINT` | service NMT (**requis**, même en mode littéraire) | `http://localhost:8081/translate` |
| `TM_PATH` | mémoire de traduction (SQLite) | `./data/tm.sqlite` |
| `NARRATIVE_PATH` | bible de série (SQLite) | `./data/lumen-series.sqlite` |
| `LLM_CACHE_PATH` | cache des complétions LLM | `./data/llm-cache.sqlite` |
| `LLM_CHAT_PROVIDER` | `anthropic` \| `openai` \| `deepseek` | `anthropic` |
| `LLM_MODEL` | modèle LLM (mode littéraire) | `claude-sonnet-4-6` |
| `LLM_BATCH_SIZE` | segments par appel LLM littéraire (`0` = défaut interne) | `0` |
| `LLM_API_KEY` | clé du fournisseur LLM | `sk-ant-…` |
| `COMET_ENDPOINT` | service COMET-QE (éval) | `http://localhost:8000/score` |

> 🔐 **Clé API** : ne la mets pas en clair dans le dépôt. `.env.local` est
> gitignoré ; pour plus de sûreté, garde la clé dans un fichier hors dépôt
> (`~/.epub-translator-secrets`) et source-le avant de lancer une commande, ou
> exporte `EPUB_TRANSLATOR_LLM_API_KEY` dans ton shell.

---

## 4. Construire & vérifier

```bash
just preflight          # vérifie go/java/lint/rg + santé NMT (non bloquant)
go build -o ./bin/epub-translator ./cmd/epub-translator
just check              # gofmt -s + golangci-lint + go test ./...
```

Si ton shell a un `GOROOT` cassé (`go: cannot find GOROOT directory`) :
```bash
env -u GOROOT /usr/local/go/bin/go test ./...
```

---

## 5. Lancer un service de traduction (NMT)

Le pipeline a **toujours** besoin d'un endpoint NMT (brouillon, et repli en mode
littéraire). Trois options :

**A. Serveur de dev intégré** (gratuit, zéro install ; qualité jouet) :
```bash
just nmt-dev            # dictionnaire EN→FR sur :8081
```

**B. LibreTranslate via Docker** (vrai NMT local) :
```bash
docker compose --profile real-nmt up -d libretranslate   # 1er lancement: télécharge le modèle
# .env.local :
#   EPUB_TRANSLATOR_NMT_PROVIDER=libretranslate
#   EPUB_TRANSLATOR_NMT_ENDPOINT=http://localhost:5050/translate
```

**C. Tout en Docker** (`app` + `nmt`) — voir [§ Docker](#7-docker) plus bas.

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
- **Mode littéraire (LLM)** : fonctionne via `app` (il lit `.env.local`, donc la
  clé API), mais l'LLM est une **API externe**, pas un service hébergé. Pour
  l'activer en conteneur, adapte la `command:` du service `app`
  (`--mode literary --series … --glossary …`).
- **COMET-QE** : disponible via le profil `eval` (`docker compose --profile eval
  up comet`) ou via `uv` (voir §6) — pas dans le profil par défaut (image lourde).

---

## Et après ?

- Traduire un livre, pas à pas → [`guide-traduction.md`](guide-traduction.md)
- Évaluer + relire ligne par ligne → [`evaluation-qualite.md`](evaluation-qualite.md)
- Améliorer la qualité (bible, glossaire) → [`ameliorer-traduction.md`](ameliorer-traduction.md)
