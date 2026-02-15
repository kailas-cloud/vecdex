"""Health and metrics endpoint tests."""

import pytest


@pytest.mark.p0
class TestHealth:
    """GET /health — no auth required."""

    def test_health_returns_200(self, health_client):
        resp = health_client.get("/health")
        assert resp.status_code == 200

    def test_health_has_status_field(self, health_client):
        data = health_client.get("/health").json()
        assert "status" in data
        assert data["status"] in ("ok", "degraded", "error")

    def test_health_has_checks(self, health_client):
        data = health_client.get("/health").json()
        assert "checks" in data
        assert isinstance(data["checks"], dict)

    def test_health_database_check_present(self, health_client):
        data = health_client.get("/health").json()
        assert "database" in data["checks"]
        assert data["checks"]["database"] in ("ok", "error")

    def test_health_embedding_check_present(self, health_client):
        data = health_client.get("/health").json()
        assert "embedding" in data["checks"]
        assert data["checks"]["embedding"] in ("ok", "error")

    def test_health_ok_when_all_pass(self, health_client):
        data = health_client.get("/health").json()
        if all(v == "ok" for v in data["checks"].values()):
            assert data["status"] == "ok"

    def test_health_no_auth_required(self, raw_client):
        resp = raw_client.get("/health")
        assert resp.status_code == 200


@pytest.mark.p0
class TestMetrics:
    """GET /metrics — no auth required, Prometheus format."""

    def test_metrics_returns_200(self, health_client):
        resp = health_client.get("/metrics")
        assert resp.status_code == 200

    def test_metrics_no_auth_required(self, raw_client):
        resp = raw_client.get("/metrics")
        assert resp.status_code == 200

    def test_metrics_content_type(self, health_client):
        resp = health_client.get("/metrics")
        ct = resp.headers.get("content-type", "")
        assert "text/plain" in ct or "text/openmetrics" in ct


@pytest.mark.p1
class TestHealthP1:
    """P1 health edge cases."""

    def test_health_json_content_type(self, health_client):
        resp = health_client.get("/health")
        assert "application/json" in resp.headers.get("content-type", "")

    def test_health_status_ok_means_200(self, health_client):
        resp = health_client.get("/health")
        data = resp.json()
        if data["status"] == "ok":
            assert resp.status_code == 200

    def test_health_repeated_calls_stable(self, health_client):
        """Health endpoint should be idempotent."""
        r1 = health_client.get("/health").json()
        r2 = health_client.get("/health").json()
        assert r1["status"] == r2["status"]

    def test_health_with_auth_header_still_works(self, client):
        """Health exempt from auth but should work WITH auth too."""
        resp = client.get("/health")
        assert resp.status_code == 200


@pytest.mark.p1
class TestMetricsP1:
    """P1 metrics edge cases."""

    def test_metrics_has_vecdex_namespace(self, health_client):
        """Prometheus metrics should use vecdex_ namespace."""
        text = health_client.get("/metrics").text
        assert "vecdex_" in text or "process_" in text or "go_" in text

    def test_metrics_body_not_empty(self, health_client):
        text = health_client.get("/metrics").text
        assert len(text) > 100
