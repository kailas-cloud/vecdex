"""E2E tests for chunk-style ingest metadata and document-level retrieval."""

from concurrent.futures import ThreadPoolExecutor, as_completed

import httpx
import pytest

from conftest import (
    VECDEX_API_KEY,
    VECDEX_BASE_URL,
    search_with_retry,
)


pytestmark = pytest.mark.p0


def make_client() -> httpx.Client:
    return httpx.Client(
        base_url=VECDEX_BASE_URL,
        headers={"Authorization": f"Bearer {VECDEX_API_KEY}"},
        timeout=30.0,
    )


class TestChunkedDocumentSearch:
    def test_search_by_document_without_declared_system_fields(
        self, client, collection_factory
    ):
        coll = collection_factory()
        name = coll["name"]

        docs = [
            {
                "id": "paper-a-chunk-0001",
                "content": "scifact evidence chunk one with retrieval signal",
                "tags": {"parent_doc_id": "paper-a"},
                "numerics": {"chunk_index": 1},
            },
            {
                "id": "paper-a-chunk-0002",
                "content": "scifact evidence chunk two with retrieval signal",
                "tags": {"parent_doc_id": "paper-a"},
                "numerics": {"chunk_index": 2},
            },
            {
                "id": "paper-b-chunk-0001",
                "content": "another paper chunk unrelated to paper a",
                "tags": {"parent_doc_id": "paper-b"},
                "numerics": {"chunk_index": 1},
            },
        ]

        resp = client.post(
            f"/collections/{name}/documents/batch-upsert",
            json={"documents": docs},
        )
        assert resp.status_code == 200
        assert resp.json()["succeeded"] == 3

        resp = search_with_retry(
            client,
            name,
            query="scifact evidence retrieval",
            mode="keyword",
            top_k=10,
            limit=10,
            filters={"must": [{"key": "parent_doc_id", "match": "paper-a"}]},
        )
        items = resp.json()["items"]
        ids = {item["id"] for item in items}
        assert ids == {"paper-a-chunk-0001", "paper-a-chunk-0002"}
        for item in items:
            assert item["tags"]["parent_doc_id"] == "paper-a"

        resp = search_with_retry(
            client,
            name,
            query="scifact evidence retrieval",
            mode="keyword",
            top_k=10,
            limit=10,
            filters={
                "must": [
                    {"key": "parent_doc_id", "match": "paper-a"},
                    {"key": "chunk_index", "range": {"gte": 2}},
                ]
            },
        )
        items = resp.json()["items"]
        assert [item["id"] for item in items] == ["paper-a-chunk-0002"]
        assert items[0]["numerics"]["chunk_index"] == 2

    def test_parallel_batch_ingest_preserves_document_lookup(
        self, client, collection_factory
    ):
        coll = collection_factory()
        name = coll["name"]
        batches = 4
        batch_size = 25

        def batch_upsert(batch_no: int) -> tuple[int, dict]:
            c = make_client()
            try:
                start = batch_no * batch_size
                documents = []
                for offset in range(batch_size):
                    chunk_index = start + offset + 1
                    documents.append(
                        {
                            "id": f"paper-par-chunk-{chunk_index:04d}",
                            "content": (
                                f"parallel ingest scifact chunk {chunk_index} "
                                "with repeated retrieval phrase"
                            ),
                            "tags": {"parent_doc_id": "paper-par"},
                            "numerics": {"chunk_index": chunk_index},
                        }
                    )
                resp = c.post(
                    f"/collections/{name}/documents/batch-upsert",
                    json={"documents": documents},
                )
                return resp.status_code, resp.json()
            finally:
                c.close()

        with ThreadPoolExecutor(max_workers=batches) as pool:
            futures = [pool.submit(batch_upsert, i) for i in range(batches)]
            results = [f.result() for f in as_completed(futures)]

        assert all(status == 200 for status, _ in results)
        assert all(body["succeeded"] == batch_size for _, body in results)

        resp = search_with_retry(
            client,
            name,
            query="parallel ingest scifact chunk",
            mode="keyword",
            top_k=100,
            limit=100,
            filters={"must": [{"key": "parent_doc_id", "match": "paper-par"}]},
        )
        items = resp.json()["items"]
        assert len(items) == 100
        chunk_indexes = sorted(item["numerics"]["chunk_index"] for item in items)
        assert chunk_indexes == list(range(1, 101))
        assert all(item["tags"]["parent_doc_id"] == "paper-par" for item in items)
