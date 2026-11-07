"""Retrieval, indexed metadata and response budgets through MCP."""
import pytest

from conftest import eventually
from mcp_client import write_key

pytestmark = pytest.mark.p0


@pytest.mark.parametrize("mode", ["keyword", "semantic", "hybrid"])
def test_search_and_similar_have_source_versions(mcp, populated_collection, mode):
    context = populated_collection["context_id"]
    data = mcp.call("memory_search", {"context_id": context, "query": "memoryrecord", "mode": mode, "limit": 3})
    assert 0 < len(data["items"]) <= 3
    for hit in data["items"]:
        assert hit["id"] and hit["version"] and hit["uri"] and isinstance(hit["score"], (int, float))
        assert "vector" not in hit
        assert mcp.read(context, hit["id"])["version"] == hit["version"]
    similar = mcp.call("memory_similar", {"context_id": context, "id": "doc-1", "limit": 3})
    assert all(item["id"] != "doc-1" for item in similar["items"])
    assert len(similar["items"]) <= 3


@pytest.mark.parametrize("filters,expected", [
    ({"must": [{"key": "category", "match": "programming"}]}, {"doc-1", "doc-2"}),
    ({"must_not": [{"key": "category", "match": "programming"}]}, {"doc-3", "doc-4", "doc-5"}),
    ({"should": [{"key": "category", "match": "database"}, {"key": "priority", "range": {"gt": 9}}]}, {"doc-1", "doc-4"}),
    ({"must": [{"key": "priority", "range": {"gte": 7, "lt": 9}}]}, {"doc-2", "doc-4"}),
    ({"must": [{"key": "priority", "range": {"gt": 7, "lte": 9}}]}, {"doc-2", "doc-3"}),
    ({"must": [{"key": "category", "match": "programming"}], "must_not": [{"key": "priority", "range": {"gt": 9}}]}, {"doc-2"}),
    ({"must": [{"key": "category", "match": "absent"}]}, set()),
])
@pytest.mark.parametrize("mode", ["keyword", "hybrid"])
def test_filters(mcp, populated_collection, filters, expected, mode):
    data = mcp.call("memory_search", {"context_id": populated_collection["context_id"], "query": "memoryrecord", "mode": mode, "filters": filters, "limit": 20})
    assert {hit["id"] for hit in data["items"]} == expected


def test_context_defaults_override_and_search_budget(mcp, populated_collection, context_factory):
    context = context_factory(populated_collection, search={"mode": "keyword", "limit": 1, "filters": {"must": [{"key": "category", "match": "database"}]}})
    args = {"context_id": context["context_id"], "query": "memoryrecord"}
    assert [item["id"] for item in mcp.call("memory_search", args)["items"]] == ["doc-4"]
    override = mcp.call("memory_search", {**args, "filters": {}, "limit": 5, "max_content_bytes": 40})
    assert len(override["items"]) <= 5
    assert sum(len(item["content"].encode()) for item in override["items"]) <= 40
    assert any(item["content_truncated"] for item in override["items"])
    low = mcp.result("memory_search", {**args, "filters": {}, "limit": 100, "max_output_bytes": 4096})
    import json
    assert len(json.dumps(low, separators=(",", ":"), ensure_ascii=False).encode()) <= 4096


def test_full_source_tail_replacement_and_filter_patch(mcp, collection_factory):
    collection = collection_factory(fields=[{"name": "tag", "type": "tag"}])
    context = collection["context_id"]
    content = "first paragraph " * 4500 + " lastfragmentneedle"
    first = mcp.create(context, "fragments", content, metadata={"tag": "before"})
    def found(query, expected, filters=None):
        data = mcp.call("memory_search", {"context_id": context, "query": query, "mode": "keyword", "filters": filters or {}})
        assert {item["id"] for item in data["items"]} == expected
    eventually(lambda: found("lastfragmentneedle", {"fragments"}))
    second = mcp.update(context, "fragments", first["version"], metadata={"tag": "after"})
    eventually(lambda: found("lastfragmentneedle", {"fragments"}, {"must": [{"key": "tag", "match": "after"}]}))
    third = mcp.update(context, "fragments", second["version"], content="replacementneedle")
    eventually(lambda: found("lastfragmentneedle", set()))
    eventually(lambda: found("replacementneedle", {"fragments"}))
    mcp.delete(context, "fragments", third["version"])
    eventually(lambda: found("replacementneedle", set()))


@pytest.mark.parametrize("fields", [
    {"query": ""}, {"query": "x" * 4097}, {"mode": "unknown"}, {"limit": 101}, {"limit": -1},
    {"filters": {"must": [{"key": "missing", "match": "x"}]}},
    {"filters": {"must": [{"key": "priority", "match": "x"}]}},
    {"filters": {"must": [{"key": "category", "range": {"gt": 2}}]}},
    {"filters": {"must": [{"key": "priority", "range": {"gte": 10, "lt": 1}}]}},
    {"filters": {"must": [{"key": "category", "match": "x", "range": {"gt": 1}}]}},
])
def test_invalid_search_contract(mcp, populated_collection, fields):
    mcp.error("memory_search", {"context_id": populated_collection["context_id"], "query": "memoryrecord", **fields}, "invalid_argument")


def test_declared_field_types_are_enforced(mcp, collection_factory):
    context = collection_factory(fields=[{"name": "tag", "type": "tag"}, {"name": "count", "type": "numeric"}])["context_id"]
    for metadata in ({"tag": 10}, {"count": "ten"}, {"count": True}):
        mcp.error("memory_create", {"context_id": context, "id": "invalid", "content": "source", "metadata": metadata, "idempotency_key": write_key()}, "invalid_argument")
