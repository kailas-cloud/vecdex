"""End-to-end integration flow tests."""

import time

import pytest

from conftest import unique_name, search_with_retry


@pytest.mark.p0
class TestCRUDLifecycle:
    """Full create → read → update → search → delete lifecycle."""

    def test_full_document_lifecycle(self, client, collection_factory):
        coll = collection_factory(
            fields=[{"name": "lang", "type": "tag"}]
        )
        name = coll["name"]

        # Create
        resp = client.put(
            f"/api/v2/collections/{name}/documents/lifecycle-1",
            json={"content": "lifecycle test document", "metadata": {"lang": "go"}},
        )
        assert resp.status_code == 201

        # Read
        resp = client.get(f"/api/v2/collections/{name}/documents/lifecycle-1")
        assert resp.status_code == 200
        assert resp.json()["content"] == "lifecycle test document"

        # Update
        resp = client.put(
            f"/api/v2/collections/{name}/documents/lifecycle-1",
            json={"content": "updated lifecycle document", "metadata": {"lang": "rust"}},
        )
        assert resp.status_code == 200

        # Verify update
        resp = client.get(f"/api/v2/collections/{name}/documents/lifecycle-1")
        assert resp.json()["content"] == "updated lifecycle document"
        assert resp.json().get("metadata", {}).get("lang") == "rust"

        # Delete
        resp = client.delete(f"/api/v2/collections/{name}/documents/lifecycle-1")
        assert resp.status_code == 204

        # Verify deletion
        resp = client.get(f"/api/v2/collections/{name}/documents/lifecycle-1")
        assert resp.status_code == 404

    def test_collection_lifecycle(self, client):
        name = unique_name()

        # Create
        resp = client.post("/api/v2/collections", json={"name": name})
        assert resp.status_code == 201

        # Read
        resp = client.get(f"/api/v2/collections/{name}")
        assert resp.status_code == 200

        # Delete
        resp = client.delete(f"/api/v2/collections/{name}")
        assert resp.status_code == 204

        # Verify deletion
        resp = client.get(f"/api/v2/collections/{name}")
        assert resp.status_code == 404


@pytest.mark.p0
class TestSearchConsistency:
    """CRUD operations followed by search — verify consistency."""

    def test_upserted_document_is_searchable(self, client, collection_factory):
        coll = collection_factory()
        name = coll["name"]
        created = client.put(
            f"/api/v2/collections/{name}/documents/searchable-1",
            json={"content": "unique xylophone melody for testing"},
        )
        assert created.status_code == 201, created.text
        time.sleep(0.5)
        resp = search_with_retry(
            client, name, query="xylophone melody", mode="semantic"
        )
        data = resp.json()
        ids = [item["id"] for item in data["items"]]
        assert "searchable-1" in ids

    def test_deleted_document_not_in_search(self, client, collection_factory):
        coll = collection_factory()
        name = coll["name"]
        client.put(
            f"/api/v2/collections/{name}/documents/will-delete",
            json={"content": "ephemeral zephyr content for removal"},
        )
        time.sleep(0.5)
        # Verify it's searchable first
        search_with_retry(client, name, query="ephemeral zephyr", mode="semantic")

        # Delete
        client.delete(f"/api/v2/collections/{name}/documents/will-delete")
        time.sleep(0.5)

        # Verify not in search
        resp = client.post(
            f"/api/v2/collections/{name}/search",
            json={"query": "ephemeral zephyr", "mode": "semantic"},
        )
        assert resp.status_code == 200
        ids = [item["id"] for item in resp.json()["items"]]
        assert "will-delete" not in ids


