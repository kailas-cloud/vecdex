"""Document CRUD tests — PUT, GET, DELETE, PATCH."""

import pytest

from conftest import unique_name, assert_embedding_headers


@pytest.mark.p0
class TestUpsertDocument:
    """PUT /collections/{collection}/documents/{id}"""

    def test_create_returns_201(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "test content"},
        )
        assert resp.status_code == 201

    def test_update_returns_200(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "original"},
        )
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "updated"},
        )
        assert resp.status_code == 200

    def test_response_has_id_and_content(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "test content"},
        )
        data = resp.json()
        assert data["id"] == "doc-1"
        assert data["content"] == "test content"

    def test_upsert_with_tags(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "lang", "type": "tag"}])
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "test", "tags": {"lang": "python"}},
        )
        data = resp.json()
        assert data.get("tags", {}).get("lang") == "python"

    def test_upsert_with_numerics(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "rating", "type": "numeric"}])
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "test", "numerics": {"rating": 42}},
        )
        data = resp.json()
        assert data.get("numerics", {}).get("rating") == 42

    def test_upsert_missing_content_returns_400(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={},
        )
        assert resp.status_code == 400

    def test_upsert_empty_content_returns_400(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": ""},
        )
        assert resp.status_code == 400

    def test_upsert_nonexistent_collection_returns_404(self, client):
        resp = client.put(
            "/collections/nonexistent-xyz/documents/doc-1",
            json={"content": "test"},
        )
        assert resp.status_code == 404

    def test_upsert_idempotent(self, client, collection_factory):
        """PUT same doc twice — second returns 200."""
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/idem-1",
            json={"content": "same content"},
        )
        resp = client.put(
            f"/collections/{coll['name']}/documents/idem-1",
            json={"content": "same content"},
        )
        assert resp.status_code == 200


@pytest.mark.p0
class TestGetDocument:
    """GET /collections/{collection}/documents/{id}"""

    def test_get_existing_document(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "hello world"},
        )
        resp = client.get(f"/collections/{coll['name']}/documents/doc-1")
        assert resp.status_code == 200
        data = resp.json()
        assert data["id"] == "doc-1"
        assert data["content"] == "hello world"

    def test_get_nonexistent_document_returns_404(self, client, collection_factory):
        coll = collection_factory()
        resp = client.get(f"/collections/{coll['name']}/documents/no-such-doc")
        assert resp.status_code == 404
        data = resp.json()
        assert data["code"] == "document_not_found"

    def test_get_nonexistent_collection_returns_404(self, client):
        resp = client.get("/collections/nonexistent-xyz/documents/doc-1")
        assert resp.status_code == 404


@pytest.mark.p0
class TestDeleteDocument:
    """DELETE /collections/{collection}/documents/{id}"""

    def test_delete_returns_204(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "to be deleted"},
        )
        resp = client.delete(f"/collections/{coll['name']}/documents/doc-1")
        assert resp.status_code == 204

    def test_delete_nonexistent_returns_404(self, client, collection_factory):
        coll = collection_factory()
        resp = client.delete(f"/collections/{coll['name']}/documents/no-such-doc")
        assert resp.status_code == 404

    def test_delete_removes_document(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "will delete"},
        )
        client.delete(f"/collections/{coll['name']}/documents/doc-1")
        resp = client.get(f"/collections/{coll['name']}/documents/doc-1")
        assert resp.status_code == 404


@pytest.mark.p0
class TestPatchDocument:
    """PATCH /collections/{collection}/documents/{id}"""

    def test_patch_tags_only(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "lang", "type": "tag"}])
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "original", "tags": {"lang": "go"}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"tags": {"lang": "python"}},
        )
        assert resp.status_code == 200
        data = resp.json()
        assert data.get("tags", {}).get("lang") == "python"
        assert data["content"] == "original"

    def test_patch_content_triggers_revectorization(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "original content"},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "updated content"},
        )
        assert resp.status_code == 200
        assert resp.json()["content"] == "updated content"

    def test_patch_nonexistent_returns_404(self, client, collection_factory):
        coll = collection_factory()
        resp = client.patch(
            f"/collections/{coll['name']}/documents/no-such-doc",
            json={"content": "fail"},
        )
        assert resp.status_code == 404

    def test_patch_empty_body_returns_400(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "test"},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/doc-1",
            json={},
        )
        assert resp.status_code == 400


@pytest.mark.p0
class TestListDocuments:
    """GET /collections/{collection}/documents"""

    def test_list_empty_collection(self, client, collection_factory):
        coll = collection_factory()
        resp = client.get(f"/collections/{coll['name']}/documents")
        assert resp.status_code == 200
        data = resp.json()
        assert "items" in data
        assert len(data["items"]) == 0

    def test_list_returns_documents(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            json={"content": "first"},
        )
        client.put(
            f"/collections/{coll['name']}/documents/doc-2",
            json={"content": "second"},
        )
        resp = client.get(f"/collections/{coll['name']}/documents")
        data = resp.json()
        assert len(data["items"]) == 2

    def test_list_has_pagination_fields(self, client, collection_factory):
        coll = collection_factory()
        resp = client.get(f"/collections/{coll['name']}/documents")
        data = resp.json()
        assert "has_more" in data


