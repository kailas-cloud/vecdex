"""Replicas with incompatible vectorizers must not mix embeddings in a collection."""
import os

import httpx
import pytest

pytestmark = pytest.mark.p0


def test_model_change_requires_reindexing(client, collection_factory):
    incompatible_url = os.getenv("VECDEX_INCOMPATIBLE_URL")
    if not incompatible_url:
        pytest.skip("second vectorizer is not configured")
    collection = collection_factory(fields=[{"name": "category", "type": "tag"}])["name"]
    base = f"/collections/{collection}"
    path = f"{base}/documents/source"
    created = client.put(path, json={"content": "original text", "tags": {"category": "before"}})
    assert created.status_code == 201
    fingerprint = client.get(base).json()["embedding_space"]
    assert len(fingerprint) == 64
    with httpx.Client(base_url=incompatible_url, headers={"Authorization": "Bearer test-api-key"}) as changed:
        assert changed.get(base).json()["embedding_space"] == fingerprint
        for response in (
            changed.put(path, json={"content": "replacement"}),
            changed.patch(path, json={"content": "replacement"}),
            changed.post(f"{base}/documents/search", json={"query": "original", "mode": "semantic"}),
            changed.post(f"{path}/similar", json={}),
        ):
            assert response.status_code == 409, response.text
            assert response.json()["code"] == "embedding_space_mismatch"
        batch = changed.post(f"{base}/documents/batch-upsert", json={
            "documents": [{"id": "batch", "content": "replacement"}],
        })
        assert batch.status_code == 200, batch.text
        assert batch.json()["failed"] == 1
        assert batch.json()["items"][0]["error"]["code"] == "embedding_space_mismatch"
        # Source extraction and metadata maintenance remain available for reindexing.
        assert changed.get(path).json()["content"] == "original text"
        assert changed.get(f"{base}/documents").json()["items"][0]["id"] == "source"
        assert changed.patch(path, json={"tags": {"category": "after"}}).status_code == 200
        keyword = changed.post(f"{base}/documents/search", json={"query": "original", "mode": "keyword"})
        assert keyword.status_code == 200, keyword.text
        assert keyword.json()["items"][0]["id"] == "source"
    assert client.get(path).json()["content"] == "original text"
    assert client.get(path).json()["tags"]["category"] == "after"