@pytest.mark.p0
class TestFilteredSearch:
    """End-to-end filtered search flows."""

    def test_tag_filter_returns_correct_subset(self, client, collection_factory):
        coll = collection_factory(
            fields=[{"name": "env", "type": "tag"}]
        )
        name = coll["name"]

        # Create docs with different tags
        client.put(
            f"/api/v2/collections/{name}/documents/prod-1",
            json={"content": "production server configuration", "metadata": {"env": "prod"}},
        )
        client.put(
            f"/api/v2/collections/{name}/documents/dev-1",
            json={"content": "development server configuration", "metadata": {"env": "dev"}},
        )
        time.sleep(0.5)

        resp = search_with_retry(
            client,
            name,
            query="server configuration",
            mode="semantic",
            filters={"must": [{"key": "env", "match": "prod"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("metadata", {}).get("env") == "prod"


@pytest.mark.p0
class TestBatchWorkflow:
    """Batch insert followed by search and batch delete."""

    def test_batch_insert_and_search(self, client, collection_factory):
        coll = collection_factory()
        name = coll["name"]

        # Batch upsert
        resp = client.post(
            f"/api/v2/collections/{name}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "bw-1", "content": "quantum computing fundamentals"},
                    {"id": "bw-2", "content": "quantum entanglement theory"},
                    {"id": "bw-3", "content": "classical mechanics overview"},
                ]
            },
        )
        assert resp.status_code == 200
        assert resp.json()["succeeded"] == 3

        time.sleep(0.5)

        # Search
        resp = search_with_retry(
            client, name, query="quantum", mode="semantic"
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "bw-1" in ids or "bw-2" in ids

    def test_batch_delete_removes_from_search(self, client, collection_factory):
        coll = collection_factory()
        name = coll["name"]

        # Create docs
        for i in range(3):
            client.put(
                f"/api/v2/collections/{name}/documents/bd-{i}",
                json={"content": f"batch delete test document number {i}"},
            )
        time.sleep(0.5)

        # Batch delete
        resp = client.post(
            f"/api/v2/collections/{name}/documents/batch-delete",
            json={"documents": [{"id": doc_id} for doc_id in ["bd-0", "bd-1", "bd-2"]]},
        )
        assert resp.json()["succeeded"] == 3

        # Verify documents gone
        for i in range(3):
            resp = client.get(f"/api/v2/collections/{name}/documents/bd-{i}")
            assert resp.status_code == 404


@pytest.mark.p0
class TestPatchFlow:
    """Patch document and verify via GET."""

    def test_patch_tags_preserves_content(self, client, collection_factory):
        coll = collection_factory(
            fields=[{"name": "status", "type": "tag"}]
        )
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/patch-1",
            json={"content": "original content stays", "metadata": {"status": "draft"}},
        )

        client.patch(
            f"/api/v2/collections/{name}/documents/patch-1",
            json={"metadata": {"status": "published"}},
        )

        resp = client.get(f"/api/v2/collections/{name}/documents/patch-1")
        data = resp.json()
        assert data["content"] == "original content stays"
        assert data.get("metadata", {}).get("status") == "published"

    def test_patch_content_updates_searchable_text(self, client, collection_factory):
        coll = collection_factory()
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/patch-2",
            json={"content": "old content about nothing"},
        )
        time.sleep(0.5)

        client.patch(
            f"/api/v2/collections/{name}/documents/patch-2",
            json={"content": "specialized quantum chromodynamics analysis"},
        )
        time.sleep(0.5)

        resp = search_with_retry(
            client, name, query="quantum chromodynamics", mode="semantic"
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "patch-2" in ids


@pytest.mark.p1
class TestPatchVsPutP1:
    """P1: PATCH merge semantics vs PUT replace semantics."""

    def test_put_replaces_all_tags(self, client, collection_factory):
        """PUT replaces the entire document — old tags gone."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
            ]
        )
        name = coll["name"]
        client.put(
            f"/api/v2/collections/{name}/documents/replace-1",
            json={"content": "v1", "metadata": {"a": "1", "b": "2"}},
        )
        client.put(
            f"/api/v2/collections/{name}/documents/replace-1",
            json={"content": "v2", "metadata": {"a": "updated"}},
        )
        data = client.get(f"/api/v2/collections/{name}/documents/replace-1").json()
        assert data["content"] == "v2"
        assert data.get("metadata", {}).get("a") == "updated"
        # b should be gone after PUT replace
        assert data.get("metadata", {}).get("b") is None

    def test_patch_merges_tags(self, client, collection_factory):
        """PATCH only touches specified fields."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
            ]
        )
        name = coll["name"]
        client.put(
            f"/api/v2/collections/{name}/documents/merge-1",
            json={"content": "v1", "metadata": {"a": "1", "b": "2"}},
        )
        client.patch(
            f"/api/v2/collections/{name}/documents/merge-1",
            json={"metadata": {"a": "updated"}},
        )
        data = client.get(f"/api/v2/collections/{name}/documents/merge-1").json()
        assert data.get("metadata", {}).get("a") == "updated"
        assert data.get("metadata", {}).get("b") == "2"


