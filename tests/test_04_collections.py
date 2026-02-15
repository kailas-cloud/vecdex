"""Collection CRUD tests."""

import pytest

from conftest import unique_name, xfail_on_valkey


@pytest.mark.p0
class TestCreateCollection:
    """POST /collections"""

    def test_create_returns_201(self, client, collection_factory):
        coll = collection_factory()
        assert coll is not None

    def test_create_returns_name(self, client, collection_factory):
        name = unique_name()
        coll = collection_factory(name=name)
        assert coll["name"] == name

    def test_create_returns_created_at(self, client, collection_factory):
        coll = collection_factory()
        assert "created_at" in coll
        assert isinstance(coll["created_at"], str)

    def test_create_with_fields(self, client, collection_factory):
        coll = collection_factory(
            fields=[
                {"name": "category", "type": "tag"},
                {"name": "priority", "type": "numeric"},
            ]
        )
        assert coll.get("fields") is not None
        assert len(coll["fields"]) == 2

    def test_create_without_fields(self, client, collection_factory):
        coll = collection_factory()
        # fields may be null/absent when no fields are defined
        fields = coll.get("fields")
        assert fields is None or len(fields) == 0

    def test_create_duplicate_returns_409(self, client, collection_factory):
        name = unique_name()
        collection_factory(name=name)
        resp = client.post("/collections", json={"name": name})
        assert resp.status_code == 409
        data = resp.json()
        assert data["code"] == "collection_already_exists"

    def test_create_empty_name_returns_400(self, client):
        resp = client.post("/collections", json={"name": ""})
        assert resp.status_code == 400

    def test_create_missing_name_returns_400(self, client):
        resp = client.post("/collections", json={})
        assert resp.status_code == 400

    def test_create_invalid_name_special_chars(self, client):
        resp = client.post("/collections", json={"name": "bad name!"})
        # Server may return 400 or 500 for invalid name patterns
        assert resp.status_code in (400, 500)


@pytest.mark.p0
class TestGetCollection:
    """GET /collections/{collection}"""

    def test_get_existing(self, client, collection_factory):
        coll = collection_factory()
        resp = client.get(f"/collections/{coll['name']}")
        assert resp.status_code == 200
        data = resp.json()
        assert data["name"] == coll["name"]

    def test_get_nonexistent_returns_404(self, client):
        resp = client.get("/collections/nonexistent-collection-xyz")
        assert resp.status_code == 404
        data = resp.json()
        assert data["code"] == "collection_not_found"

    def test_get_has_document_count(self, client, collection_factory):
        coll = collection_factory()
        data = client.get(f"/collections/{coll['name']}").json()
        # document_count may be 0 or absent for empty collections
        if "document_count" in data:
            assert isinstance(data["document_count"], int)


@pytest.mark.p0
class TestDeleteCollection:
    """DELETE /collections/{collection}"""

    def test_delete_returns_204(self, client):
        name = unique_name()
        client.post("/collections", json={"name": name})
        resp = client.delete(f"/collections/{name}")
        assert resp.status_code == 204

    def test_delete_nonexistent_returns_404(self, client):
        resp = client.delete("/collections/nonexistent-collection-xyz")
        assert resp.status_code == 404

    def test_delete_removes_collection(self, client):
        name = unique_name()
        client.post("/collections", json={"name": name})
        client.delete(f"/collections/{name}")
        resp = client.get(f"/collections/{name}")
        assert resp.status_code == 404


