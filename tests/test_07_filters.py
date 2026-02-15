"""Filter expression tests — must/should/must_not, range, tags."""

import time

import pytest

from conftest import search_with_retry, xfail_on_valkey


@pytest.mark.p0
class TestFilterMust:
    """must (AND) filter conditions."""

    def test_must_tag_filter(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": [{"key": "category", "match": "programming"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") == "programming"

    def test_must_numeric_range_filter(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": [{"key": "priority", "range": {"gte": 9}}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("numerics", {}).get("priority", 0) >= 9


@pytest.mark.p0
class TestFilterMustNot:
    """must_not (NOT) filter conditions."""

    def test_must_not_excludes_tag(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must_not": [{"key": "category", "match": "database"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") != "database"


@pytest.mark.p0
class TestFilterShould:
    """should (OR) filter conditions."""

    def test_should_matches_any(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={
                "should": [
                    {"key": "category", "match": "programming"},
                    {"key": "category", "match": "database"},
                ]
            },
        )
        data = resp.json()
        for item in data["items"]:
            cat = item.get("tags", {}).get("category")
            assert cat in ("programming", "database")


@pytest.mark.p0
class TestFilterCombined:
    """Combined filter expressions."""

    def test_must_and_must_not_combined(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={
                "must": [{"key": "priority", "range": {"gte": 7}}],
                "must_not": [{"key": "category", "match": "database"}],
            },
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("numerics", {}).get("priority", 0) >= 7
            assert item.get("tags", {}).get("category") != "database"

    def test_empty_filter_is_noop(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client, coll, query="technology", mode="semantic", filters={}
        )
        assert resp.status_code == 200
        assert len(resp.json()["items"]) > 0


@pytest.mark.p0
class TestFilterRange:
    """Numeric range filter variants."""

    def test_range_lt(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": [{"key": "priority", "range": {"lt": 8}}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("numerics", {}).get("priority", 0) < 8

    def test_range_gte_and_lte(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": [{"key": "priority", "range": {"gte": 7, "lte": 9}}]},
        )
        data = resp.json()
        for item in data["items"]:
            p = item.get("numerics", {}).get("priority", 0)
            assert 7 <= p <= 9


@pytest.mark.p1
class TestFilterP1:
    """P1 filter edge cases."""

    def test_multiple_must_conditions(self, client, populated_collection):
        """Multiple must = AND of all conditions."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={
                "must": [
                    {"key": "category", "match": "programming"},
                    {"key": "priority", "range": {"gte": 9}},
                ]
            },
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") == "programming"
            assert item.get("numerics", {}).get("priority", 0) >= 9

    @xfail_on_valkey
    def test_filter_with_keyword_mode(self, client, populated_collection):
        """Filters should work with keyword search too."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="language",
            mode="keyword",
            filters={"must": [{"key": "category", "match": "programming"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") == "programming"

    def test_must_not_with_range(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must_not": [{"key": "priority", "range": {"gte": 9}}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("numerics", {}).get("priority", 0) < 9

    def test_should_with_must_not(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={
                "should": [
                    {"key": "category", "match": "programming"},
                    {"key": "category", "match": "infrastructure"},
                ],
                "must_not": [{"key": "priority", "range": {"lt": 8}}],
            },
        )
        data = resp.json()
        for item in data["items"]:
            cat = item.get("tags", {}).get("category")
            assert cat in ("programming", "infrastructure")
            assert item.get("numerics", {}).get("priority", 0) >= 8

    def test_range_gt_strict(self, client, populated_collection):
        """gt (strictly greater than) vs gte."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": [{"key": "priority", "range": {"gt": 9}}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("numerics", {}).get("priority", 0) > 9

    def test_single_should_equals_must(self, client, populated_collection):
        """6.2.2: Single should == must equivalent."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"should": [{"key": "category", "match": "programming"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") == "programming"

    def test_must_not_single_doc_returns_empty(self, client, collection_factory):
        """6.3.2: must_not on the only matching doc → empty results."""
        coll = collection_factory(
            fields=[{"name": "env", "type": "tag"}]
        )
        name = coll["name"]
        client.put(
            f"/collections/{name}/documents/only-one",
            json={"content": "only document here", "tags": {"env": "prod"}},
        )
        time.sleep(0.5)
        resp = client.post(
            f"/collections/{name}/documents/search",
            json={
                "query": "only document",
                "mode": "semantic",
                "filters": {"must_not": [{"key": "env", "match": "prod"}]},
            },
        )
        assert resp.status_code == 200
        assert len(resp.json()["items"]) == 0

    def test_empty_must_array_is_noop(self, client, populated_collection):
        """6.4.3: Empty must: [] → noop, returns results."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": []},
        )
        assert len(resp.json()["items"]) > 0

    def test_range_lte_zero(self, client, populated_collection):
        """6.5.4: range: {lte: 0} → only docs with priority ≤ 0."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "priority", "range": {"lte": 0}}]},
            },
        )
        assert resp.status_code == 200
        for item in resp.json()["items"]:
            assert item.get("numerics", {}).get("priority", 0) <= 0

    def test_range_gt_and_gte_conflict_returns_400(self, client, populated_collection):
        """6.5.5: range: {gt: 5, gte: 5} → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "priority", "range": {"gt": 5, "gte": 5}}]},
            },
        )
        assert resp.status_code == 400

    def test_range_lt_and_lte_conflict_returns_400(self, client, populated_collection):
        """6.5.6: range: {lt: 5, lte: 5} → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "priority", "range": {"lt": 5, "lte": 5}}]},
            },
        )
        assert resp.status_code == 400

    def test_empty_range_returns_400(self, client, populated_collection):
        """6.5.7: Empty range {} → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "priority", "range": {}}]},
            },
        )
        assert resp.status_code == 400


@pytest.mark.p0
class TestFilterValidation:
    """P0 filter validation errors."""

    def test_unknown_field_in_filter_returns_400(self, client, populated_collection):
        """6.6.1: Unknown field in filter → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "nonexistent_field", "match": "val"}]},
            },
        )
        assert resp.status_code == 400

    def test_condition_without_match_or_range_returns_400(self, client, populated_collection):
        """6.6.2: Condition without match/range → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "category"}]},
            },
        )
        assert resp.status_code == 400

    def test_both_match_and_range_returns_400(self, client, populated_collection):
        """6.6.3: Both match AND range → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {
                    "must": [{"key": "category", "match": "programming", "range": {"gte": 5}}]
                },
            },
        )
        assert resp.status_code == 400

    def test_match_on_numeric_field_returns_400(self, client, populated_collection):
        """6.6.4: match on numeric / range on tag → 400."""
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": [{"key": "priority", "match": "high"}]},
            },
        )
        assert resp.status_code == 400

    def test_over_32_conditions_returns_400(self, client, populated_collection):
        """6.6.5: >32 conditions → 400."""
        coll = populated_collection["name"]
        conditions = [{"key": "category", "match": f"val{i}"} for i in range(33)]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={
                "query": "technology",
                "mode": "semantic",
                "filters": {"must": conditions},
            },
        )
        assert resp.status_code == 400


