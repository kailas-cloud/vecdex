"""Similar documents endpoint tests — find-by-ID, text collections only."""

import time

import pytest

from conftest import (
    assert_no_embedding_headers,
)


def similar(client, collection, doc_id, **kwargs):
    """Helper: POST /collections/{collection}/documents/{id}/similar."""
    return client.post(
        f"/collections/{collection}/documents/{doc_id}/similar",
        json=kwargs if kwargs else {},
    )


def similar_with_retry(client, collection, doc_id, retries=5, **kwargs):
    """Similar with retry for indexing lag."""
    for attempt in range(retries):
        resp = similar(client, collection, doc_id, **kwargs)
        if resp.status_code == 200 and len(resp.json().get("items", [])) > 0:
            return resp
        time.sleep(0.3)
    return resp


# ============================================================
# P0 — Core: text collections
# ============================================================


@pytest.mark.p0
class TestSimilarTextBasic:
    """POST /collections/{collection}/documents/{id}/similar on text collections."""

    def test_returns_200(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        assert resp.status_code == 200

    def test_returns_results(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        data = resp.json()
        assert len(data["items"]) > 0

    def test_source_document_excluded(self, client, populated_collection):
        """The source document itself must NOT appear in results."""
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        ids = [item["id"] for item in resp.json()["items"]]
        assert "doc-1" not in ids

    def test_scores_between_0_and_1(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        for item in resp.json()["items"]:
            assert 0 <= item["score"] <= 1

    def test_scores_sorted_descending(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        scores = [item["score"] for item in resp.json()["items"]]
        assert scores == sorted(scores, reverse=True)

    def test_results_have_content_and_id(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        for item in resp.json()["items"]:
            assert "id" in item
            assert "content" in item

    def test_response_has_total_and_limit(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        data = resp.json()
        assert "total" in data
        assert "limit" in data

    def test_no_embedding_headers(self, client, populated_collection):
        """Similar uses stored vector — zero embedding cost."""
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        assert_no_embedding_headers(resp)


# ============================================================
# P0 — Error contract
# ============================================================


@pytest.mark.p0
class TestSimilarErrors:
    """Error responses for similar endpoint."""

    def test_nonexistent_document_returns_404(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar(client, coll, "nonexistent-xyz")
        assert resp.status_code == 404
        assert resp.json()["code"] == "document_not_found"

    def test_nonexistent_collection_returns_404(self, client):
        resp = similar(client, "nonexistent-xyz", "doc-1")
        assert resp.status_code == 404
        assert resp.json()["code"] == "collection_not_found"

    def test_error_has_code_and_message(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar(client, coll, "nonexistent-xyz")
        data = resp.json()
        assert "code" in data
        assert "message" in data

    def test_geo_collection_returns_400(self, client, populated_geo_collection):
        """Similar is not supported on geo collections."""
        coll = populated_geo_collection["name"]
        resp = similar(client, coll, "times-square")
        assert resp.status_code == 400
        assert resp.json()["code"] == "collection_type_mismatch"


# ============================================================
# P1 — Parameters
# ============================================================


@pytest.mark.p1
class TestSimilarParams:
    """top_k, limit, min_score, include_vectors, filters."""

    def test_limit_caps_results(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1", limit=2)
        assert len(resp.json()["items"]) <= 2

    def test_top_k_controls_window(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1", top_k=2)
        assert len(resp.json()["items"]) <= 2

    def test_min_score_filters_low_scores(self, client, populated_collection):
        """min_score=0.9 should exclude distant documents."""
        coll = populated_collection["name"]
        resp = similar(client, coll, "doc-1", min_score=0.9)
        assert resp.status_code == 200
        for item in resp.json()["items"]:
            assert item["score"] >= 0.9

    def test_include_vectors(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1", include_vectors=True)
        for item in resp.json()["items"]:
            assert "vector" in item
            assert isinstance(item["vector"], list)
            assert len(item["vector"]) == 1024

    def test_without_include_vectors_no_vector_field(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        for item in resp.json()["items"]:
            assert "vector" not in item

    def test_empty_body_uses_defaults(self, client, populated_collection):
        """Empty body → default top_k, limit, no filters."""
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        assert resp.status_code == 200

    def test_filter_by_tag(self, client, populated_collection):
        """Filter similar results by category tag."""
        coll = populated_collection["name"]
        resp = similar(
            client, coll, "doc-1",
            filters={"must": [{"key": "category", "match": "infrastructure"}]},
            top_k=10,
        )
        assert resp.status_code == 200
        for item in resp.json()["items"]:
            assert item["tags"]["category"] == "infrastructure"

    def test_filter_must_not(self, client, populated_collection):
        """Exclude category=programming from similar results."""
        coll = populated_collection["name"]
        resp = similar(
            client, coll, "doc-1",
            filters={"must_not": [{"key": "category", "match": "programming"}]},
            top_k=10,
        )
        assert resp.status_code == 200
        for item in resp.json()["items"]:
            assert item["tags"].get("category") != "programming"


# ============================================================
# P1 — Param validation
# ============================================================


@pytest.mark.p1
class TestSimilarParamValidation:
    """Validation for similar request parameters."""

    def test_top_k_0_returns_400(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar(client, coll, "doc-1", top_k=0)
        assert resp.status_code == 400

    def test_top_k_501_returns_400(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar(client, coll, "doc-1", top_k=501)
        assert resp.status_code == 400

    def test_limit_0_returns_400(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar(client, coll, "doc-1", limit=0)
        assert resp.status_code == 400

    def test_limit_101_returns_400(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = similar(client, coll, "doc-1", limit=101)
        assert resp.status_code == 400


# ============================================================
# P1 — Edge cases
# ============================================================


@pytest.mark.p1
class TestSimilarEdgeCases:
    """Edge cases and invariants."""

    def test_single_doc_collection_returns_empty(
        self, client, collection_factory
    ):
        """Collection with 1 document → similar returns empty (source excluded)."""
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/only-doc",
            json={"content": "the only document in the collection"},
        )
        time.sleep(0.5)
        resp = similar(client, coll["name"], "only-doc")
        assert resp.status_code == 200
        assert len(resp.json()["items"]) == 0

    def test_deterministic_results(self, client, populated_collection):
        """Same source doc → same similar results (deterministic embedder)."""
        coll = populated_collection["name"]
        r1 = similar_with_retry(client, coll, "doc-1")
        r2 = similar_with_retry(client, coll, "doc-1")
        ids1 = [i["id"] for i in r1.json()["items"]]
        ids2 = [i["id"] for i in r2.json()["items"]]
        assert ids1 == ids2

    def test_different_source_docs_different_order(self, client, populated_collection):
        """Different source documents should produce different ordering."""
        coll = populated_collection["name"]
        r1 = similar_with_retry(client, coll, "doc-1")  # Python programming
        r3 = similar_with_retry(client, coll, "doc-3")  # Kubernetes
        ids1 = [i["id"] for i in r1.json()["items"]]
        ids3 = [i["id"] for i in r3.json()["items"]]
        # At minimum, top result should differ (programming vs infra)
        assert ids1[0] != ids3[0]

    def test_results_have_tags_and_numerics(self, client, populated_collection):
        """Similar results include metadata fields."""
        coll = populated_collection["name"]
        resp = similar_with_retry(client, coll, "doc-1")
        items = resp.json()["items"]
        has_tags = any(item.get("tags") for item in items)
        assert has_tags
