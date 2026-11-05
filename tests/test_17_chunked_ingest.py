"""Former chunk metadata names are ordinary explicitly declared indexes."""

from concurrent.futures import ThreadPoolExecutor, as_completed

import httpx
import pytest

from conftest import (
    VECDEX_API_KEY,
    VECDEX_BASE_URL,
    search_with_retry,
)


pytestmark = pytest.mark.p0


def test_undeclared_chunk_metadata_has_no_index_or_grouping_behavior(client, collection_factory):
    name = collection_factory()["name"]
    metadata = {"parent_doc_id": {"arbitrary": [True, None]}, "chunk_index": [1, 2]}
    response = client.post(f"/api/v2/collections/{name}/documents/batch-upsert", json={"documents": [
        {"id": "source-a", "content": "SciFact ordinary metadata source", "metadata": metadata},
        {"id": "source-b", "content": "SciFact ordinary metadata source", "metadata": metadata},
    ]})
    assert response.status_code == 200 and response.json()["succeeded"] == 2, response.text
    found = search_with_retry(client, name, query="SciFact", mode="keyword", limit=10)
    assert {item["id"] for item in found.json()["items"]} == {"source-a", "source-b"}
    assert all(item["metadata"] == metadata for item in found.json()["items"])
    rejected = client.post(f"/api/v2/collections/{name}/search", json={
        "query": "SciFact", "filters": {"must": [{"key": "parent_doc_id", "match": "anything"}]},
    })
    assert rejected.status_code == 400 and rejected.json()["code"] == "validation_failed"


def make_client() -> httpx.Client:
    return httpx.Client(
        base_url=VECDEX_BASE_URL,
        headers={"Authorization": f"Bearer {VECDEX_API_KEY}"},
        timeout=30.0,
    )


class TestChunkedDocumentSearch:
    def test_group_metadata_keeps_independent_document_ids(
        self, client, collection_factory
    ):
        coll = collection_factory(fields=[
            {"name": "parent_doc_id", "type": "tag"},
            {"name": "chunk_index", "type": "numeric"},
        ])
        name = coll["name"]

        docs = [
            {
                "id": "paper-a-chunk-0001",
                "content": "scifact evidence chunk one with retrieval signal",
                "metadata": {"parent_doc_id": "paper-a", "chunk_index": 1},
            },
            {
                "id": "paper-a-chunk-0002",
                "content": "scifact evidence chunk two with retrieval signal",
                "metadata": {"parent_doc_id": "paper-a", "chunk_index": 2},
            },
            {
                "id": "paper-b-chunk-0001",
                "content": "another paper chunk unrelated to paper a",
                "metadata": {"parent_doc_id": "paper-b", "chunk_index": 1},
            },
        ]

        resp = client.post(
            f"/api/v2/collections/{name}/documents/batch-upsert",
            json={"documents": docs},
        )
        assert resp.status_code == 200
        assert resp.json()["succeeded"] == 3

        resp = search_with_retry(
            client,
            name,
            query="scifact evidence retrieval",
            mode="keyword",
            limit=10,
            filters={"must": [{"key": "parent_doc_id", "match": "paper-a"}]},
        )
        items = resp.json()["items"]
        assert {item["id"] for item in items} == {"paper-a-chunk-0001", "paper-a-chunk-0002"}
        assert items[0]["metadata"]["parent_doc_id"] == "paper-a"
        assert items[0]["metadata"]["chunk_index"] >= 1

        resp = search_with_retry(
            client,
            name,
            query="scifact evidence retrieval",
            mode="keyword",
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
        assert items[0]["metadata"]["chunk_index"] == 2

    def test_parallel_batch_ingest_preserves_document_lookup(
        self, client, collection_factory
    ):
        coll = collection_factory(fields=[
            {"name": "parent_doc_id", "type": "tag"},
            {"name": "chunk_index", "type": "numeric"},
        ])
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
                            "metadata": {"parent_doc_id": "paper-par", "chunk_index": chunk_index},
                        }
                    )
                resp = c.post(
                    f"/api/v2/collections/{name}/documents/batch-upsert",
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
            limit=100,
            filters={"must": [{"key": "parent_doc_id", "match": "paper-par"}]},
        )
        items = resp.json()["items"]
        assert len(items) == batches * batch_size
        assert len({item["id"] for item in items}) == batches * batch_size
        assert items[0]["metadata"]["parent_doc_id"] == "paper-par"
        assert items[0]["metadata"]["chunk_index"] >= 1

        for item in items:
            source = client.get(f"/api/v2/collections/{name}/documents/{item['id']}")
            assert source.status_code == 200
            assert source.json()["version"] == item["version"]
            assert source.json()["content"] == item["snippet"]