@pytest.mark.p1
class TestUpsertDocumentP1:
    """P1 upsert edge cases."""

    def test_doc_id_with_hyphens_and_underscores(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/my-doc_123",
            json={"content": "test"},
        )
        assert resp.status_code == 201
        assert resp.json()["id"] == "my-doc_123"

    def test_long_content(self, client, collection_factory):
        """Content up to 160KB should be accepted."""
        coll = collection_factory()
        content = "x" * 10000  # 10KB — well within limits
        resp = client.put(
            f"/collections/{coll['name']}/documents/long-1",
            json={"content": content},
        )
        assert resp.status_code == 201

    def test_update_preserves_id(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/preserve-id",
            json={"content": "v1"},
        )
        resp = client.put(
            f"/collections/{coll['name']}/documents/preserve-id",
            json={"content": "v2"},
        )
        assert resp.json()["id"] == "preserve-id"
        assert resp.json()["content"] == "v2"

    def test_upsert_malformed_json_returns_400(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/doc-1",
            content=b"{broken",
            headers={"content-type": "application/json"},
        )
        assert resp.status_code == 400

    def test_multiple_tags(self, client, collection_factory):
        coll = collection_factory(
            fields=[
                {"name": "lang", "type": "tag"},
                {"name": "status", "type": "tag"},
            ]
        )
        resp = client.put(
            f"/collections/{coll['name']}/documents/multi-tag",
            json={
                "content": "test",
                "tags": {"lang": "go", "status": "draft"},
            },
        )
        data = resp.json()
        assert data.get("tags", {}).get("lang") == "go"
        assert data.get("tags", {}).get("status") == "draft"


