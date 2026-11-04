"""Storage and HTTP regressions for replacement, revisions, and collection lifecycle."""
from concurrent.futures import ThreadPoolExecutor

import pytest


@pytest.mark.p0
def test_conditional_write_has_one_winner(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/collections/{collection}/documents/conditional"
    created = client.put(path, json={"content": "original"})
    assert created.status_code == 201
    revision = created.json()["revision"]
    assert created.headers["etag"] == f'"{revision}"'

    def update(number):
        return client.put(
            path,
            json={"content": f"writer {number}"},
            headers={"If-Match": str(revision)},
        )

    with ThreadPoolExecutor(max_workers=8) as executor:
        responses = list(executor.map(update, range(8)))
    assert [response.status_code for response in responses].count(200) == 1
    assert [response.status_code for response in responses].count(409) == 7
    winner = next(response for response in responses if response.status_code == 200)
    current = client.get(path)
    assert current.json()["revision"] == revision + 1
    assert current.json()["content"] == winner.json()["content"]
    assert current.headers["etag"] == f'"{revision + 1}"'
    stale = client.patch(
        path, json={"content": "stale patch"}, headers={"If-Match": str(revision)}
    )
    assert stale.status_code == 409
    assert client.get(path).json()["content"] == winner.json()["content"]


@pytest.mark.p0
def test_batch_replacement_removes_old_fields(client, collection_factory):
    collection = collection_factory(fields=[{"name": "priority", "type": "numeric"}])["name"]
    path = f"/collections/{collection}/documents"
    original = client.put(
        f"{path}/replace",
        json={"content": "old", "tags": {"old_tag": "old"}, "numerics": {"priority": 2}},
    )
    assert original.status_code == 201
    replaced = client.post(
        f"{path}/batch-upsert", json={"documents": [{"id": "replace", "content": "new"}]}
    )
    assert replaced.status_code == 200
    assert replaced.json()["failed"] == 0
    current = client.get(f"{path}/replace").json()
    assert current["content"] == "new"
    assert "old_tag" not in current.get("tags", {})
    assert "priority" not in current.get("numerics", {})
    assert current["revision"] == original.json()["revision"] + 1


@pytest.mark.p0
def test_delete_recreate_does_not_resurrect_documents(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/collections/{collection}"
    assert client.put(f"{path}/documents/old", json={"content": "old"}).status_code == 201
    assert client.delete(path).status_code == 204
    assert client.post("/collections", json={"name": collection}).status_code == 201
    assert client.get(f"{path}/documents/old").status_code == 404
    listing = client.get(f"{path}/documents")
    assert listing.status_code == 200
    assert listing.json()["items"] == []


@pytest.mark.p0
def test_collection_named_collection_cannot_overwrite_schema(client, collection_factory):
    target = collection_factory()["name"]
    collection_factory(name="collection")
    response = client.put(
        f"/collections/collection/documents/{target}", json={"content": "ordinary document"}
    )
    assert response.status_code == 201
    metadata = client.get(f"/collections/{target}")
    assert metadata.status_code == 200
    assert metadata.json()["name"] == target


@pytest.mark.p0
@pytest.mark.parametrize("field", ["__content", "__vector", "__revision", "__n:priority"])
def test_reserved_tags_rejected_on_all_write_paths(client, collection_factory, field):
    collection = collection_factory()["name"]
    path = f"/collections/{collection}/documents"
    assert client.put(f"{path}/safe", json={"content": "safe"}).status_code == 201
    assert client.put(
        f"{path}/safe", json={"content": "unsafe", "tags": {field: "overwrite"}}
    ).status_code == 400
    assert client.patch(f"{path}/safe", json={"tags": {field: None}}).status_code == 400
    batch = client.post(
        f"{path}/batch-upsert",
        json={"documents": [{"id": "safe", "content": "unsafe", "tags": {field: "overwrite"}}]},
    )
    assert batch.status_code == 200
    assert batch.json()["failed"] == 1
    assert client.get(f"{path}/safe").json()["content"] == "safe"


def test_returned_etag_can_be_used_as_if_match(client, collection_factory):
    collection = collection_factory()["name"]
    path = f"/collections/{collection}/documents/etag"
    created = client.put(path, json={"content": "initial"})
    assert created.status_code == 201
    etag = created.headers["etag"]
    replaced = client.put(path, json={"content": "replacement"}, headers={"If-Match": etag})
    assert replaced.status_code == 200, replaced.text
    stale = client.patch(path, json={"content": "stale"}, headers={"If-Match": etag})
    assert stale.status_code == 409, stale.text
    patched = client.patch(path, json={"content": "current"}, headers={"If-Match": replaced.headers["etag"]})
    assert patched.status_code == 200, patched.text
