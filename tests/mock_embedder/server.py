"""Mock OpenAI-compatible embedding server.

Ports the StubEmbedder algorithm from internal/testutil/embedder.go:
SHA256-based deterministic vectors with L2 normalization.
"""

import hashlib
import math
import struct
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

DIMENSIONS = 1024


def stub_embed(text: str, dimensions: int = DIMENSIONS) -> list[float]:
    """Port of Go StubEmbedder.Embed — SHA256-based deterministic vectors."""
    seed = hashlib.sha256(text.encode()).digest()
    vec = [0.0] * dimensions

    for i in range(0, dimensions, 8):
        block_input = seed + bytes([i >> 8, i & 0xFF])
        block = hashlib.sha256(block_input).digest()
        for j in range(8):
            if i + j >= dimensions:
                break
            bits = struct.unpack_from("<I", block, j * 4)[0]
            vec[i + j] = bits / 0xFFFFFFFF * 2 - 1

    # L2 normalization
    norm = math.sqrt(sum(v * v for v in vec))
    if norm > 0:
        vec = [v / norm for v in vec]

    return vec


@asynccontextmanager
async def lifespan(app: FastAPI):
    yield


app = FastAPI(lifespan=lifespan)


@app.get("/health")
async def health():
    return {"status": "ok"}


@app.post("/v1/embeddings")
async def embeddings(request: Request):
    body = await request.json()
    model = body.get("model", "stub")
    input_data = body.get("input", [])

    if isinstance(input_data, str):
        input_data = [input_data]

    data = []
    total_tokens = 0
    for idx, text in enumerate(input_data):
        vec = stub_embed(text)
        tokens = len(text) // 4
        total_tokens += tokens
        data.append({
            "object": "embedding",
            "index": idx,
            "embedding": vec,
        })

    return JSONResponse({
        "object": "list",
        "data": data,
        "model": model,
        "usage": {
            "prompt_tokens": total_tokens,
            "total_tokens": total_tokens,
        },
    })


@app.get("/v1/models")
async def list_models():
    """ListModels endpoint used by vecdex health check."""
    return JSONResponse({
        "object": "list",
        "data": [
            {
                "id": "stub",
                "object": "model",
                "created": int(time.time()),
                "owned_by": "mock",
            }
        ],
    })


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=9999)
