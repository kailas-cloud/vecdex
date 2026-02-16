"""Shared fixtures for vecdex E2E tests."""

import os
import time
import uuid

import httpx
import pytest
from tenacity import retry, stop_after_attempt, wait_fixed

VECDEX_BASE_URL = os.environ.get("VECDEX_BASE_URL", "http://localhost:8080")
VECDEX_API_KEY = os.environ.get("VECDEX_API_KEY", "test-api-key")
DB_DRIVER = os.environ.get("DB_DRIVER", "redis")

xfail_on_valkey = pytest.mark.xfail(
    DB_DRIVER == "valkey",
    reason="TEXT/BM25 search not supported on valkey-search 1.0.x",
    strict=True,
)


def unique_name() -> str:
    """Generate a unique collection name for test isolation."""
    ts = int(time.time() * 1000)
    short_id = uuid.uuid4().hex[:8]
    return f"pytest_{ts}_{short_id}"


@pytest.fixture(scope="session")
def client() -> httpx.Client:
    """Authenticated httpx client for API v1."""
    c = httpx.Client(
        base_url=VECDEX_BASE_URL,
        headers={"Authorization": f"Bearer {VECDEX_API_KEY}"},
        timeout=30.0,
    )
    yield c
    c.close()


@pytest.fixture(scope="session")
def raw_client() -> httpx.Client:
    """Unauthenticated httpx client for 401 tests."""
    c = httpx.Client(base_url=VECDEX_BASE_URL, timeout=30.0)
    yield c
    c.close()


@pytest.fixture(scope="session")
def health_client() -> httpx.Client:
    """Client without /api/v1 prefix for /health and /metrics."""
    c = httpx.Client(base_url=VECDEX_BASE_URL, timeout=30.0)
    yield c
    c.close()


@pytest.fixture()
def collection_name() -> str:
    """Return a unique collection name."""
    return unique_name()


@pytest.fixture()
def collection_factory(client: httpx.Client):
    """Factory that creates collections and cleans up after the test."""
    created: list[str] = []

    def _create(
        name: str | None = None,
        fields: list[dict] | None = None,
        type: str | None = None,
    ) -> dict:
        coll_name = name or unique_name()
        body: dict = {"name": coll_name}
        if fields:
            body["fields"] = fields
        if type:
            body["type"] = type
        resp = client.post("/collections", json=body)
        assert resp.status_code == 201, f"Failed to create collection: {resp.text}"
        created.append(coll_name)
        return resp.json()

    yield _create

    for name in created:
        client.delete(f"/collections/{name}")


@pytest.fixture()
def populated_collection(client: httpx.Client, collection_factory):
    """Collection with 5 documents, tag 'category' and numeric 'priority' fields."""
    coll = collection_factory(
        fields=[
            {"name": "category", "type": "tag"},
            {"name": "priority", "type": "numeric"},
        ]
    )
    coll_name = coll["name"]

    docs = [
        {
            "id": "doc-1",
            "content": "Python is a programming language used for web development",
            "tags": {"category": "programming"},
            "numerics": {"priority": 10},
        },
        {
            "id": "doc-2",
            "content": "Go is a statically typed language designed at Google",
            "tags": {"category": "programming"},
            "numerics": {"priority": 8},
        },
        {
            "id": "doc-3",
            "content": "Kubernetes orchestrates containerized applications",
            "tags": {"category": "infrastructure"},
            "numerics": {"priority": 9},
        },
        {
            "id": "doc-4",
            "content": "Redis is an in-memory data store for caching",
            "tags": {"category": "database"},
            "numerics": {"priority": 7},
        },
        {
            "id": "doc-5",
            "content": "Docker packages applications into containers",
            "tags": {"category": "infrastructure"},
            "numerics": {"priority": 6},
        },
    ]

    for doc in docs:
        resp = client.put(
            f"/collections/{coll_name}/documents/{doc['id']}",
            json={
                "content": doc["content"],
                "tags": doc.get("tags"),
                "numerics": doc.get("numerics"),
            },
        )
        assert resp.status_code in (200, 201), f"Failed to upsert {doc['id']}: {resp.text}"

    # Wait for indexing
    time.sleep(0.5)

    return {"name": coll_name, "docs": docs, **coll}


@retry(stop=stop_after_attempt(5), wait=wait_fixed(0.3))
def search_with_retry(client: httpx.Client, collection: str, **kwargs) -> httpx.Response:
    """Search with retry for indexing lag."""
    resp = client.post(f"/collections/{collection}/documents/search", json=kwargs)
    assert resp.status_code == 200
    data = resp.json()
    if kwargs.get("_expect_results", True) and len(data.get("items", [])) == 0:
        raise AssertionError("No search results yet, retrying...")
    return resp


def assert_embedding_headers(resp: httpx.Response):
    """Assert embedding response headers are present and numeric."""
    assert "x-embedding-tokens" in resp.headers
    tokens = int(resp.headers["x-embedding-tokens"])
    assert tokens >= 0


def assert_no_embedding_headers(resp: httpx.Response):
    """Assert embedding headers are absent."""
    assert "x-embedding-tokens" not in resp.headers


# --- NYC POI test data for geo collections ---

NYC_POIS = [
    ("times-square", "Times Square", 40.7580, -73.9855, "landmark"),
    ("central-park", "Central Park", 40.7829, -73.9654, "park"),
    ("brooklyn-bridge", "Brooklyn Bridge", 40.7061, -73.9969, "landmark"),
    ("empire-state", "Empire State Building", 40.7484, -73.9857, "landmark"),
    ("statue-liberty", "Statue of Liberty", 40.6892, -74.0445, "landmark"),
]


@pytest.fixture()
def geo_collection_factory(collection_factory):
    """Factory for geo collections with default lat/lon/category fields."""

    def _create(name=None, fields=None):
        default_fields = [
            {"name": "latitude", "type": "numeric"},
            {"name": "longitude", "type": "numeric"},
            {"name": "category", "type": "tag"},
        ]
        return collection_factory(
            name=name, fields=fields or default_fields, type="geo"
        )

    return _create


@pytest.fixture()
def populated_geo_collection(client: httpx.Client, geo_collection_factory):
    """Geo collection pre-loaded with 5 NYC POIs."""
    coll = geo_collection_factory()
    coll_name = coll["name"]

    for doc_id, content, lat, lon, category in NYC_POIS:
        resp = client.put(
            f"/collections/{coll_name}/documents/{doc_id}",
            json={
                "content": content,
                "numerics": {"latitude": lat, "longitude": lon},
                "tags": {"category": category},
            },
        )
        assert resp.status_code in (200, 201), f"Failed to upsert {doc_id}: {resp.text}"

    time.sleep(0.5)
    return {"name": coll_name, **coll}