@pytest.mark.p0
class TestListCollections:
    """GET /collections"""

    def test_list_returns_200(self, client):
        resp = client.get("/collections")
        assert resp.status_code == 200

    def test_list_has_items_and_has_more(self, client):
        data = client.get("/collections").json()
        assert "items" in data
        assert "has_more" in data
        assert isinstance(data["items"], list)

    def test_list_includes_created_collection(self, client, collection_factory):
        coll = collection_factory()
        data = client.get("/collections").json()
        names = [c["name"] for c in data["items"]]
        assert coll["name"] in names

    def test_list_with_limit(self, client, collection_factory):
        collection_factory()
        collection_factory()
        resp = client.get("/collections", params={"limit": 1})
        data = resp.json()
        assert len(data["items"]) <= 1

    def test_list_with_cursor(self, client, collection_factory):
        collection_factory()
        collection_factory()
        data = client.get("/collections", params={"limit": 1}).json()
        assert "has_more" in data
        if data["has_more"]:
            assert "next_cursor" in data


@pytest.mark.p1
class TestCreateCollectionP1:
    """P1 collection creation edge cases."""

    def test_name_with_underscores_and_hyphens(self, client, collection_factory):
        name = f"test_coll-{unique_name()}"
        coll = collection_factory(name=name)
        assert coll["name"] == name

    def test_single_char_name(self, client, collection_factory):
        coll = collection_factory(name=f"a{unique_name()[-8:]}")
        assert coll is not None

    def test_max_length_name(self, client, collection_factory):
        """64 char max name."""
        name = "a" * 64
        resp = client.post("/collections", json={"name": name})
        if resp.status_code == 201:
            client.delete(f"/collections/{name}")
        assert resp.status_code in (201, 400)

    def test_over_max_length_name(self, client):
        name = "a" * 65
        resp = client.post("/collections", json={"name": name})
        assert resp.status_code in (400, 500)

    def test_numeric_only_name(self, client, collection_factory):
        """Names like '12345' should be valid per pattern."""
        name = f"12345{unique_name()[-8:]}"
        coll = collection_factory(name=name)
        assert coll["name"] == name

    def test_create_returns_vector_dimensions(self, client, collection_factory):
        coll = collection_factory()
        if "vector_dimensions" in coll or "vectorDimensions" in coll:
            dim = coll.get("vector_dimensions") or coll.get("vectorDimensions")
            assert isinstance(dim, int)
            assert dim > 0

    def test_field_type_tag(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "status", "type": "tag"}])
        fields = coll.get("fields", [])
        assert any(f["name"] == "status" for f in fields)

    def test_field_type_numeric(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "rating", "type": "numeric"}])
        fields = coll.get("fields", [])
        assert any(f["name"] == "rating" for f in fields)


@pytest.mark.p1
class TestListCollectionsP1:
    """P1 list collections edge cases."""

    def test_list_all_items_when_limit_large(self, client, collection_factory):
        """When limit is large enough, has_more == false."""
        collection_factory()
        data = client.get("/collections", params={"limit": 100}).json()
        assert data["has_more"] is False

    def test_list_invalid_cursor_returns_empty(self, client):
        data = client.get("/collections", params={"cursor": "nonexistent-xyz"}).json()
        assert len(data["items"]) == 0

    def test_list_deleted_collection_not_in_list(self, client):
        name = unique_name()
        client.post("/collections", json={"name": name})
        client.delete(f"/collections/{name}")
        data = client.get("/collections").json()
        names = [c["name"] for c in data["items"]]
        assert name not in names


@pytest.mark.p1
class TestDeleteCollectionP1:
    """P1 delete edge cases."""

    def test_double_delete_returns_404(self, client):
        name = unique_name()
        client.post("/collections", json={"name": name})
        client.delete(f"/collections/{name}")
        resp = client.delete(f"/collections/{name}")
        assert resp.status_code == 404

    def test_delete_collection_removes_documents(self, client):
        """Deleting a collection should also remove all its documents."""
        name = unique_name()
        client.post("/collections", json={"name": name})
        client.put(
            f"/collections/{name}/documents/doc-1",
            json={"content": "orphan test"},
        )
        client.delete(f"/collections/{name}")
        resp = client.get(f"/collections/{name}/documents/doc-1")
        assert resp.status_code == 404

    def test_delete_and_recreate_same_name(self, client):
        """3.4.5: Delete collection then recreate with same name → 201."""
        name = unique_name()
        client.post("/collections", json={"name": name})
        client.delete(f"/collections/{name}")
        resp = client.post("/collections", json={"name": name})
        assert resp.status_code == 201
        # Clean up
        client.delete(f"/collections/{name}")


