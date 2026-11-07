"""Negotiation, discovery, concrete resources and tool error wire contract."""
import json
import os

import pytest

from mcp_client import LEGACY, LATEST, MCPClient

pytestmark = pytest.mark.p0


def test_discovery_is_complete_and_structured(mcp):
    capabilities = mcp.initialized["capabilities"]
    assert {"tools", "resources", "prompts"} <= set(capabilities)
    first = mcp.request("tools/list")
    assert first.get("nextCursor"), "fixture must exercise discovery pagination"
    tools = mcp.all_pages("tools/list", "tools")
    names = {tool["name"] for tool in tools}
    assert len(names) == len(tools) == 17
    assert {"memory_create", "memory_read", "memory_update", "memory_delete", "memory_list", "memory_search", "memory_similar", "context_create", "context_get", "context_update", "context_delete", "context_list", "collection_create", "collection_get", "collection_delete", "collection_list", "memory_status"} == names
    for tool in tools:
        assert tool["description"] and tool["inputSchema"] and tool["outputSchema"]
    status = mcp.call("memory_status")
    assert set(status["protocol_versions"]) == {LATEST, LEGACY}
    assert status["cache_scope"] == "private"
    assert status["max_result_bytes"] >= 4096
    usage = status["embedding_usage"]
    assert usage["period"] == "month" and usage["tokens"] >= 0


def test_legacy_fallback_is_explicit(mcp):
    with MCPClient(mcp.base_url, mcp.api_key, LEGACY) as legacy:
        assert len(legacy.all_pages("tools/list", "tools")) == 17
        assert legacy.call("memory_status")["cache_scope"] == "private"
        assert not legacy.initialized["capabilities"].get("resources", {}).get("subscribe", False)


def test_resources_and_prompts_carry_usable_workflows(mcp, collection_factory):
    resources = mcp.all_pages("resources/list", "resources")
    uris = {resource["uri"] for resource in resources}
    assert {"vecdex://guide", "vecdex://schema", "vecdex://status", "vecdex://collections", "vecdex://contexts"} <= uris
    guide = mcp.resource("vecdex://guide")["text"]
    assert "idempotency" in guide and "generation" in guide
    assert mcp.resource("vecdex://schema")["text"]
    col = collection_factory()
    prompts = mcp.all_pages("prompts/list", "prompts")
    assert prompts
    for prompt in prompts:
        arguments = {arg["name"]: col["context_id"] if arg["name"] == "context_id" else "test" for arg in prompt.get("arguments", []) if arg.get("required")}
        assert mcp.request("prompts/get", {"name": prompt["name"], "arguments": arguments})["messages"]
    source = mcp.create(col["context_id"], "resource", "source evidence", metadata={"source": "test"})
    value = json.loads(mcp.resource(source["uri"])["text"])
    assert value["content"] == "source evidence" and value["version"] == source["version"]
    context = json.loads(mcp.resource(col["context"]["uri"])["text"])
    assert context["context_id"] == col["context_id"]


@pytest.mark.parametrize("headers", [{}, {"Authorization": "Bearer incorrect"}, {"Authorization": "Basic dGVzdA=="}])
def test_mcp_auth_rejects_unauthorized(raw_client, headers):
    response = raw_client.post("/mcp", headers={"Accept": "application/json, text/event-stream", **headers}, json={"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}})
    assert response.status_code == 401


@pytest.mark.parametrize("path", ["/api/v1/collections", "/api/v2/collections", "/api/v2/usage"])
def test_removed_rest_surface(client, path):
    assert client.get(path).status_code == 404


def test_unknown_method_and_schema_error_are_distinct(mcp):
    assert mcp.wire("vecdex/does-not-exist")["error"]["code"] == -32601
    mcp.error("memory_read", {}, "invalid_argument")


def test_latest_requires_method_header(mcp):
    response = mcp.http.post("/mcp", json={"jsonrpc": "2.0", "id": 99, "method": "tools/list", "params": {"_meta": {"io.modelcontextprotocol/protocolVersion": LATEST, "io.modelcontextprotocol/clientCapabilities": {}}}})
    assert response.status_code == 400
    assert response.json()["error"]["code"] == -32020


def test_request_and_result_limits(mcp, collection_factory):
    context = collection_factory()["context_id"]
    mcp.error("memory_status", {"max_output_bytes": 4095}, "invalid_argument")
    mcp.error("memory_status", {"max_output_bytes": 262145}, "invalid_argument")
    mcp.create(context, "large-metadata", "small text", metadata={"value": "x" * 20000})
    mcp.error("memory_read", {"context_id": context, "id": "large-metadata", "include_metadata": True, "max_output_bytes": 4096}, "response_too_large")
    assert mcp.read(context, "large-metadata", include_content=False, metadata_fields=[]).get("content") is None


def test_read_only_principal_and_owner_isolation(mcp, collection_factory):
    col = collection_factory()
    with MCPClient(mcp.base_url, os.getenv("VECDEX_READ_API_KEY", "test-read-key")) as reader:
        reader.error("collection_create", {"name": "forbidden"}, "forbidden")
        reader.error("context_get", {"context_id": col["context_id"]}, "not_found")
        own = reader.call("context_create", {"name": "reader context", "collection": col["name"], "generation": col["generation"], "idempotency_key": "reader-" + col["name"]})["context"]
        names = {tool["name"] for tool in reader.all_pages("tools/list", "tools")}
        assert "memory_read" in names and "memory_create" not in names and "collection_create" not in names
        reader.error("memory_create", {"context_id": own["context_id"], "id": "forbidden", "content": "read only", "idempotency_key": "forbidden"}, "forbidden")
        reader.call("context_delete", {"context_id": own["context_id"], "expected_version": own["version"], "idempotency_key": "reader-delete-" + col["name"]})


def test_mcp_request_tracing(mcp):
    response = mcp.http.post("/mcp", headers={"Mcp-Method": "tools/list", "X-Request-ID": "mcp-contract-trace"}, json={"jsonrpc": "2.0", "id": 100, "method": "tools/list", "params": {"_meta": {"io.modelcontextprotocol/protocolVersion": LATEST, "io.modelcontextprotocol/clientCapabilities": {}}}})
    assert response.status_code == 200
    assert response.headers["X-Request-ID"] == "mcp-contract-trace"
    response = mcp._send("tools/list", {}, 101)
    assert response.headers["X-Request-ID"]


def test_skills_manifest_and_resource_digests(mcp):
    import hashlib
    skills = mcp.all_pages("skills/list", "skills")
    assert skills
    for skill in skills:
        assert skill["frontmatter"]["name"] and skill["frontmatter"]["description"]
        assert mcp.request("skills/get", {"uri": skill["uri"]})["skill"] == skill
        assert skill["resources"]
        for resource in skill["resources"]:
            text = mcp.resource(resource["uri"])["text"].encode()
            assert len(text) == resource["size"]
            assert "sha256:" + hashlib.sha256(text).hexdigest() == resource["digest"]
