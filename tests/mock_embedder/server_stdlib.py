"""Stdlib-only mock OpenAI-compatible embedding server for local E2E runs."""

import hashlib
import json
import math
import struct
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


DIMENSIONS = 1024


def stub_embed(text: str, dimensions: int = DIMENSIONS) -> list[float]:
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

    norm = math.sqrt(sum(v * v for v in vec))
    if norm > 0:
        vec = [v / norm for v in vec]
    return vec


class Handler(BaseHTTPRequestHandler):
    def _send(self, code: int, payload: dict) -> None:
        data = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt: str, *args) -> None:  # noqa: A003
        return

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/health":
            self._send(200, {"status": "ok"})
            return
        if self.path == "/v1/models":
            self._send(200, {"object": "list", "data": [{"id": "stub", "object": "model"}]})
            return
        self._send(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/v1/embeddings":
            self._send(404, {"error": "not found"})
            return

        body_len = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(body_len) or b"{}")
        input_data = body.get("input", [])
        if isinstance(input_data, str):
            input_data = [input_data]

        data = []
        total_tokens = 0
        for idx, text in enumerate(input_data):
            total_tokens += max(1, len(text) // 4)
            data.append({"object": "embedding", "index": idx, "embedding": stub_embed(text)})

        self._send(
            200,
            {
                "object": "list",
                "data": data,
                "model": body.get("model", "stub"),
                "usage": {"prompt_tokens": total_tokens, "total_tokens": total_tokens},
            },
        )


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 9999), Handler).serve_forever()
