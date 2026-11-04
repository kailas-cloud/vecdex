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

    if any(len(text) > 32768 for text in input_data):
        return JSONResponse({"error": {"message": "model context exceeded", "type": "invalid_request_error", "code": "context_length_exceeded"}}, status_code=400)
    if any("MOCK_FAIL_FRAGMENT" in text for text in input_data):
        return JSONResponse({"error": {"message": "injected embedding failure", "type": "server_error"}}, status_code=500)
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



# Test-only Bedrock runtime. Production still uses the exact regional AWS URL;
# the isolated Compose network resolves that hostname to this TLS mock.
bedrock_calls = []


@app.get("/_bedrock/calls")
async def recorded_bedrock_calls():
    return {"calls": bedrock_calls[-1024:]}


@app.post("/model/{model_id}/invoke")
async def bedrock_embeddings(model_id: str, request: Request):
    if request.headers.get("authorization") != "Bearer fake-bedrock-key":
        return JSONResponse({"message": "invalid bearer key"}, status_code=403,
                            headers={"X-Amzn-Errortype": "AccessDeniedException"})
    body = await request.json()
    texts = body.get("texts", [])
    purpose = body.get("input_type")
    dimensions = body.get("output_dimension")
    valid = (model_id == "cohere.embed-v4:0" and isinstance(texts, list)
             and 1 <= len(texts) <= 96 and purpose in ("search_document", "search_query")
             and dimensions in (256, 512, 1024, 1536)
             and body.get("max_tokens") == 128000 and body.get("truncate") == "NONE")
    if not valid:
        return JSONResponse({"message": "invalid v4 contract"}, status_code=400,
                            headers={"X-Amzn-Errortype": "ValidationException"})
    bedrock_calls.append({
        "hashes": [hashlib.sha256(text.encode()).hexdigest() for text in texts],
        "lengths": [len(text) for text in texts], "purpose": purpose,
        "dimensions": dimensions, "max_tokens": body["max_tokens"], "truncate": body["truncate"],
    })
    if any(len(text) > 32768 for text in texts):
        return JSONResponse({"message": "input is too long"}, status_code=400,
                            headers={"X-Amzn-Errortype": "ValidationException"})
    if any("MOCK_FAIL_FRAGMENT" in text for text in texts):
        return JSONResponse({"message": "injected embedding failure"}, status_code=500,
                            headers={"X-Amzn-Errortype": "InternalServerException"})
    headers = {"X-Amzn-Bedrock-Input-Token-Count": str(sum(len(text) // 4 for text in texts))}
    if any("MOCK_NO_TOKEN_HEADER" in text for text in texts):
        headers = {}
    if any("MOCK_BAD_TOKEN_HEADER" in text for text in texts):
        headers = {"X-Amzn-Bedrock-Input-Token-Count": "invalid"}
    return JSONResponse({"embeddings": {"float": [stub_embed(text, dimensions) for text in texts]}}, headers=headers)


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=9999)
