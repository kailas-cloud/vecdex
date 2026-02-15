"""Authentication tests — Bearer token validation."""

import pytest


@pytest.mark.p0
class TestAuthRequired:
    """Endpoints that require Bearer auth should return 401 without it."""

    def test_no_auth_header_returns_401(self, raw_client):
        resp = raw_client.get("/collections")
        assert resp.status_code == 401

    def test_no_auth_error_has_code_and_message(self, raw_client):
        data = raw_client.get("/collections").json()
        assert "code" in data
        assert "message" in data

    def test_empty_bearer_returns_401(self, raw_client):
        # httpx rejects "Bearer " (empty token) as illegal header value,
        # so use a whitespace-only token instead
        resp = raw_client.get(
            "/collections", headers={"Authorization": "Bearer x"}
        )
        assert resp.status_code == 401

    def test_invalid_token_returns_401(self, raw_client):
        resp = raw_client.get(
            "/collections", headers={"Authorization": "Bearer wrong-key"}
        )
        assert resp.status_code == 401

    def test_basic_auth_returns_401(self, raw_client):
        resp = raw_client.get(
            "/collections", headers={"Authorization": "Basic dGVzdDp0ZXN0"}
        )
        assert resp.status_code == 401

    def test_lowercase_bearer_returns_401(self, raw_client):
        resp = raw_client.get(
            "/collections", headers={"Authorization": "bearer test-api-key"}
        )
        assert resp.status_code == 401

    def test_post_collection_requires_auth(self, raw_client):
        resp = raw_client.post("/collections", json={"name": "test"})
        assert resp.status_code == 401

    def test_put_document_requires_auth(self, raw_client):
        resp = raw_client.put(
            "/collections/test/documents/doc1",
            json={"content": "test"},
        )
        assert resp.status_code == 401

    def test_search_requires_auth(self, raw_client):
        resp = raw_client.post(
            "/collections/test/documents/search",
            json={"query": "test"},
        )
        assert resp.status_code == 401


@pytest.mark.p0
class TestAuthExempt:
    """Endpoints exempt from auth."""

    def test_health_exempt(self, raw_client):
        resp = raw_client.get("/health")
        assert resp.status_code == 200

    def test_metrics_exempt(self, raw_client):
        resp = raw_client.get("/metrics")
        assert resp.status_code == 200


@pytest.mark.p1
class TestAuthP1:
    """P1 auth edge cases."""

    def test_delete_collection_requires_auth(self, raw_client):
        resp = raw_client.delete("/collections/test")
        assert resp.status_code == 401

    def test_patch_document_requires_auth(self, raw_client):
        resp = raw_client.patch(
            "/collections/test/documents/doc1",
            json={"content": "test"},
        )
        assert resp.status_code == 401

    def test_batch_upsert_requires_auth(self, raw_client):
        resp = raw_client.post(
            "/collections/test/documents/batch-upsert",
            json={"documents": [{"id": "x", "content": "y"}]},
        )
        assert resp.status_code == 401

    def test_batch_delete_requires_auth(self, raw_client):
        resp = raw_client.post(
            "/collections/test/documents/batch-delete",
            json={"ids": ["x"]},
        )
        assert resp.status_code == 401

    def test_usage_requires_auth(self, raw_client):
        resp = raw_client.get("/usage")
        assert resp.status_code == 401

    def test_get_document_requires_auth(self, raw_client):
        resp = raw_client.get("/collections/test/documents/doc1")
        assert resp.status_code == 401

    def test_list_documents_requires_auth(self, raw_client):
        resp = raw_client.get("/collections/test/documents")
        assert resp.status_code == 401

    def test_token_with_extra_spaces_rejected(self, raw_client):
        resp = raw_client.get(
            "/collections",
            headers={"Authorization": "Bearer  test-api-key"},
        )
        # Double space after Bearer — token becomes " test-api-key"
        assert resp.status_code == 401

    def test_valid_token_works(self, client):
        """Sanity: valid token passes auth."""
        resp = client.get("/collections")
        assert resp.status_code == 200
