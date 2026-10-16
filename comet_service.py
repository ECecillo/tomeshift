"""Reference-free COMET-QE scoring service for tomeshift.

Exposes a single endpoint matching the contract the Go `eval`/`run` commands
expect:

    POST /score
      { "samples": [ {"src": "...", "mt": "...", "ref": "..."} ] }  # ref ignored (QE)
    -> { "scores": [0.11, ...] }

Quality estimation is *reference-free*: it scores the source against the
machine translation, so it works even when no human reference of the same
edition exists (the common case here — web-novel vs light-novel).

Run it with `uv` (pins a compatible Python + deps; see requirements-comet.txt):

    uv run --python 3.12 \
      --with "unbabel-comet>=2.2" --with "huggingface-hub<1.0" \
      --with "torch<2.3" --with "setuptools<81" \
      --with fastapi --with "uvicorn[standard]" \
      -- python -m uvicorn comet_service:app --host 127.0.0.1 --port 8000

The model `wmt20-comet-qe-da` is non-gated (no HF login needed). It outputs
z-score-like values centered near 0 (negatives are normal) — use it to *rank*
segments and compare systems, not as an absolute 0..1 quality grade.
See docs/evaluation-qualite.md for interpretation.
"""

from comet import download_model, load_from_checkpoint
from fastapi import FastAPI

MODEL_ID = "Unbabel/wmt20-comet-qe-da"  # reference-free QE, non-gated (~2.3 GB on first run)
model = load_from_checkpoint(download_model(MODEL_ID))
app = FastAPI()


@app.post("/score")
async def score(body: dict):
    samples = body.get("samples", [])
    # QE = reference-free: only src + mt are sent to the model.
    data = [{"src": s["src"], "mt": s["mt"]} for s in samples]
    if not data:
        return {"scores": []}
    # num_workers=2 sidesteps a torch DataLoader bug where a multiprocessing
    # context is passed even with num_workers=0 (CPU path, gpus=0).
    out = model.predict(data, batch_size=8, gpus=0, num_workers=2)
    return {"scores": [float(x) for x in out.scores]}
