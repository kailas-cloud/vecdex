"""Wire-level MCP assertions, without REST emulation or implicit mutations."""
import itertools
import json
import threading
import uuid

import httpx

LATEST, LEGACY = "2026-07-28", "2025-11-25"


def write_key():
    return uuid.uuid4().hex


class MCPClient:
    def __init__(self, base_url, api_key, protocol=LATEST):
        self.base_url, self.api_key, self.protocol = base_url, api_key, protocol
        self.http = httpx.Client(base_url=base_url, timeout=90, headers={
            "Authorization": f"Bearer {api_key}", "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": protocol,
        })
        self._ids, self._lock = itertools.count(1), threading.Lock()
        if protocol == LATEST:
            self.initialized = self.request("server/discover")
            assert protocol in self.initialized["supportedVersions"]
        else:
            self.initialized = self.request("initialize", {
                "protocolVersion": protocol, "capabilities": {},
                "clientInfo": {"name": "vecdex-contract-tests", "version": "2.0.0"},
            })
            assert self.initialized["protocolVersion"] == protocol
            self.notify("notifications/initialized", {})

    def close(self):
        self.http.close()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def _send(self, method, params, request_id=None):
        params = dict(params or {})
        headers = {"Mcp-Method": method}
        if self.protocol == LATEST:
            params["_meta"] = {
                "io.modelcontextprotocol/protocolVersion": self.protocol,
                "io.modelcontextprotocol/clientCapabilities": {},
                "io.modelcontextprotocol/clientInfo": {"name": "vecdex-contract-tests", "version": "2.0.0"},
                **params.get("_meta", {}),
            }
        if method in ("tools/call", "prompts/get"):
            headers["Mcp-Name"] = params["name"]
        elif method == "resources/read":
            headers["Mcp-Name"] = params["uri"]
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if request_id is not None:
            body["id"] = request_id
        return self.http.post("/mcp", json=body, headers=headers)

    def wire(self, method, params=None):
        with self._lock:
            request_id = next(self._ids)
        response = self._send(method, params, request_id)
        assert response.status_code in (200, 400, 404), (response.status_code, response.text)
        if session_id := response.headers.get("MCP-Session-Id"):
            self.http.headers["MCP-Session-Id"] = session_id
        if "text/event-stream" in response.headers.get("content-type", ""):
            messages = [json.loads(line[5:].strip()) for line in response.text.splitlines()
                        if line.startswith("data:") and line[5:].strip()]
            matching = [message for message in messages if message.get("id") == request_id]
            assert len(matching) == 1, messages
            return matching[0]
        value = response.json()
        assert value["id"] == request_id
        if response.status_code != 200:
            assert "error" in value, value
        return value

    def request(self, method, params=None):
        value = self.wire(method, params)
        assert "error" not in value, value
        return value["result"]

    def notify(self, method, params):
        response = self._send(method, params)
        assert response.status_code in (200, 202, 204), response.text

    def all_pages(self, method, key):
        items, params = [], {}
        for _ in range(1000):
            page = self.request(method, params)
            items.extend(page.get(key, []))
            if not page.get("nextCursor"):
                return items
            params = {"cursor": page["nextCursor"]}
        raise AssertionError("discovery pagination did not terminate")

    def result(self, name, arguments=None):
        result = self.request("tools/call", {"name": name, "arguments": arguments or {}})
        structured = result["structuredContent"]
        texts = [part["text"] for part in result["content"] if part["type"] == "text"]
        assert len(texts) == 1, result
        assert json.loads(texts[0]) == structured, "text and structured results diverged"
        assert bool(result.get("isError", False)) == (not structured["ok"])
        if not structured["ok"]:
            error = structured["error"]
            assert isinstance(error["retryable"], bool)
            assert error["code"] and error["message"] and error["action"]
        return result

    def call(self, name, arguments=None):
        result = self.result(name, arguments)
        assert not result.get("isError", False), result
        return result["structuredContent"]["data"]

    def error(self, name, arguments=None, code=None):
        result = self.result(name, arguments)
        assert result.get("isError", False), result
        error = result["structuredContent"]["error"]
        if code is not None:
            assert error["code"] == code, error
        return error

    def resource(self, uri):
        result = self.request("resources/read", {"uri": uri})
        assert len(result["contents"]) == 1
        content = result["contents"][0]
        assert content["uri"] == uri
        return content

    def create(self, context_id, document_id, content, **fields):
        return self.call("memory_create", {"context_id": context_id, "id": document_id, "content": content,
                                           "idempotency_key": write_key(), **fields})

    def update(self, context_id, document_id, version, **fields):
        return self.call("memory_update", {"context_id": context_id, "id": document_id,
                                           "if_version": version, "idempotency_key": write_key(), **fields})

    def delete(self, context_id, document_id, version, **fields):
        return self.call("memory_delete", {"context_id": context_id, "id": document_id,
                                           "if_version": version, "idempotency_key": write_key(), **fields})

    def read(self, context_id, document_id, **fields):
        return self.call("memory_read", {"context_id": context_id, "id": document_id, **fields})

    def read_all(self, context_id, document_id, max_bytes=4096, include_metadata=True):
        first = self.read(context_id, document_id, max_bytes=max_bytes, include_metadata=include_metadata)
        parts, page = [first["content"]], first
        for _ in range(1000):
            if page["complete"]:
                return {**first, "content": "".join(parts), "complete": True}
            page = self.read(context_id, document_id, max_bytes=max_bytes,
                             offset=page["next_offset"], version=first["version"])
            assert page["version"] == first["version"]
            parts.append(page["content"])
        raise AssertionError("source read did not terminate")
