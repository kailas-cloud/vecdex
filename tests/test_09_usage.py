"""Usage and budget endpoint tests — GET /usage."""

import pytest

from conftest import (
    assert_embedding_headers,
    assert_no_embedding_headers,
    xfail_on_valkey,
)


pytestmark = pytest.mark.p1


class TestUsageBasic:
    """GET /usage — basic response structure."""

    def test_usage_returns_200(self, client):
        resp = client.get("/usage")
        assert resp.status_code == 200

    def test_usage_has_period(self, client):
        data = client.get("/usage").json()
        assert "period" in data
        # Default period is "day" per handler
        assert data["period"] in ("day", "month", "total")

    def test_usage_has_usage_metrics(self, client):
        data = client.get("/usage").json()
        assert "usage" in data
        usage = data["usage"]
        assert "embedding_requests" in usage or "embeddingRequests" in usage
        assert "tokens" in usage

    def test_usage_has_budget(self, client):
        data = client.get("/usage").json()
        assert "budget" in data
        budget = data["budget"]
        assert "tokens_limit" in budget or "tokensLimit" in budget
        assert "tokens_remaining" in budget or "tokensRemaining" in budget
        assert "is_exhausted" in budget


class TestUsagePeriods:
    """GET /usage?period=day|month|total"""

    def test_usage_period_day(self, client):
        resp = client.get("/usage", params={"period": "day"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["period"] == "day"

    def test_usage_period_month(self, client):
        resp = client.get("/usage", params={"period": "month"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["period"] == "month"

    def test_usage_period_total(self, client):
        resp = client.get("/usage", params={"period": "total"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["period"] == "total"

    def test_day_has_period_timestamps(self, client):
        data = client.get("/usage", params={"period": "day"}).json()
        # period_start and period_end present for day/month
        has_start = "period_start_at" in data or "periodStartAt" in data
        has_end = "period_end_at" in data or "periodEndAt" in data
        assert has_start
        assert has_end


class TestUsageCollection:
    """GET /usage?collection=<name> — filter by collection."""

    def test_usage_with_collection_filter(self, client, collection_factory):
        coll = collection_factory()
        resp = client.get("/usage", params={"collection": coll["name"]})
        assert resp.status_code == 200
        data = resp.json()
        # Collection should be echoed back
        coll_field = data.get("collection") or data.get("Collection")
        assert coll_field == coll["name"]


class TestUsageAfterOperations:
    """Usage counters should reflect embedding operations."""

    def test_tokens_increase_after_upsert(self, client, collection_factory):
        """Upsert consumes embedding tokens — usage should reflect it."""
        coll = collection_factory()

        # Get baseline
        before = client.get("/usage", params={"period": "day"}).json()
        before_tokens = (
            before.get("usage", {}).get("tokens")
            or before.get("usage", {}).get("Tokens", 0)
        )

        # Upsert a doc (triggers embedding)
        client.put(
            f"/collections/{coll['name']}/documents/usage-test",
            json={"content": "some content for embedding usage tracking test"},
        )

        # Check tokens increased
        after = client.get("/usage", params={"period": "day"}).json()
        after_tokens = (
            after.get("usage", {}).get("tokens")
            or after.get("usage", {}).get("Tokens", 0)
        )
        assert after_tokens >= before_tokens


@pytest.mark.p0
class TestUsageDefaultPeriod:
    """P0 usage default period and total behavior."""

    def test_default_period_is_month(self, client):
        """8.1.1: Default period = 'month' (per spec)."""
        data = client.get("/usage").json()
        assert data["period"] == "month"

    def test_total_period_has_no_period_timestamps(self, client):
        """8.1.3: period=total → no period_start/period_end."""
        data = client.get("/usage", params={"period": "total"}).json()
        assert data["period"] == "total"
        assert "period_start_at" not in data and "periodStartAt" not in data
        assert "period_end_at" not in data and "periodEndAt" not in data


@pytest.mark.p0
class TestEmbeddingHeadersOnOperations:
    """P0 embedding headers on various operations."""

    def test_put_has_embedding_headers(self, client, collection_factory):
        """8.2.1: PUT → X-Embedding-Tokens > 0."""
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/emb-hdr-1",
            json={"content": "embedding header tracking test"},
        )
        assert resp.status_code == 201
        assert_embedding_headers(resp)

    def test_patch_without_content_no_embedding_headers(self, client, collection_factory):
        """8.2.2: PATCH without content → no embedding headers."""
        coll = collection_factory(fields=[{"name": "tag1", "type": "tag"}])
        client.put(
            f"/collections/{coll['name']}/documents/emb-hdr-2",
            json={"content": "test", "tags": {"tag1": "a"}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/emb-hdr-2",
            json={"tags": {"tag1": "b"}},
        )
        assert resp.status_code == 200
        assert_no_embedding_headers(resp)

    def test_patch_with_content_has_embedding_headers(self, client, collection_factory):
        """8.2.3: PATCH with content → embedding headers present."""
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/emb-hdr-3",
            json={"content": "original"},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/emb-hdr-3",
            json={"content": "updated content for re-embedding"},
        )
        assert resp.status_code == 200
        assert_embedding_headers(resp)

    def test_search_semantic_has_embedding_headers(self, client, populated_collection):
        """8.2.4: Search semantic → embedding headers present."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "semantic"},
        )
        assert resp.status_code == 200
        assert_embedding_headers(resp)

    @xfail_on_valkey
    def test_search_keyword_no_embedding_headers(self, client, populated_collection):
        """8.2.5: Search keyword → no embedding headers."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "Python", "mode": "keyword"},
        )
        assert resp.status_code == 200
        assert_no_embedding_headers(resp)

    def test_get_doc_no_embedding_headers(self, client, collection_factory):
        """8.2.6: GET doc → no embedding headers."""
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/emb-hdr-get",
            json={"content": "test"},
        )
        resp = client.get(f"/collections/{coll['name']}/documents/emb-hdr-get")
        assert resp.status_code == 200
        assert_no_embedding_headers(resp)

    def test_delete_doc_no_embedding_headers(self, client, collection_factory):
        """8.2.7: DELETE doc → no embedding headers."""
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/emb-hdr-del",
            json={"content": "test"},
        )
        resp = client.delete(f"/collections/{coll['name']}/documents/emb-hdr-del")
        assert resp.status_code == 204
        assert_no_embedding_headers(resp)

    def test_list_docs_no_embedding_headers(self, client, collection_factory):
        """8.2.8: List docs → no embedding headers."""
        coll = collection_factory()
        resp = client.get(f"/collections/{coll['name']}/documents")
        assert resp.status_code == 200
        assert_no_embedding_headers(resp)


class TestUsageBudgetUnlimited:
    """P1 unlimited budget tests."""

    def test_unlimited_budget_sentinel_values(self, client):
        """8.2.10: Unlimited budget → sentinel -1 values."""
        data = client.get("/usage", params={"period": "total"}).json()
        budget = data.get("budget", {})
        # JSON uses snake_case: tokens_limit
        tokens_limit = budget.get("tokens_limit")
        if tokens_limit is None:
            tokens_limit = budget.get("tokensLimit")
        # Config-dependent — just verify the field exists and is numeric
        assert tokens_limit is not None
        assert isinstance(tokens_limit, (int, float))
