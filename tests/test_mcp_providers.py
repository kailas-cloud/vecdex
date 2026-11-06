"""Provider failures, cache accounting and shared budgets at the MCP boundary."""
import hashlib
import os

import httpx
import pytest

from mcp_client import MCPClient, write_key

MOCK_URL = os.getenv("VECDEX_BEDROCK_MOCK_URL")
pytestmark = pytest.mark.p0


def matching_calls(content):
    digest = hashlib.sha256(content.encode()).hexdigest()
    response = httpx.get(f"{MOCK_URL}/_bedrock/calls", timeout=30)
    response.raise_for_status()
    return [call for call in response.json()["calls"] if digest in call["hashes"]]


@pytest.mark.skipif(not MOCK_URL, reason="Bedrock mock profile only")
def test_long_v4_source_whole_and_query_purpose(mcp, collection_factory):
    col = collection_factory()
    content = col["name"] + "\n" + "English русский 中文 🚀 code x=1;\n" * 400
    mcp.create(col["context_id"], "whole", content)
    assert mcp.read_all(col["context_id"], "whole")["content"] == content
    calls = matching_calls(content)
    assert len(calls) == 1
    assert calls[0]["lengths"] == [len(content)]
    assert calls[0]["purpose"] == "search_document"
    assert calls[0]["max_tokens"] == 128000 and calls[0]["truncate"] == "NONE"
    query = col["name"] + " unique query"
    mcp.call("memory_search", {"context_id": col["context_id"], "query": query, "mode": "semantic"})
    assert matching_calls(query)[0]["purpose"] == "search_query"


@pytest.mark.skipif(not MOCK_URL, reason="Bedrock mock profile only")
@pytest.mark.parametrize("marker", ["MOCK_NO_TOKEN_HEADER", "MOCK_BAD_TOKEN_HEADER"])
def test_unknown_usage_reservation_and_cache_hit(mcp, collection_factory, marker):
    col = collection_factory()
    content = f"{col['name']} {marker} complete source"
    before = mcp.call("memory_status")["embedding_usage"]
    mcp.create(col["context_id"], "first", content)
    after = mcp.call("memory_status")["embedding_usage"]
    assert after["unknown_requests"] == before["unknown_requests"] + 1
    assert after["budget_remaining"] <= before["budget_remaining"] - 128000
    mcp.create(col["context_id"], "cached", content)
    assert len(matching_calls(content)) == 1


@pytest.mark.skipif(not os.getenv("VECDEX_PEER_URL"), reason="shared replica fixture not configured")
def test_replicas_share_context_sources_and_budget(mcp, collection_factory):
    col = collection_factory()
    before = mcp.call("memory_status")["embedding_usage"]
    with MCPClient(os.environ["VECDEX_PEER_URL"], mcp.api_key) as peer:
        peer.create(col["context_id"], "from-peer", col["name"] + " peer source")
        assert mcp.read(col["context_id"], "from-peer")["content"] == col["name"] + " peer source"
        first = mcp.call("memory_status")["embedding_usage"]
        peer_report = peer.call("memory_status")["embedding_usage"]
        last = mcp.call("memory_status")["embedding_usage"]
        assert first["requests"] > before["requests"]
        # Health probes can account for work between observations. The peer must
        # report the same monotonic counters, bounded by primary observations.
        for field in ("tokens", "requests", "unknown_requests"):
            assert first[field] <= peer_report[field] <= last[field]
        assert first["budget_limit"] == peer_report["budget_limit"] == last["budget_limit"]
        assert first["budget_remaining"] >= peer_report["budget_remaining"] >= last["budget_remaining"]


@pytest.mark.skipif(not os.getenv("VECDEX_INCOMPATIBLE_URL"), reason="incompatible embedder fixture not configured")
def test_incompatible_space_preserves_nonsemantic_access(mcp, collection_factory):
    col = collection_factory()
    first = mcp.create(col["context_id"], "space", "original space content")
    with MCPClient(os.environ["VECDEX_INCOMPATIBLE_URL"], mcp.api_key) as peer:
        assert peer.read(col["context_id"], "space")["content"] == "original space content"
        peer.call("memory_list", {"context_id": col["context_id"]})
        peer.call("memory_search", {"context_id": col["context_id"], "query": "original", "mode": "keyword"})
        patched = peer.update(col["context_id"], "space", first["version"], metadata={"safe": True})
        peer.error("memory_update", {"context_id": col["context_id"], "id": "space", "if_version": patched["version"], "content": "different embedding", "idempotency_key": write_key()}, "embedding_space_mismatch")
        peer.error("memory_search", {"context_id": col["context_id"], "query": "original", "mode": "semantic"}, "embedding_space_mismatch")
        peer.error("memory_similar", {"context_id": col["context_id"], "id": "space"}, "embedding_space_mismatch")


@pytest.mark.skipif(not os.getenv("VECDEX_BUDGET_URL"), reason="isolated exhausted budget fixture not configured")
def test_budget_rejection_keeps_read_only_and_metadata_access(mcp, collection_factory):
    col = collection_factory()
    source = mcp.create(col["context_id"], "existing", col["name"] + " existing budget evidence")
    with MCPClient(os.environ["VECDEX_BUDGET_URL"], mcp.api_key) as limited:
        args = {"context_id": col["context_id"]}
        assert limited.read(col["context_id"], "existing")["version"] == source["version"]
        limited.update(col["context_id"], "existing", source["version"], metadata={"still": "writable"})
        limited.call("memory_search", {**args, "query": "existing", "mode": "keyword"})
        limited.error("memory_create", {**args, "id": "rejected", "content": col["name"] + " unique uncached forbidden cost", "idempotency_key": write_key()}, "budget_exhausted")
        limited.error("memory_search", {**args, "query": col["name"] + " new semantic query", "mode": "semantic"}, "budget_exhausted")
        mcp.error("memory_read", {**args, "id": "rejected"}, "not_found")
        usage = limited.call("memory_status")["embedding_usage"]
        assert usage["budget_limit"] == 1