@pytest.mark.p0
class TestFilterWithModes:
    """P0 filters with different search modes."""

    @xfail_on_valkey
    def test_filters_with_hybrid_mode(self, client, populated_collection):
        """6.7.1: Filters + hybrid mode."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="hybrid",
            filters={"must": [{"key": "category", "match": "programming"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") == "programming"

    def test_filters_with_semantic_mode(self, client, populated_collection):
        """6.7.2: Filters + semantic mode."""
        coll = populated_collection["name"]
        resp = search_with_retry(
            client,
            coll,
            query="technology",
            mode="semantic",
            filters={"must": [{"key": "category", "match": "infrastructure"}]},
        )
        data = resp.json()
        for item in data["items"]:
            assert item.get("tags", {}).get("category") == "infrastructure"


@pytest.mark.p0
class TestFilterSpecialTags:
    """P0/P1 tag values with special characters."""

    @xfail_on_valkey
    def test_tag_with_spaces(self, client, collection_factory):
        """6.9.1: Tag with spaces — stored and filterable."""
        coll = collection_factory(fields=[{"name": "label", "type": "tag"}])
        name = coll["name"]
        client.put(
            f"/collections/{name}/documents/sp-1",
            json={"content": "document with spaced tag", "tags": {"label": "hello world"}},
        )
        time.sleep(0.5)
        resp = search_with_retry(
            client,
            name,
            query="document",
            mode="semantic",
            filters={"must": [{"key": "label", "match": "hello world"}]},
        )
        ids = [item["id"] for item in resp.json()["items"]]
        assert "sp-1" in ids

    def test_tag_with_commas_not_split(self, client, collection_factory):
        """6.9.2: Tag with commas — not split into multiple values."""
        coll = collection_factory(fields=[{"name": "label", "type": "tag"}])
        name = coll["name"]
        client.put(
            f"/collections/{name}/documents/cm-1",
            json={"content": "comma tag test", "tags": {"label": "a,b,c"}},
        )
        doc = client.get(f"/collections/{name}/documents/cm-1").json()
        assert doc.get("tags", {}).get("label") == "a,b,c"

    def test_tag_with_pipe_not_or(self, client, collection_factory):
        """6.9.3: Tag with pipe — not interpreted as OR."""
        coll = collection_factory(fields=[{"name": "label", "type": "tag"}])
        name = coll["name"]
        client.put(
            f"/collections/{name}/documents/pipe-1",
            json={"content": "pipe tag test", "tags": {"label": "a|b"}},
        )
        doc = client.get(f"/collections/{name}/documents/pipe-1").json()
        assert doc.get("tags", {}).get("label") == "a|b"

    def test_tag_with_braces_escaped(self, client, collection_factory):
        """6.9.4: Tag with braces — proper escaping."""
        coll = collection_factory(fields=[{"name": "label", "type": "tag"}])
        name = coll["name"]
        client.put(
            f"/collections/{name}/documents/brace-1",
            json={"content": "brace tag test", "tags": {"label": "{value}"}},
        )
        doc = client.get(f"/collections/{name}/documents/brace-1").json()
        assert doc.get("tags", {}).get("label") == "{value}"
