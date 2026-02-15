"""Search endpoint tests — three modes, scores, limits."""

import time

import pytest

from conftest import (
    search_with_retry,
    assert_embedding_headers,
    assert_no_embedding_headers,
    xfail_on_valkey,
)


@pytest.mark.p0
class TestSearchSemantic:
    """Semantic (vector KNN) search mode."""

    def test_semantic_search_returns_results(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming language", mode="semantic"
        )
        data = resp.json()
        assert len(data["items"]) > 0

    def test_semantic_scores_between_0_and_1(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming language", mode="semantic"
        )
        for item in resp.json()["items"]:
            assert 0 <= item["score"] <= 1

    def test_semantic_returns_content(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming language", mode="semantic"
        )
        for item in resp.json()["items"]:
            assert "content" in item
            assert "id" in item


@pytest.mark.p0
@xfail_on_valkey
class TestSearchKeyword:
    """BM25 keyword search mode."""

    def test_keyword_search_returns_results(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="Python", mode="keyword"
        )
        data = resp.json()
        assert len(data["items"]) > 0

    def test_keyword_finds_exact_term(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="Kubernetes", mode="keyword"
        )
        data = resp.json()
        contents = [item["content"] for item in data["items"]]
        assert any("Kubernetes" in c for c in contents)


@pytest.mark.p0
@xfail_on_valkey
class TestSearchHybrid:
    """Hybrid (default) search mode."""

    def test_hybrid_is_default_mode(self, client, populated_collection):
        coll = populated_collection["name"]
        # No mode specified — should default to hybrid
        resp = search_with_retry(client, coll, query="containerized applications")
        assert resp.status_code == 200
        assert len(resp.json()["items"]) > 0

    def test_hybrid_scores_between_0_and_1(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(client, coll, query="container", mode="hybrid")
        for item in resp.json()["items"]:
            assert 0 <= item["score"] <= 1


@pytest.mark.p0
class TestSearchParams:
    """top_k, limit, min_score, include_vectors."""

    def test_limit_caps_results(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming", mode="semantic", limit=2
        )
        data = resp.json()
        assert len(data["items"]) <= 2

    def test_top_k_controls_window(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming", mode="semantic", top_k=3
        )
        data = resp.json()
        assert len(data["items"]) <= 3

    def test_include_vectors_returns_vectors(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="programming",
            mode="semantic",
            include_vectors=True,
        )
        data = resp.json()
        assert len(data["items"]) > 0
        for item in data["items"]:
            assert "vector" in item
            assert isinstance(item["vector"], list)
            assert len(item["vector"]) > 0

    def test_response_has_total_and_limit(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(client, coll, query="programming", mode="semantic")
        data = resp.json()
        assert "total" in data
        assert "limit" in data

    def test_search_nonexistent_collection_returns_404(self, client):
        resp = client.post(
            "/collections/nonexistent-xyz/documents/search",
            json={"query": "test"},
        )
        assert resp.status_code == 404

    def test_search_empty_query_returns_400(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": ""},
        )
        assert resp.status_code == 400


@pytest.mark.p1
class TestSearchSemanticP1:
    """P1 semantic search edge cases."""

    def test_scores_sorted_descending(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming language", mode="semantic"
        )
        items = resp.json()["items"]
        scores = [item["score"] for item in items]
        assert scores == sorted(scores, reverse=True)

    def test_same_query_returns_same_results(self, client, populated_collection):
        """Deterministic mock embedder → stable results."""
        coll = populated_collection["name"]
        r1 = search_with_retry(
            client, coll, query="programming language", mode="semantic"
        )
        r2 = search_with_retry(
            client, coll, query="programming language", mode="semantic"
        )
        ids1 = [i["id"] for i in r1.json()["items"]]
        ids2 = [i["id"] for i in r2.json()["items"]]
        assert ids1 == ids2

    def test_vectors_have_correct_dimensions(self, client, populated_collection):
        """Vectors should have 1024 dimensions (per docker.yaml config)."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="test",
            mode="semantic",
            include_vectors=True,
        )
        for item in resp.json()["items"]:
            assert len(item["vector"]) == 1024

    def test_without_include_vectors_no_vector_field(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="test", mode="semantic"
        )
        for item in resp.json()["items"]:
            assert "vector" not in item


@pytest.mark.p1
@xfail_on_valkey
class TestSearchKeywordP1:
    """P1 keyword search edge cases."""

    def test_keyword_no_match_returns_empty(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "xyzzyplughtwisty", "mode": "keyword"},
        )
        assert resp.status_code == 200
        data = resp.json()
        assert len(data["items"]) == 0

    def test_keyword_scores_positive(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="Python", mode="keyword"
        )
        for item in resp.json()["items"]:
            assert item["score"] > 0


@pytest.mark.p0
@xfail_on_valkey
class TestSearchHybridScoresAndHeaders:
    """P0 hybrid search: scores and embedding headers."""

    def test_hybrid_scores_in_range(self, client, populated_collection):
        """5.1.3: Hybrid scores in [0.0, 1.0]."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming language", mode="hybrid"
        )
        for item in resp.json()["items"]:
            assert 0.0 <= item["score"] <= 1.0

    def test_hybrid_search_has_embedding_headers(self, client, populated_collection):
        """5.1.4: Hybrid search includes embedding headers."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming", mode="hybrid"
        )
        assert_embedding_headers(resp)

    def test_hybrid_results_sorted_by_score(self, client, populated_collection):
        """5.1.5: Results sorted by score descending (GR-3)."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming language", mode="hybrid"
        )
        items = resp.json()["items"]
        scores = [item["score"] for item in items]
        assert scores == sorted(scores, reverse=True)


