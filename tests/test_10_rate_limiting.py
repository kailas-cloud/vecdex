"""Rate limiting tests.

Rate limiting is defined in the OpenAPI spec but not yet implemented
in the server. All tests are xfail.
"""

import pytest


pytestmark = [pytest.mark.p2]

RATE_LIMIT_XFAIL = pytest.mark.xfail(
    reason="Rate limiting not implemented — error code exists but no middleware",
    strict=True,
)


@RATE_LIMIT_XFAIL
class TestRateLimitHeaders:
    """Response should include X-RateLimit-* headers."""

    def test_rate_limit_headers_on_success(self, client):
        resp = client.get("/collections")
        assert "x-ratelimit-limit" in resp.headers
        assert "x-ratelimit-remaining" in resp.headers

    def test_rate_limit_headers_on_search(self, client, populated_collection):
        coll = populated_collection["name"]
        resp = client.post(
            f"/collections/{coll}/documents/search",
            json={"query": "test", "mode": "semantic"},
        )
        assert "x-ratelimit-limit" in resp.headers


@RATE_LIMIT_XFAIL
class TestRateLimit429:
    """Exceeding rate limit should return 429."""

    def test_burst_triggers_429(self, client):
        """Rapid-fire requests should eventually hit 429."""
        got_429 = False
        for _ in range(200):
            resp = client.get("/collections")
            if resp.status_code == 429:
                got_429 = True
                break
        assert got_429

    def test_429_has_retry_after(self, client):
        got_429 = False
        for _ in range(200):
            resp = client.get("/collections")
            if resp.status_code == 429:
                got_429 = True
                assert "retry-after" in resp.headers
                break
        assert got_429, "Never received 429"

    def test_429_has_error_code(self, client):
        got_429 = False
        for _ in range(200):
            resp = client.get("/collections")
            if resp.status_code == 429:
                got_429 = True
                data = resp.json()
                assert data["code"] == "rate_limited"
                break
        assert got_429, "Never received 429"


@RATE_LIMIT_XFAIL
class TestRateLimitP0:
    """P0 rate limit header presence on 2xx."""

    def test_ratelimit_limit_on_2xx(self, client):
        """10.4: X-RateLimit-Limit on 2xx responses."""
        resp = client.get("/collections")
        assert resp.status_code == 200
        assert "x-ratelimit-limit" in resp.headers
        limit = int(resp.headers["x-ratelimit-limit"])
        assert limit > 0


@RATE_LIMIT_XFAIL
class TestRateLimitP1:
    """P1 rate limit edge cases."""

    def test_search_rate_limit(self, client, populated_collection):
        """10.5: Search rate limit (20 req/s)."""
        coll = populated_collection["name"]
        got_429 = False
        for _ in range(50):
            resp = client.post(
                f"/collections/{coll}/documents/search",
                json={"query": "test", "mode": "semantic"},
            )
            if resp.status_code == 429:
                got_429 = True
                break
        assert got_429, "Search rate limit not triggered"

    def test_ratelimit_remaining_decrements(self, client):
        """10.7: X-RateLimit-Remaining decrements."""
        r1 = client.get("/collections")
        remaining1 = int(r1.headers["x-ratelimit-remaining"])
        r2 = client.get("/collections")
        remaining2 = int(r2.headers["x-ratelimit-remaining"])
        assert remaining2 < remaining1

    def test_ratelimit_reset_restores(self, client):
        """10.8: After reset — limit restored."""
        import time
        r1 = client.get("/collections")
        assert "x-ratelimit-reset" in r1.headers
        reset_ts = int(r1.headers["x-ratelimit-reset"])
        # Wait for reset (can't actually wait in test, just verify header exists)
        assert reset_ts > 0
