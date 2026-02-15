"""Error response contract tests — consistent error format across endpoints."""

import pytest


pytestmark = pytest.mark.p1


class TestErrorFormat:
    """Every error response must have 'code' and 'message' fields."""

    def test_401_has_code_and_message(self, raw_client):
        data = raw_client.get("/collections").json()
        assert "code" in data
        assert "message" in data

    def test_404_collection_has_code_and_message(self, client):
        data = client.get("/collections/nonexistent-xyz").json()
        assert data["code"] == "collection_not_found"
        assert "message" in data
        assert len(data["message"]) > 0

    def test_404_document_has_code_and_message(self, client, collection_factory):
        coll = collection_factory()
        data = client.get(f"/collections/{coll['name']}/documents/nope").json()
        assert data["code"] == "document_not_found"
        assert "message" in data

    def test_409_has_code_and_message(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post("/collections", json={"name": coll["name"]})
        assert resp.status_code == 409
        data = resp.json()
        assert data["code"] == "collection_already_exists"
        assert "message" in data

    def test_400_malformed_json_has_code(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            content=b"not json",
            headers={"content-type": "application/json"},
        )
        assert resp.status_code == 400
        data = resp.json()
        assert data["code"] == "bad_request"

    def test_400_validation_has_code(self, client):
        resp = client.post("/collections", json={"name": ""})
        assert resp.status_code == 400
        data = resp.json()
        assert data["code"] in ("validation_failed", "bad_request")


class TestContentType:
    """Error responses should have application/json Content-Type."""

    def test_404_content_type(self, client):
        resp = client.get("/collections/nonexistent-xyz")
        assert "application/json" in resp.headers.get("content-type", "")

    def test_401_content_type(self, raw_client):
        resp = raw_client.get("/collections")
        assert "application/json" in resp.headers.get("content-type", "")

    def test_409_content_type(self, client, collection_factory):
        coll = collection_factory()
        resp = client.post("/collections", json={"name": coll["name"]})
        assert "application/json" in resp.headers.get("content-type", "")
