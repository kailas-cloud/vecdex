"""Long documents are split by the server only after an explicit model limit."""
import os
import pytest


pytestmark = pytest.mark.p0


def test_long_document_patch_and_delete_lifecycle(client, collection_factory):
    collection = collection_factory(fields=[{"name": "category", "type": "tag"}])["name"]
    base = f"/collections/{collection}"
    path = f"{base}/documents/whole"
    content = ("Начало документа 中文 🚀\n" * 2500) + "\nFinal unique tailwordfragment."
    assert 32768 < len(content) and len(content.encode()) <= 163840
    created = client.put(path, json={"content": content, "tags": {"category": "old"}})
    assert created.status_code == 201, created.text
    assert client.get(path).json()["content"] == content
    listing = client.get(f"{base}/documents").json()
    assert len(listing["items"]) == 1
    assert listing["items"][0]["id"] == "whole"
    found = client.post(f"{base}/documents/search", json={"query": "tailwordfragment", "mode": "keyword"})
    assert found.status_code == 200, found.text
    assert [item["id"] for item in found.json()["items"]] == ["whole"]
    assert "tailwordfragment" in found.json()["items"][0]["content"]
    other = f"{base}/documents/other"
    assert client.put(other, json={"content": "different complete record"}).status_code == 201
    similar = client.post(f"{path}/similar", json={"top_k": 5, "limit": 5})
    assert similar.status_code == 200, similar.text
    assert [item["id"] for item in similar.json()["items"]] == ["other"]
    assert client.delete(other).status_code == 204
    patched = client.patch(path, json={"tags": {"category": "new"}})
    assert patched.status_code == 200, patched.text
    assert "x-embedding-tokens" not in patched.headers
    filtered = client.post(f"{base}/documents/search", json={
        "query": "tailwordfragment", "mode": "keyword",
        "filters": {"must": [{"key": "category", "match": "new"}]},
    })
    assert [item["id"] for item in filtered.json()["items"]] == ["whole"]
    assert client.patch(path, json={"content": "brief replacement"}).status_code == 200
    stale = client.post(f"{base}/documents/search", json={"query": "tailwordfragment", "mode": "keyword"})
    assert stale.json()["items"] == []
    assert client.get(path).json()["content"] == "brief replacement"
    assert client.delete(path).status_code == 204
    assert client.get(path).status_code == 404
    assert client.get(f"{base}/documents").json()["items"] == []


@pytest.mark.skipif(os.getenv("VECDEX_EMBEDDING_BACKEND") == "onnx", reason="mock-only injected failure")
def test_failed_fragment_preserves_previous_document(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/collections/{collection}/documents/stable"
    original = client.put(path, json={"content": "previous complete document"})
    assert original.status_code == 201
    content = "safe prefix " * 6000 + "MOCK_FAIL_FRAGMENT"
    failed = client.put(path, json={"content": content})
    assert failed.status_code == 502
    current = client.get(path).json()
    assert current["content"] == "previous complete document"
    assert current["revision"] == original.json()["revision"]


@pytest.mark.skipif(os.getenv("VECDEX_EMBEDDING_BACKEND") == "onnx", reason="mock-only injected failure")
def test_mixed_batch_keeps_parent_results(client, collection_factory):
    collection = collection_factory()["name"]
    base = f"/collections/{collection}/documents"
    long_text = "paragraph of a long document\n" * 2500
    response = client.post(f"{base}/batch-upsert", json={"documents": [
        {"id": "large", "content": long_text},
        {"id": "small", "content": "small complete document"},
        {"id": "failed", "content": "prefix " * 7000 + "MOCK_FAIL_FRAGMENT"},
    ]})
    assert response.status_code == 200, response.text
    assert response.json()["succeeded"] == 2
    assert response.json()["failed"] == 1
    assert client.get(f"{base}/large").json()["content"] == long_text
    assert client.get(f"{base}/small").json()["content"] == "small complete document"
    assert client.get(f"{base}/failed").status_code == 404