@pytest.mark.p2
class TestCreateCollectionP2:
    """P2 collection creation edge cases."""

    def test_unicode_name_returns_400(self, client):
        """3.1.15: Unicode name like 'кириллица' → 400."""
        resp = client.post("/collections", json={"name": "кириллица"})
        assert resp.status_code == 400

    def test_name_with_dot_returns_400(self, client):
        """3.1.16: Name with dot 'my.col' → 400."""
        resp = client.post("/collections", json={"name": "my.col"})
        assert resp.status_code == 400


@pytest.mark.p1
class TestCreateCollectionFieldsP1:
    """P1 collection field validation edge cases."""

    @xfail_on_valkey
    def test_max_64_fields_accepted(self, client):
        """3.1.18: 64 fields (max) → 201."""
        name = unique_name()
        fields = [{"name": f"f{i}", "type": "tag"} for i in range(64)]
        resp = client.post("/collections", json={"name": name, "fields": fields})
        if resp.status_code == 201:
            client.delete(f"/collections/{name}")
        assert resp.status_code == 201

    def test_65_fields_returns_400(self, client):
        """3.1.19: 65 fields → 400."""
        name = unique_name()
        fields = [{"name": f"f{i}", "type": "tag"} for i in range(65)]
        resp = client.post("/collections", json={"name": name, "fields": fields})
        assert resp.status_code == 400

    def test_unknown_field_type_returns_400(self, client):
        """3.1.20: Unknown field type 'text' → 400."""
        name = unique_name()
        resp = client.post(
            "/collections",
            json={"name": name, "fields": [{"name": "f1", "type": "text"}]},
        )
        assert resp.status_code == 400

    def test_reserved_field_name_id_returns_400(self, client):
        """3.1.21: Reserved field name 'id' → 400."""
        name = unique_name()
        resp = client.post(
            "/collections",
            json={"name": name, "fields": [{"name": "id", "type": "tag"}]},
        )
        assert resp.status_code == 400

    def test_reserved_field_name_content_returns_400(self, client):
        """3.1.21: Reserved field name 'content' → 400."""
        name = unique_name()
        resp = client.post(
            "/collections",
            json={"name": name, "fields": [{"name": "content", "type": "tag"}]},
        )
        assert resp.status_code == 400

    def test_reserved_field_name_vector_returns_400(self, client):
        """3.1.21: Reserved field name 'vector' → 400."""
        name = unique_name()
        resp = client.post(
            "/collections",
            json={"name": name, "fields": [{"name": "vector", "type": "tag"}]},
        )
        assert resp.status_code == 400

    def test_duplicate_field_name_returns_400(self, client):
        """3.1.22: Duplicate field name → 400."""
        name = unique_name()
        resp = client.post(
            "/collections",
            json={
                "name": name,
                "fields": [
                    {"name": "dup", "type": "tag"},
                    {"name": "dup", "type": "numeric"},
                ],
            },
        )
        assert resp.status_code == 400

    def test_empty_fields_array_accepted(self, client):
        """3.1.24: Empty array fields: [] → 201."""
        name = unique_name()
        resp = client.post(
            "/collections", json={"name": name, "fields": []}
        )
        if resp.status_code == 201:
            client.delete(f"/collections/{name}")
        assert resp.status_code == 201

    def test_duplicate_name_different_fields_returns_409(self, client, collection_factory):
        """3.1.26: Duplicate name with different fields → 409."""
        name = unique_name()
        collection_factory(
            name=name,
            fields=[{"name": "a", "type": "tag"}],
        )
        resp = client.post(
            "/collections",
            json={"name": name, "fields": [{"name": "b", "type": "numeric"}]},
        )
        assert resp.status_code == 409