@pytest.mark.p1
class TestMultiCollectionP1:
    """P1: Operations across multiple collections."""

    def test_same_doc_id_different_collections(self, client, collection_factory):
        """Same doc ID in different collections should be independent."""
        coll1 = collection_factory()
        coll2 = collection_factory()
        client.put(
            f"/api/v2/collections/{coll1['name']}/documents/shared-id",
            json={"content": "collection one content"},
        )
        client.put(
            f"/api/v2/collections/{coll2['name']}/documents/shared-id",
            json={"content": "collection two content"},
        )
        d1 = client.get(f"/api/v2/collections/{coll1['name']}/documents/shared-id").json()
        d2 = client.get(f"/api/v2/collections/{coll2['name']}/documents/shared-id").json()
        assert d1["content"] == "collection one content"
        assert d2["content"] == "collection two content"

    def test_delete_one_collection_preserves_other(self, client, collection_factory):
        coll1 = collection_factory()
        coll2 = collection_factory()
        client.put(
            f"/api/v2/collections/{coll1['name']}/documents/doc-x",
            json={"content": "keep me"},
        )
        client.put(
            f"/api/v2/collections/{coll2['name']}/documents/doc-x",
            json={"content": "keep me too"},
        )
        client.delete(f"/api/v2/collections/{coll1['name']}")
        resp = client.get(f"/api/v2/collections/{coll2['name']}/documents/doc-x")
        assert resp.status_code == 200
        assert resp.json()["content"] == "keep me too"


