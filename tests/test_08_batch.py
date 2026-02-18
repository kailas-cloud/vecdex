"""Batch upsert and delete tests."""

import pytest

from conftest import unique_name


@pytest.mark.p0
class TestBatchUpsert:
    """POST /collections/{collection}/documents/batch-upsert"""

    def test_batch_upsert_returns_200(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "batch-1", "content": "first doc"},
                    {"id": "batch-2", "content": "second doc"},
                    {"id": "batch-3", "content": "third doc"},
                ]
            },
        )
        assert resp.status_code == 200

    def test_batch_upsert_reports_succeeded(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "batch-1", "content": "first doc"},
                    {"id": "batch-2", "content": "second doc"},
                ]
            },
        )
        data = resp.json()
        assert data["succeeded"] == 2
        assert data["failed"] == 0

    def test_batch_upsert_items_have_status(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "batch-1", "content": "first doc"},
                ]
            },
        )
        data = resp.json()
        assert len(data["items"]) == 1
        assert data["items"][0]["id"] == "batch-1"
        assert data["items"][0]["status"] == "ok"

    def test_batch_upsert_documents_retrievable(self, client, collection_factory):
        coll = collection_factory()
        client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "batch-1", "content": "retrievable content"},
                ]
            },
        )
        resp = client.get(f"/collections/{coll['name']}/documents/batch-1")
        assert resp.status_code == 200
        assert resp.json()["content"] == "retrievable content"

    def test_batch_upsert_nonexistent_collection(self, client):
        """Batch against nonexistent collection returns 200 with per-item errors."""
        resp = client.post(
            "/collections/nonexistent-xyz/documents/batch-upsert",
            json={
                "documents": [{"id": "doc-1", "content": "test"}],
            },
        )
        # Batch endpoints always return 200 with per-item status
        assert resp.status_code in (200, 404)
        if resp.status_code == 200:
            data = resp.json()
            assert data["failed"] >= 1


@pytest.mark.p0
class TestBatchDelete:
    """POST /collections/{collection}/documents/batch-delete"""

    def test_batch_delete_returns_200(self, client, collection_factory):
        coll = collection_factory()
        # Create docs first
        for i in range(3):
            client.put(
                f"/collections/{coll['name']}/documents/del-{i}",
                json={"content": f"doc {i}"},
            )
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ["del-0", "del-1", "del-2"]},
        )
        assert resp.status_code == 200

    def test_batch_delete_reports_succeeded(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/del-1",
            json={"content": "to delete"},
        )
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ["del-1"]},
        )
        data = resp.json()
        assert data["succeeded"] == 1

    def test_batch_delete_nonexistent_doc_reports_failure(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ["nonexistent-doc"]},
        )
        data = resp.json()
        assert data["failed"] >= 1


@pytest.mark.p1
class TestBatchUpsertP1:
    """P1 batch upsert edge cases."""

    def test_batch_upsert_with_tags(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "lang", "type": "tag"}])
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "bt-1", "content": "python code", "tags": {"lang": "python"}},
                    {"id": "bt-2", "content": "go code", "tags": {"lang": "go"}},
                ]
            },
        )
        data = resp.json()
        assert data["succeeded"] == 2
        # Verify tags persisted
        doc = client.get(f"/collections/{coll['name']}/documents/bt-1").json()
        assert doc.get("tags", {}).get("lang") == "python"

    def test_batch_upsert_updates_existing(self, client, collection_factory):
        """Batch upsert should update existing docs."""
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/existing",
            json={"content": "original"},
        )
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={
                "documents": [
                    {"id": "existing", "content": "updated via batch"},
                ]
            },
        )
        assert resp.json()["succeeded"] == 1
        doc = client.get(f"/collections/{coll['name']}/documents/existing").json()
        assert doc["content"] == "updated via batch"

    def test_batch_upsert_single_doc(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={"documents": [{"id": "single", "content": "only one"}]},
        )
        assert resp.json()["succeeded"] == 1

    def test_batch_upsert_malformed_json_returns_400(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            content=b"not json",
            headers={"content-type": "application/json"},
        )
        assert resp.status_code == 400


@pytest.mark.p1
class TestBatchDeleteP1:
    """P1 batch delete edge cases."""

    def test_batch_delete_mix_existing_and_nonexistent(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/exists-1",
            json={"content": "real doc"},
        )
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ["exists-1", "ghost-1"]},
        )
        data = resp.json()
        assert data["succeeded"] >= 1
        assert data["failed"] >= 1

    def test_batch_delete_already_deleted(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/del-twice",
            json={"content": "delete me"},
        )
        client.delete(f"/collections/{coll['name']}/documents/del-twice")
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ["del-twice"]},
        )
        data = resp.json()
        assert data["failed"] >= 1


@pytest.mark.p0
class TestBatchUpsertLimits:
    """P0 batch upsert boundary tests."""

    def test_batch_upsert_100_docs_max(self, client, collection_factory):
        """7.1.5: 100 docs (max) → 200."""
        coll = collection_factory()
        docs = [
            {"id": f"max-{i}", "content": f"doc number {i}"}
            for i in range(100)
        ]
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={"documents": docs},
        )
        assert resp.status_code == 200
        assert resp.json()["succeeded"] == 100


@pytest.mark.p1
class TestBatchUpsertBoundaryP1:
    """P1 batch boundary edge cases."""

    def test_batch_upsert_over_limit_returns_400(self, client, collection_factory):
        """7.1.6: docs exceeding max_batch_size (5000) → 400."""
        coll = collection_factory()
        docs = [
            {"id": f"over-{i}", "content": f"doc {i}"}
            for i in range(5001)
        ]
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={"documents": docs},
        )
        assert resp.status_code == 400

    def test_batch_upsert_0_docs_returns_400(self, client, collection_factory):
        """7.1.8: 0 docs → 400."""
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={"documents": []},
        )
        assert resp.status_code == 400


@pytest.mark.p1
class TestBatchDeleteBoundaryP1:
    """P1 batch delete boundary edge cases."""

    def test_batch_delete_100_ids_max(self, client, collection_factory):
        """7.2.3: 100 IDs (max) → 200."""
        coll = collection_factory()
        # Create 100 docs
        docs = [
            {"id": f"bd100-{i}", "content": f"doc {i}"}
            for i in range(100)
        ]
        client.post(
            f"/collections/{coll['name']}/documents/batch-upsert",
            json={"documents": docs},
        )
        ids = [f"bd100-{i}" for i in range(100)]
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ids},
        )
        assert resp.status_code == 200

    def test_batch_delete_over_limit_returns_400(self, client, collection_factory):
        """7.2.4: IDs exceeding max_batch_size (5000) → 400."""
        coll = collection_factory()
        ids = [f"over-{i}" for i in range(5001)]
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": ids},
        )
        assert resp.status_code == 400

    def test_batch_delete_empty_ids_returns_400(self, client, collection_factory):
        """7.2.7: Empty IDs array → 400."""
        coll = collection_factory()
        resp = client.post(
            f"/collections/{coll['name']}/documents/batch-delete",
            json={"ids": []},
        )
        assert resp.status_code == 400