@pytest.mark.p0
@xfail_on_valkey
class TestSearchKeywordHeaders:
    """P0 keyword search: embedding headers absent."""

    def test_keyword_search_no_embedding_headers(self, client, populated_collection):
        """5.3.2: Keyword mode — embedding headers ABSENT."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="Python", mode="keyword"
        )
        assert_no_embedding_headers(resp)


@pytest.mark.p0
class TestSearchTopKLimit:
    """P0 top_k and limit interaction."""

    def test_top_k_50_limit_5(self, client, populated_collection):
        """5.4.1: top_k=50, limit=5 → ≤ 5 results."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming", mode="semantic",
            top_k=50, limit=5,
        )
        assert len(resp.json()["items"]) <= 5


@pytest.mark.p1
class TestSearchParamValidationP1:
    """P1 search parameter validation."""

    def test_top_k_501_returns_400(self, client, populated_collection):
        """5.4.5: top_k=501 → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "semantic", "top_k": 501},
        )
        assert resp.status_code == 400

    def test_top_k_0_returns_400(self, client, populated_collection):
        """5.4.6: top_k=0 → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "semantic", "top_k": 0},
        )
        assert resp.status_code == 400

    def test_limit_0_returns_400(self, client, populated_collection):
        """5.4.7: limit=0 → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "semantic", "limit": 0},
        )
        assert resp.status_code == 400

    def test_limit_101_returns_400(self, client, populated_collection):
        """5.4.8: limit=101 → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "semantic", "limit": 101},
        )
        assert resp.status_code == 400

    def test_invalid_mode_returns_400(self, client, populated_collection):
        """5.7.4: Invalid mode 'full_text' → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "full_text"},
        )
        assert resp.status_code == 400


@pytest.mark.p0
class TestSearchMinScore:
    """P0 min_score filter."""

    def test_min_score_filters_low_scores(self, client, populated_collection):
        """5.5.1: min_score=0.8 → all returned scores ≥ 0.8."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "programming language", "mode": "semantic", "min_score": 0.8},
        )
        assert resp.status_code == 200
        for item in resp.json()["items"]:
            assert item["score"] >= 0.8


@pytest.mark.p1
@xfail_on_valkey
class TestSearchHybridP1:
    """P1 hybrid search edge cases."""

    def test_hybrid_returns_tags_and_numerics(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="programming", mode="hybrid"
        )
        items = resp.json()["items"]
        # At least some items should have tags from populated_collection
        has_tags = any(item.get("tags") for item in items)
        assert has_tags


@pytest.mark.p1
class TestSearchMalformedBody:
    """P1 malformed search body (backend-agnostic)."""

    def test_malformed_search_body_returns_400(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            content=b"not json",
            headers={"content-type": "application/json"},
        )
        assert resp.status_code == 400
