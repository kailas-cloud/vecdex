"""Keyset pagination remains usable while earlier IDs are inserted or deleted."""
import pytest

pytestmark = pytest.mark.p0


def test_document_cursor_ignores_offset_shifts(client, collection_factory):
    name = collection_factory()["name"]
    base = f"/collections/{name}"
    documents = f"{base}/documents"
    for identity in ("a", "b", "c", "d", "e"):
        assert client.put(f"{documents}/{identity}", json={"content": f"cursor document {identity}"}).status_code == 201
    page = client.get(documents, params={"limit": 2}).json()
    assert [item["id"] for item in page["items"]] == ["a", "b"]
    assert page["has_more"] is True
    cursor = page["next_cursor"]
    assert client.delete(f"{documents}/a").status_code == 204
    assert client.delete(f"{documents}/b").status_code == 204
    assert client.put(f"{documents}/aa", json={"content": "inserted before the cursor"}).status_code == 201
    assert client.put(f"{documents}/z", json={"content": "inserted after the cursor"}).status_code == 201
    # Replacing an existing parent does not duplicate its catalog entry.
    assert client.put(f"{documents}/c", json={"content": "updated document"}).status_code == 200
    seen = []
    while cursor:
        response = client.get(documents, params={"limit": 2, "cursor": cursor})
        assert response.status_code == 200, response.text
        page = response.json()
        seen.extend(item["id"] for item in page["items"])
        cursor = page.get("next_cursor")
    assert seen == ["c", "d", "e", "z"]
    assert client.get(base).json()["document_count"] == 5


def test_document_cursor_rejects_recreated_collection(client, collection_factory):
    name = collection_factory()["name"]
    base = f"/collections/{name}"
    for identity in ("a", "b"):
        assert client.put(f"{base}/documents/{identity}", json={"content": identity}).status_code == 201
    cursor = client.get(f"{base}/documents", params={"limit": 1}).json()["next_cursor"]
    assert client.delete(base).status_code == 204
    assert client.post("/collections", json={"name": name, "type": "text"}).status_code == 201
    assert client.put(f"{base}/documents/new", json={"content": "new generation"}).status_code == 201
    stale = client.get(f"{base}/documents", params={"limit": 1, "cursor": cursor})
    assert stale.status_code == 400, stale.text
    assert stale.json()["code"] == "validation_failed"
    assert client.get(base).json()["document_count"] == 1