@pytest.mark.p1
class TestPatchDocumentP1:
    """P1 patch edge cases."""

    def test_patch_remove_tag_with_null(self, client, collection_factory):
        """Setting tag to null should remove it."""
        coll = collection_factory(fields=[{"name": "lang", "type": "tag"}])
        client.put(
            f"/collections/{coll['name']}/documents/null-tag",
            json={"content": "test", "tags": {"lang": "go"}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/null-tag",
            json={"tags": {"lang": None}},
        )
        assert resp.status_code == 200
        # Tag should be removed
        tags = resp.json().get("tags") or {}
        assert "lang" not in tags

    def test_patch_only_numerics(self, client, collection_factory):
        coll = collection_factory(fields=[{"name": "rating", "type": "numeric"}])
        client.put(
            f"/collections/{coll['name']}/documents/num-patch",
            json={"content": "test", "numerics": {"rating": 5}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/num-patch",
            json={"numerics": {"rating": 10}},
        )
        assert resp.status_code == 200
        assert resp.json().get("numerics", {}).get("rating") == 10

    def test_patch_preserves_unmentioned_tags(self, client, collection_factory):
        """PATCH should merge, not replace."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
            ]
        )
        client.put(
            f"/collections/{coll['name']}/documents/merge-test",
            json={"content": "test", "tags": {"a": "1", "b": "2"}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/merge-test",
            json={"tags": {"a": "updated"}},
        )
        data = resp.json()
        assert data.get("tags", {}).get("a") == "updated"
        assert data.get("tags", {}).get("b") == "2"


@pytest.mark.p1
class TestListDocumentsP1:
    """P1 list documents edge cases."""

    def test_list_with_limit(self, client, collection_factory):
        coll = collection_factory()
        for i in range(5):
            client.put(
                f"/collections/{coll['name']}/documents/page-{i}",
                json={"content": f"doc {i}"},
            )
        resp = client.get(
            f"/collections/{coll['name']}/documents", params={"limit": 2}
        )
        data = resp.json()
        assert len(data["items"]) == 2
        assert data["has_more"] is True

    def test_cursor_pagination_complete(self, client, collection_factory):
        """Walk through all pages via cursor."""
        coll = collection_factory()
        total = 5
        for i in range(total):
            client.put(
                f"/collections/{coll['name']}/documents/cur-{i}",
                json={"content": f"doc {i}"},
            )

        all_ids = []
        cursor = None
        for _ in range(10):  # safety limit
            params = {"limit": 2}
            if cursor:
                params["cursor"] = cursor
            resp = client.get(
                f"/collections/{coll['name']}/documents", params=params
            )
            data = resp.json()
            all_ids.extend(item["id"] for item in data["items"])
            if not data["has_more"]:
                break
            cursor = data.get("next_cursor") or data.get("nextCursor")

        assert len(all_ids) == total

    def test_list_nonexistent_collection_returns_404(self, client):
        resp = client.get("/collections/nonexistent-xyz/documents")
        assert resp.status_code == 404


@pytest.mark.p1
class TestDeleteDocumentP1:
    """P1 delete edge cases."""

    def test_double_delete_returns_404(self, client, collection_factory):
        coll = collection_factory()
        client.put(
            f"/collections/{coll['name']}/documents/double-del",
            json={"content": "delete me twice"},
        )
        client.delete(f"/collections/{coll['name']}/documents/double-del")
        resp = client.delete(f"/collections/{coll['name']}/documents/double-del")
        assert resp.status_code == 404


@pytest.mark.p0
class TestUpsertDocumentHeaders:
    """PUT embedding header and location header tests."""

    def test_upsert_has_embedding_headers(self, client, collection_factory):
        """4.1.5: PUT response includes X-Embedding-Tokens header."""
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/hdr-1",
            json={"content": "embedding header test content"},
        )
        assert resp.status_code == 201
        assert_embedding_headers(resp)

    def test_upsert_has_location_header(self, client, collection_factory):
        """4.1.4: Location header present and starts with /api/v1/."""
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/loc-1",
            json={"content": "location header test"},
        )
        assert resp.status_code == 201
        assert "location" in resp.headers
        assert resp.headers["location"].startswith("/api/v1/")


@pytest.mark.p0
class TestUpsertDocumentValidation:
    """PUT validation edge cases."""

    def test_upsert_undeclared_tag_field_accepted(self, client, collection_factory):
        """4.1.9: Tag field not in schema → stored but not indexed (201)."""
        coll = collection_factory(fields=[{"name": "lang", "type": "tag"}])
        resp = client.put(
            f"/collections/{coll['name']}/documents/extra-tag",
            json={"content": "test", "tags": {"nonexistent": "value"}},
        )
        assert resp.status_code == 201


@pytest.mark.p1
class TestUpsertDocumentIdEdgeCases:
    """P1 document ID edge cases."""

    def test_doc_id_over_256_chars_returns_400(self, client, collection_factory):
        """4.1.15: Doc ID > 256 chars → 400."""
        coll = collection_factory()
        long_id = "a" * 257
        resp = client.put(
            f"/collections/{coll['name']}/documents/{long_id}",
            json={"content": "test"},
        )
        assert resp.status_code == 400

    def test_doc_id_special_chars_returns_400(self, client, collection_factory):
        """4.1.16: Special chars in doc ID → 400."""
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/bad%20id!",
            json={"content": "test"},
        )
        assert resp.status_code == 400


@pytest.mark.p0
class TestPatchDocumentMerge:
    """PATCH merge semantics (P0)."""

    def test_patch_tags_merge_keeps_existing(self, client, collection_factory):
        """4.2.3: PATCH {b:3,c:4} on existing {a:1,b:2} → {a:1,b:3,c:4}."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
                {"name": "c", "type": "tag"},
            ]
        )
        client.put(
            f"/collections/{coll['name']}/documents/merge-deep",
            json={"content": "test", "tags": {"a": "1", "b": "2"}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/merge-deep",
            json={"tags": {"b": "3", "c": "4"}},
        )
        assert resp.status_code == 200
        tags = resp.json().get("tags", {})
        assert tags.get("a") == "1"
        assert tags.get("b") == "3"
        assert tags.get("c") == "4"

    def test_patch_remove_numeric_with_null(self, client, collection_factory):
        """4.2.5: Remove numeric via null: numerics: {priority: null}."""
        coll = collection_factory(fields=[{"name": "priority", "type": "numeric"}])
        client.put(
            f"/collections/{coll['name']}/documents/rm-num",
            json={"content": "test", "numerics": {"priority": 5}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/rm-num",
            json={"numerics": {"priority": None}},
        )
        assert resp.status_code == 200
        numerics = resp.json().get("numerics") or {}
        assert "priority" not in numerics

    def test_patch_remove_nonexistent_tag_is_noop(self, client, collection_factory):
        """4.2.6: Remove nonexistent tag → no-op 200."""
        coll = collection_factory(
            fields=[
                {"name": "a", "type": "tag"},
                {"name": "b", "type": "tag"},
            ]
        )
        client.put(
            f"/collections/{coll['name']}/documents/noop-rm",
            json={"content": "test", "tags": {"a": "1"}},
        )
        resp = client.patch(
            f"/collections/{coll['name']}/documents/noop-rm",
            json={"tags": {"b": None}},
        )
        assert resp.status_code == 200
        tags = resp.json().get("tags", {})
        assert tags.get("a") == "1"


@pytest.mark.p1
class TestListDocumentsCursorP1:
    """P1 cursor pagination edge cases."""

    def test_invalid_cursor_returns_400(self, client, collection_factory):
        """4.5.6: Invalid cursor → 400."""
        coll = collection_factory()
        resp = client.get(
            f"/collections/{coll['name']}/documents",
            params={"cursor": "totally-invalid-cursor-value"},
        )
        assert resp.status_code == 400