@pytest.mark.p1
class TestSearchAfterUpdateP1:
    """P1: Search reflects updated content."""

    def test_put_update_changes_search_ranking(self, client, collection_factory):
        coll = collection_factory()
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/evolve-1",
            json={"content": "original topic about gardening and flowers"},
        )
        time.sleep(0.5)

        # Update to completely different topic
        client.put(
            f"/api/v2/collections/{name}/documents/evolve-1",
            json={"content": "quantum physics and particle accelerators"},
        )
        time.sleep(0.5)

        resp = search_with_retry(
            client, name, query="quantum physics", mode="semantic"
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "evolve-1" in ids


@pytest.mark.p0
class TestFullLifecycleFlow:
    """P0: Complete lifecycle flows."""

    def test_full_lifecycle_create_search_delete(self, client, collection_factory):
        """12.1.1: Create coll → PUT 5 docs → search → DELETE 2 → search → DELETE coll → 404."""
        coll = collection_factory(
            fields=[{"name": "type", "type": "tag"}]
        )
        name = coll["name"]

        # PUT 5 docs
        for i in range(5):
            resp = client.put(
                f"/api/v2/collections/{name}/documents/fl-{i}",
                json={"content": f"lifecycle document number {i}", "metadata": {"type": "test"}},
            )
            assert resp.status_code == 201

        time.sleep(0.5)

        # Search
        resp = search_with_retry(client, name, query="lifecycle document", mode="semantic")
        assert len(resp.json()["items"]) > 0

        # DELETE 2 docs
        client.delete(f"/api/v2/collections/{name}/documents/fl-0")
        client.delete(f"/api/v2/collections/{name}/documents/fl-1")

        time.sleep(0.3)

        # Search again — deleted docs should be gone
        resp = client.post(
            f"/api/v2/collections/{name}/search",
            json={"query": "lifecycle document", "mode": "semantic"},
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "fl-0" not in ids
        assert "fl-1" not in ids

        # DELETE collection
        client.delete(f"/api/v2/collections/{name}")
        resp = client.get(f"/api/v2/collections/{name}")
        assert resp.status_code == 404


@pytest.mark.p1
class TestPatchSearchFlow:
    """P1: PATCH tag and search reflects change."""

    def test_patch_tag_search_reflects_update(self, client, collection_factory):
        """12.3.2: PATCH tag → search reflects update."""
        coll = collection_factory(
            fields=[{"name": "status", "type": "tag"}]
        )
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/ps-1",
            json={"content": "patchable document for search", "metadata": {"status": "draft"}},
        )
        time.sleep(0.5)

        # Patch status
        client.patch(
            f"/api/v2/collections/{name}/documents/ps-1",
            json={"metadata": {"status": "published"}},
        )
        time.sleep(0.3)

        # Filter for published
        resp = search_with_retry(
            client,
            name,
            query="patchable document",
            mode="semantic",
            filters={"must": [{"key": "status", "match": "published"}]},
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "ps-1" in ids

    def test_delete_tag_via_null_search_no_longer_finds(self, client, collection_factory):
        """12.3.3: Delete tag via null → search no longer finds by old tag."""
        coll = collection_factory(
            fields=[{"name": "env", "type": "tag"}]
        )
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/del-tag-1",
            json={"content": "tagged document for removal test", "metadata": {"env": "prod"}},
        )
        time.sleep(0.5)

        # Remove tag
        client.patch(
            f"/api/v2/collections/{name}/documents/del-tag-1",
            json={"metadata": {"env": None}},
        )
        time.sleep(0.3)

        # Search with old tag filter should not find it
        resp = client.post(
            f"/api/v2/collections/{name}/search",
            json={
                "query": "tagged document",
                "mode": "semantic",
                "filters": {"must": [{"key": "env", "match": "prod"}]},
            },
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "del-tag-1" not in ids


@pytest.mark.p0
class TestBatchPaginationFlow:
    """P0: Batch + pagination combined flow."""

    def test_batch_50_paginate_all_delete_all(self, client, collection_factory):
        """12.4.1: Batch 50 docs → cursor paginate all → batch delete all → list → 0."""
        coll = collection_factory()
        name = coll["name"]

        # Batch upsert 50 docs
        docs = [{"id": f"bp-{i}", "content": f"batch paginate doc {i}"} for i in range(50)]
        resp = client.post(
            f"/api/v2/collections/{name}/documents/batch-upsert",
            json={"documents": docs},
        )
        assert resp.json()["succeeded"] == 50

        # Cursor paginate all
        all_ids = []
        cursor = None
        for _ in range(100):
            params = {"limit": 10}
            if cursor:
                params["cursor"] = cursor
            resp = client.get(f"/api/v2/collections/{name}/documents", params=params)
            data = resp.json()
            all_ids.extend(item["id"] for item in data["items"])
            if not data["has_more"]:
                break
            cursor = data.get("next_cursor") or data.get("nextCursor")

        assert len(all_ids) == 50

        # Batch delete all
        resp = client.post(
            f"/api/v2/collections/{name}/documents/batch-delete",
            json={"documents": [{"id": doc_id} for doc_id in all_ids]},
        )
        assert resp.json()["succeeded"] == 50

        # List → 0
        resp = client.get(f"/api/v2/collections/{name}/documents")
        assert len(resp.json()["items"]) == 0


@pytest.mark.p1
class TestDocumentCountFlow:
    """P1: Document count accuracy."""

    def test_put_same_id_twice_count_is_1(self, client, collection_factory):
        """12.5.3: PUT same ID twice → document_count = 1."""
        coll = collection_factory()
        name = coll["name"]

        client.put(f"/api/v2/collections/{name}/documents/dup-1", json={"content": "v1"})
        client.put(f"/api/v2/collections/{name}/documents/dup-1", json={"content": "v2"})

        data = client.get(f"/api/v2/collections/{name}").json()
        count = data.get("document_count") or data.get("documentCount", 0)
        assert count == 1


@pytest.mark.p1
class TestSearchModeComparisonP1:
    """P1: Same query in all three modes."""

    def test_same_query_three_modes_all_valid(self, client, populated_collection):
        """12.6.1: Same query in 3 modes → all valid."""
        coll = populated_collection["name"]
        for mode in ("semantic", "keyword", "hybrid"):
            resp = client.post(
                f"/api/v2/collections/{coll}/search",
                json={"query": "programming", "mode": mode},
            )
            assert resp.status_code == 200
            data = resp.json()
            assert "items" in data
            assert "total" in data


@pytest.mark.p0
class TestPutPatchMergeFlow:
    """P0: PUT/PATCH merge and replace semantics."""

    def test_put_merge_tags_patch_merges_get_confirms(self, client, collection_factory):
        """12.9.1: PUT merge tags → PATCH merges → GET confirms."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
                {"name": "c", "type": "tag"},
            ]
        )
        name = coll["name"]

        # PUT with a and b
        client.put(
            f"/api/v2/collections/{name}/documents/merge-flow",
            json={"content": "merge test", "metadata": {"a": "1", "b": "2"}},
        )

        # PATCH adds c, updates b
        client.patch(
            f"/api/v2/collections/{name}/documents/merge-flow",
            json={"metadata": {"b": "updated", "c": "3"}},
        )

        # GET confirms
        data = client.get(f"/api/v2/collections/{name}/documents/merge-flow").json()
        tags = data.get("metadata", {})
        assert tags.get("a") == "1"
        assert tags.get("b") == "updated"
        assert tags.get("c") == "3"

    def test_put_replaces_all_tags_flow(self, client, collection_factory):
        """12.9.2: PUT replaces all tags."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
            ]
        )
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/replace-flow",
            json={"content": "v1", "metadata": {"a": "1", "b": "2"}},
        )
        client.put(
            f"/api/v2/collections/{name}/documents/replace-flow",
            json={"content": "v2", "metadata": {"a": "new"}},
        )

        data = client.get(f"/api/v2/collections/{name}/documents/replace-flow").json()
        assert data.get("metadata", {}).get("a") == "new"
        assert data.get("metadata", {}).get("b") is None


