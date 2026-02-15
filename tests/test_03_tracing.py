"""X-Request-ID tracing tests.

wideEventMiddleware propagates X-Request-ID from chi middleware.RequestID
context to response headers. Client-provided IDs are echoed back;
server generates a unique ID when the client doesn't send one.
"""

import uuid

import pytest


pytestmark = [pytest.mark.p1]


class TestRequestIDEcho:
    """Server should echo client-provided X-Request-ID."""

    def test_echo_on_success(self, client):
        rid = str(uuid.uuid4())
        resp = client.get("/collections", headers={"X-Request-ID": rid})
        assert resp.headers.get("x-request-id") == rid

    def test_echo_on_error(self, raw_client):
        rid = str(uuid.uuid4())
        resp = raw_client.get("/collections", headers={"X-Request-ID": rid})
        assert resp.status_code == 401
        assert resp.headers.get("x-request-id") == rid


class TestRequestIDGeneration:
    """Server should generate X-Request-ID if client doesn't send one."""

    def test_generated_when_absent(self, client):
        resp = client.get("/collections")
        assert "x-request-id" in resp.headers

    def test_generated_on_health(self, health_client):
        resp = health_client.get("/health")
        assert "x-request-id" in resp.headers

    def test_generated_on_404(self, client):
        resp = client.get("/collections/nonexistent-xyz")
        assert resp.status_code == 404
        assert "x-request-id" in resp.headers

    def test_unique_per_request(self, client):
        r1 = client.get("/collections")
        r2 = client.get("/collections")
        id1 = r1.headers.get("x-request-id")
        id2 = r2.headers.get("x-request-id")
        assert id1 is not None
        assert id1 != id2
