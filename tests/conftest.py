"""Native MCP E2E fixtures. Every memory operation uses tools/call."""
import os
import time
import uuid

import httpx
import pytest

from mcp_client import MCPClient, write_key

VECDEX_BASE_URL = os.environ.get("VECDEX_BASE_URL", "http://localhost:8080")
VECDEX_API_KEY = os.environ.get("VECDEX_API_KEY", "test-api-key")


def unique_name():
    return "pytest_" + uuid.uuid4().hex


def eventually(assertion, attempts=20):
    for attempt in range(attempts):
        try:
            return assertion()
        except AssertionError:
            if attempt == attempts - 1:
                raise
            time.sleep(0.15)


@pytest.fixture(scope="session")
def mcp():
    with MCPClient(VECDEX_BASE_URL, VECDEX_API_KEY) as connection:
        yield connection


@pytest.fixture(scope="session")
def raw_client():
    with httpx.Client(base_url=VECDEX_BASE_URL, timeout=30) as connection:
        yield connection


@pytest.fixture(scope="session")
def health_client(raw_client):
    return raw_client


@pytest.fixture(scope="session")
def client():
    """HTTP client only for operator health/tracing endpoints."""
    with httpx.Client(base_url=VECDEX_BASE_URL, timeout=30,
                      headers={"Authorization": f"Bearer {VECDEX_API_KEY}"}) as connection:
        yield connection


def open_context(mcp, collection, **settings):
    return mcp.call("context_create", {
        "name": "E2E " + collection["name"], "collection": collection["name"],
        "generation": collection["generation"], "idempotency_key": write_key(), **settings,
    })["context"]


def close_context(mcp, context_id):
    result = mcp.result("context_get", {"context_id": context_id})["structuredContent"]
    if result["ok"]:
        mcp.call("context_delete", {"context_id": context_id, "expected_version": result["data"]["version"],
                                    "idempotency_key": write_key()})
    else:
        assert result["error"]["code"] == "not_found", result


@pytest.fixture()
def context_factory(mcp):
    opened = []
    def create(collection, **settings):
        context = open_context(mcp, collection, **settings)
        opened.append(context["context_id"])
        return context
    yield create
    for context_id in opened:
        close_context(mcp, context_id)


@pytest.fixture()
def collection_factory(mcp):
    created = []
    def create(name=None, fields=None):
        collection = mcp.call("collection_create", {"name": name or unique_name(), "fields": fields or []})
        context = open_context(mcp, collection)
        created.append((collection, context))
        return {**collection, "context_id": context["context_id"], "context": context}
    yield create
    for collection, context in reversed(created):
        close_context(mcp, context["context_id"])
        current = mcp.call("collection_get", {"name": collection["name"]})
        if current["state"] == "active" and current["generation"] == collection["generation"]:
            mcp.call("collection_delete", {"name": collection["name"], "generation": collection["generation"],
                                           "confirm_name": collection["name"]})


@pytest.fixture()
def populated_collection(mcp, collection_factory):
    collection = collection_factory(fields=[{"name": "category", "type": "tag"}, {"name": "priority", "type": "numeric"}])
    descriptions = [
        ("Python programming language for web development", "programming", 10),
        ("Go programming language designed at Google", "programming", 8),
        ("Kubernetes orchestrates containerized applications", "infrastructure", 9),
        ("Valkey is an in-memory database for caching", "database", 7),
        ("Docker packages applications into containers", "infrastructure", 6),
    ]
    documents = []
    for index, (text, category, priority) in enumerate(descriptions, 1):
        document = {"id": f"doc-{index}", "content": "memoryrecord " + text,
                    "metadata": {"category": category, "priority": priority}}
        receipt = mcp.create(collection["context_id"], document["id"], document["content"], metadata=document["metadata"])
        documents.append({**document, "version": receipt["version"]})
    def indexed():
        found = mcp.call("memory_search", {"context_id": collection["context_id"], "query": "memoryrecord", "mode": "keyword"})
        assert len(found["items"]) == 5
    eventually(indexed)
    return {**collection, "docs": documents}
