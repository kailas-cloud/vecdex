"""Storage and HTTP regressions for replacement, source versions, and collection lifecycle."""
from concurrent.futures import ThreadPoolExecutor
import json

import pytest


@pytest.mark.p0
def test_conditional_write_has_one_winner(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/api/v2/collections/{collection}/documents/conditional"
    created = client.put(path, json={"content": "original"})
    assert created.status_code == 201
    etag = created.headers["etag"]
    assert etag == f'"{created.json()["version"]}"'

    def update(number):
        return client.put(
            path,
            json={"content": f"writer {number}"},
            headers={"If-Match": etag},
        )

    with ThreadPoolExecutor(max_workers=8) as executor:
        responses = list(executor.map(update, range(8)))
    assert [response.status_code for response in responses].count(200) == 1
    assert [response.status_code for response in responses].count(409) == 7
    winner = next(response for response in responses if response.status_code == 200)
    current = client.get(path)
    assert current.json()["version"] != created.json()["version"]
    assert current.json()["content"] == json.loads(winner.request.content)["content"]
    assert current.headers["etag"] == winner.headers["etag"]
    assert current.json()["version"] == winner.json()["version"]
    assert current.headers["etag"] != etag
    stale = client.patch(
        path, json={"content": "stale patch"}, headers={"If-Match": etag}
    )
    assert stale.status_code == 409
    assert client.get(path).json()["content"] == json.loads(winner.request.content)["content"]


@pytest.mark.p0
def test_batch_replacement_removes_old_fields(client, collection_factory):
    collection = collection_factory(fields=[{"name": "priority", "type": "numeric"}])["name"]
    path = f"/api/v2/collections/{collection}/documents"
    original = client.put(
        f"{path}/replace",
        json={"content": "old", "metadata": {"old_tag": "old", "priority": 2}},
    )
    assert original.status_code == 201
    replaced = client.post(
        f"{path}/batch-upsert", json={"documents": [{"id": "replace", "content": "new"}]}
    )
    assert replaced.status_code == 200
    assert replaced.json()["failed"] == 0
    current = client.get(f"{path}/replace").json()
    assert current["content"] == "new"
    assert "old_tag" not in current.get("metadata", {})
    assert "priority" not in current.get("metadata", {})
    assert current["version"] != original.json()["version"]


@pytest.mark.p0
def test_delete_recreate_does_not_resurrect_documents(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/api/v2/collections/{collection}"
    assert client.put(f"{path}/documents/old", json={"content": "old"}).status_code == 201
    assert client.delete(path).status_code == 204
    assert client.post("/api/v2/collections", json={"name": collection}).status_code == 201
    assert client.get(f"{path}/documents/old").status_code == 404
    listing = client.get(f"{path}/documents")
    assert listing.status_code == 200
    assert listing.json()["items"] == []


@pytest.mark.p0
def test_collection_named_collection_cannot_overwrite_schema(client, collection_factory):
    target = collection_factory()["name"]
    collection_factory(name="collection")
    response = client.put(
        f"/api/v2/collections/collection/documents/{target}", json={"content": "ordinary document"}
    )
    assert response.status_code == 201
    metadata = client.get(f"/api/v2/collections/{target}")
    assert metadata.status_code == 200
    assert metadata.json()["name"] == target



def test_returned_etag_can_be_used_as_if_match(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/api/v2/collections/{collection}/documents/etag"
    created = client.put(path, json={"content": "initial"})
    assert created.status_code == 201
    etag = created.headers["etag"]
    replaced = client.put(path, json={"content": "replacement"}, headers={"If-Match": etag})
    assert replaced.status_code == 200, replaced.text
    stale = client.patch(path, json={"content": "stale"}, headers={"If-Match": etag})
    assert stale.status_code == 409, stale.text
    patched = client.patch(path, json={"content": "current"}, headers={"If-Match": replaced.headers["etag"]})
    assert patched.status_code == 200, patched.text


@pytest.mark.p0
def test_stale_version_cannot_change_recreated_document(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/api/v2/collections/{collection}/documents/incarnation"
    original = client.put(path, json={"content": "original"})
    assert original.status_code == 201
    old_etag = original.headers["etag"]
    assert client.delete(path, headers={"If-Match": old_etag}).status_code == 204
    replacement = client.put(path, json={"content": "new incarnation"})
    assert replacement.status_code == 201
    assert replacement.headers["etag"] != old_etag
    for method in ["PUT", "PATCH", "DELETE"]:
        kwargs = {} if method == "DELETE" else {"json": {"content": "stale"}}
        response = client.request(method, path, headers={"If-Match": old_etag}, **kwargs)
        assert response.status_code == 409, response.text
        assert response.json()["code"] == "version_conflict"
        assert response.json()["current_version"] == replacement.json()["version"]
        assert response.headers["etag"] == replacement.headers["etag"]
    assert client.get(path).json()["content"] == "new incarnation"


@pytest.mark.p0
def test_batch_versions_are_independent_per_document(client, collection_factory):
    name = collection_factory()["name"]
    base = f"/api/v2/collections/{name}/documents"
    first = client.put(f"{base}/match", json={"content": "original matching source"})
    stale = client.put(f"{base}/stale", json={"content": "original stale source"})
    assert first.status_code == stale.status_code == 201
    changed = client.patch(f"{base}/stale", json={"content": "concurrent source"})
    assert changed.status_code == 200
    content = "SciFact committed whole source. " * 2000
    response = client.post(f"{base}/batch-upsert", json={"documents": [
        {"id": "match", "content": content, "if_version": first.json()["version"]},
        {"id": "stale", "content": "forbiddenbatchwrite", "if_version": stale.json()["version"]},
        {"id": "new", "content": "unconditional source"},
        {"id": "invalid", "content": "must not prepare", "if_version": ""},
    ]})
    assert response.status_code == 200, response.text
    result = response.json()
    assert result["succeeded"] == result["failed"] == 2
    match, conflict, new, invalid = result["items"]
    assert match["status"] == new["status"] == "ok"
    assert match["version"] != first.json()["version"]
    assert conflict["error"]["code"] == "version_conflict"
    assert conflict["error"]["current_version"] == changed.json()["version"]
    assert invalid["error"]["code"] == "validation_failed"
    stored = client.get(f"{base}/match").json()
    assert stored["content"] == content and stored["version"] == match["version"]
    assert client.get(f"{base}/invalid").status_code == 404
    deleted = client.post(f"{base}/batch-delete", json={"documents": [
        {"id": "match", "if_version": first.json()["version"]},
        {"id": "new", "if_version": new["version"]},
        {"id": "stale", "if_version": changed.json()["version"]},
    ]})
    assert deleted.status_code == 200, deleted.text
    assert deleted.json()["succeeded"] == 2 and deleted.json()["failed"] == 1
    assert deleted.json()["items"][0]["error"]["current_version"] == match["version"]
    assert client.get(f"{base}/match").json()["version"] == match["version"]
    assert client.get(f"{base}/new").status_code == 404
    assert client.get(f"{base}/stale").status_code == 404


@pytest.mark.p0
def test_idempotency_replays_compact_commit_outcomes(client, collection_factory):
    name = collection_factory()["name"]
    path = f"/api/v2/collections/{name}/documents/retried"
    body = {"content": "SciFact original source", "metadata": {"nested": {"keep": True}}}
    headers = {"Idempotency-Key": "create-source"}
    first = client.put(path, json=body, headers=headers)
    assert first.status_code == 201, first.text
    assert set(first.json()) == {"id", "version", "created"}
    repeated = client.put(path, json=body, headers=headers)
    assert repeated.status_code == 201 and repeated.json() == first.json()
    assert repeated.headers["etag"] == first.headers["etag"]
    assert "x-embedding-tokens" not in repeated.headers
    conflict = client.put(path, json={"content": "different input"}, headers=headers)
    assert conflict.status_code == 409 and conflict.json()["code"] == "idempotency_conflict"
    patch_headers = {"Idempotency-Key": "patch-source", "If-Match": first.headers["etag"]}
    patch = {"content": "SciFact patched source", "metadata": {"nested": {"new": True}}}
    updated = client.patch(path, json=patch, headers=patch_headers)
    assert updated.status_code == 200 and not updated.json()["created"]
    retry = client.patch(path, json=patch, headers=patch_headers)
    assert retry.status_code == 200 and retry.json() == updated.json()
    assert "x-embedding-tokens" not in retry.headers
    delete_headers = {"Idempotency-Key": "delete-source", "If-Match": updated.headers["etag"]}
    assert client.delete(path, headers=delete_headers).status_code == 204
    assert client.delete(path, headers=delete_headers).status_code == 204
    replacement = client.put(path, json={"content": "new incarnation"})
    assert replacement.status_code == 201
    assert client.delete(path, headers=delete_headers).status_code == 204
    old = client.put(path, json=body, headers=headers)
    assert old.status_code == 201 and old.json() == first.json()
    stored = client.get(path).json()
    assert stored["content"] == "new incarnation"
    assert stored["version"] == replacement.json()["version"]


@pytest.mark.p0
def test_batch_idempotency_is_per_source(client, collection_factory):
    name = collection_factory()["name"]
    base = f"/api/v2/collections/{name}/documents"
    documents = [
        {"id": "a", "content": "source a", "idempotency_key": "create-a"},
        {"id": "b", "content": "source b", "idempotency_key": "create-b"},
    ]
    first = client.post(f"{base}/batch-upsert", json={"documents": documents})
    again = client.post(f"{base}/batch-upsert", json={"documents": documents})
    assert first.status_code == again.status_code == 200
    assert first.json() == again.json() and again.json()["succeeded"] == 2
    assert "x-embedding-tokens" not in again.headers
    documents[0]["content"] = "changed source"
    mixed = client.post(f"{base}/batch-upsert", json={"documents": documents})
    assert mixed.json()["succeeded"] == mixed.json()["failed"] == 1
    assert mixed.json()["items"][0]["error"]["code"] == "idempotency_conflict"
    selectors = [
        {"id": item["id"], "if_version": item["version"], "idempotency_key": f"delete-{item['id']}"}
        for item in first.json()["items"]
    ]
    for _ in range(2):
        result = client.post(f"{base}/batch-delete", json={"documents": selectors})
        assert result.status_code == 200 and result.json()["succeeded"] == 2


@pytest.mark.p0
def test_idempotent_patch_distinguishes_omitted_and_null_metadata(client, collection_factory):
    name = collection_factory()["name"]
    path = f"/api/v2/collections/{name}/documents/intent"
    first = client.put(path, json={"content": "original source", "metadata": {"keep": True}})
    assert first.status_code == 201
    headers = {"Idempotency-Key": "content-only"}
    assert client.patch(path, json={"content": "updated source"}, headers=headers).status_code == 200
    conflict = client.patch(path, json={"content": "updated source", "metadata": None}, headers=headers)
    assert conflict.status_code == 409 and conflict.json()["code"] == "idempotency_conflict"
    assert client.get(path).json()["metadata"] == {"keep": True}