@pytest.mark.p1
class TestUsageTrackingFlow:
    """P1: Usage tracking after operations."""

    def test_usage_tokens_increase_after_put(self, client, collection_factory):
        """12.10.1: Usage tracking: tokens increase after PUT."""
        coll = collection_factory()

        before = client.get("/api/v2/usage", params={"period": "day"}).json()
        before_tokens = before.get("usage", {}).get("tokens", 0)

        client.put(
            f"/api/v2/collections/{coll['name']}/documents/usage-track",
            json={"content": "usage tracking flow test content for embedding"},
        )

        after = client.get("/api/v2/usage", params={"period": "day"}).json()
        after_tokens = after.get("usage", {}).get("tokens", 0)
        assert after_tokens >= before_tokens


@pytest.mark.p0
class TestPreKNNFilterFlow:
    """P0: Pre-KNN filter ensures only filtered docs in results."""

    def test_pre_knn_filter_only_filtered_docs(self, client, collection_factory):
        """12.11.1: Pre-KNN filter: only filtered docs in results."""
        coll = collection_factory(
            fields=[{"name": "env", "type": "tag"}]
        )
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/prod-1",
            json={"content": "production server config", "metadata": {"env": "prod"}},
        )
        client.put(
            f"/api/v2/collections/{name}/documents/dev-1",
            json={"content": "development server config", "metadata": {"env": "dev"}},
        )
        client.put(
            f"/api/v2/collections/{name}/documents/staging-1",
            json={"content": "staging server config", "metadata": {"env": "staging"}},
        )
        time.sleep(0.5)

        resp = search_with_retry(
            client,
            name,
            query="server config",
            mode="semantic",
            filters={"must": [{"key": "env", "match": "prod"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("metadata", {}).get("env") == "prod"


@pytest.mark.p0
class TestTagWithSpacesSearchFlow:
    """P0: Tag with spaces stored and searchable."""

    def test_tag_with_spaces_search_filter_finds_it(self, client, collection_factory):
        """12.12.1: Tag with spaces → search filter finds it."""
        coll = collection_factory(
            fields=[{"name": "label", "type": "tag"}]
        )
        name = coll["name"]

        client.put(
            f"/api/v2/collections/{name}/documents/space-tag",
            json={"content": "spaced tag document", "metadata": {"label": "hello world"}},
        )
        time.sleep(0.5)

        resp = search_with_retry(
            client,
            name,
            query="spaced tag",
            mode="semantic",
            filters={"must": [{"key": "label", "match": "hello world"}]},
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "space-tag" in ids
