"""Source deadlines share the document commit and never depend on index cleanup."""

import time

import pytest


pytestmark = pytest.mark.p0


def test_deadline_patch_preserves_content_and_can_clear_expiry(client, collection_factory):
    name = collection_factory()["name"]
    path = f"/api/v2/collections/{name}/documents/scheduled"
    deadline = int(time.time() * 1000) + 3_600_000
    first = client.put(path, json={"content": "SciFact complete source", "expires_at": deadline})
    assert first.status_code == 201, first.text
    assert client.get(path).json()["expires_at"] == deadline
    metadata = client.patch(path, json={"metadata": {"nested": {"keep": True}}})
    assert metadata.status_code == 200
    assert client.get(path).json()["expires_at"] == deadline
    headers = {"If-Match": metadata.headers["etag"], "Idempotency-Key": "extend-deadline"}
    patch = {"expires_at": deadline + 3_600_000}
    extended = client.patch(path, json=patch, headers=headers)
    replay = client.patch(path, json=patch, headers=headers)
    assert extended.status_code == replay.status_code == 200
    assert extended.json() == replay.json()
    assert "x-embedding-tokens" not in extended.headers
    assert client.get(path).json()["expires_at"] == patch["expires_at"]
    cleared = client.patch(path, json={"expires_at": None})
    assert cleared.status_code == 200 and "x-embedding-tokens" not in cleared.headers
    source = client.get(path).json()
    assert "expires_at" not in source
    assert source["content"] == "SciFact complete source"
    assert source["metadata"] == {"nested": {"keep": True}}
    for invalid in [0, -1, 1.5, "123", True, 253402300800000]:
        rejected = client.patch(path, json={"expires_at": invalid})
        assert rejected.status_code == 400, rejected.text
    assert client.get(path).json()["version"] == cleared.json()["version"]


def test_expired_source_retries_do_not_resurrect_and_old_cas_fails(client, collection_factory):
    name = collection_factory()["name"]
    base = f"/api/v2/collections/{name}"
    path = f"{base}/documents/expired"
    body = {"content": "SciFact expired source", "expires_at": int(time.time() * 1000) - 1000}
    headers = {"Idempotency-Key": "create-expiring"}
    first = client.put(path, json=body, headers=headers)
    assert first.status_code == 201, first.text
    assert client.get(path).status_code == 404
    assert client.get(base).json()["document_count"] == 0
    assert client.get(f"{base}/documents").json()["items"] == []
    for mode in ["semantic", "keyword", "hybrid"]:
        found = client.post(f"{base}/search", json={"query": "SciFact", "mode": mode})
        assert found.status_code == 200 and found.json()["items"] == [], found.text
    retry = client.put(path, json=body, headers=headers)
    assert retry.status_code == 201 and retry.json() == first.json()
    assert "x-embedding-tokens" not in retry.headers
    for method in ["PUT", "PATCH", "DELETE"]:
        kwargs = {} if method == "DELETE" else {"json": {"content": "stale source"}}
        stale = client.request(method, path, headers={"If-Match": first.headers["etag"]}, **kwargs)
        assert stale.status_code == 409 and stale.json()["code"] == "version_conflict", stale.text
    fresh = client.put(path, json={"content": "SciFact fresh persistent source"})
    assert fresh.status_code == 201 and fresh.json()["version"] != first.json()["version"]
    assert client.get(base).json()["document_count"] == 1
    assert "expires_at" not in client.get(path).json()


def test_batch_deadlines_count_only_live_sources(client, collection_factory):
    name = collection_factory()["name"]
    base = f"/api/v2/collections/{name}"
    past = int(time.time() * 1000) - 1000
    written = client.post(f"{base}/documents/batch-upsert", json={"documents": [
        {"id": "expired", "content": "SciFact expired source", "expires_at": past},
        {"id": "persistent", "content": "SciFact persistent source"},
    ]})
    assert written.status_code == 200 and written.json()["succeeded"] == 2, written.text
    assert client.get(base).json()["document_count"] == 1
    page = client.get(f"{base}/documents").json()
    assert [item["id"] for item in page["items"]] == ["persistent"]
    assert client.get(f"{base}/documents/expired").status_code == 404
    for mode in ["semantic", "keyword", "hybrid"]:
        found = client.post(f"{base}/search", json={"query": "SciFact", "mode": mode})
        assert found.status_code == 200, found.text
        assert [item["id"] for item in found.json()["items"]] == ["persistent"]
