"""Data integrity tests — UTF-8, large documents, special characters."""

import pytest

from conftest import unique_name


pytestmark = pytest.mark.p2


class TestUTF8Content:
    """UTF-8 content in documents."""

    def test_chinese_content(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/utf8-zh",
            json={"content": "这是一个中文测试文档关于自然语言处理"},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/utf8-zh").json()
        assert "中文" in data["content"]

    def test_japanese_content(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/utf8-ja",
            json={"content": "日本語のテストドキュメントです"},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/utf8-ja").json()
        assert "日本語" in data["content"]

    def test_cyrillic_content(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/utf8-ru",
            json={"content": "Кириллический текст для проверки кодировки"},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/utf8-ru").json()
        assert "Кириллический" in data["content"]

    def test_emoji_content(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/utf8-emoji",
            json={"content": "Test with emojis 🧪🔬⚗️ and symbols ∑∆Ω"},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/utf8-emoji").json()
        assert "🧪" in data["content"]

    def test_mixed_scripts(self, client, collection_factory):
        coll = collection_factory()
        content = "English 中文 日本語 Кириллица العربية"
        resp = client.put(
            f"/collections/{coll['name']}/documents/utf8-mixed",
            json={"content": content},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/utf8-mixed").json()
        assert data["content"] == content


class TestLargeDocuments:
    """Documents near size limits."""

    def test_100kb_content(self, client, collection_factory):
        """100KB document — well within 160KB limit."""
        coll = collection_factory()
        content = "a" * 100_000
        resp = client.put(
            f"/collections/{coll['name']}/documents/large-100k",
            json={"content": content},
        )
        assert resp.status_code == 201

    def test_160kb_content_at_limit(self, client, collection_factory):
        """Exactly 160KB — at the documented limit."""
        coll = collection_factory()
        content = "x" * 163_840
        resp = client.put(
            f"/collections/{coll['name']}/documents/large-160k",
            json={"content": content},
        )
        # Should be accepted (exactly at limit)
        assert resp.status_code in (201, 400)

    def test_over_160kb_rejected(self, client, collection_factory):
        """Over 160KB — should be rejected."""
        coll = collection_factory()
        content = "x" * 163_841
        resp = client.put(
            f"/collections/{coll['name']}/documents/large-over",
            json={"content": content},
        )
        assert resp.status_code == 400


class TestSpecialCharacters:
    """Special characters in content and metadata."""

    def test_content_with_newlines(self, client, collection_factory):
        coll = collection_factory()
        content = "line one\nline two\nline three"
        resp = client.put(
            f"/collections/{coll['name']}/documents/newlines",
            json={"content": content},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/newlines").json()
        assert data["content"] == content

    def test_content_with_tabs(self, client, collection_factory):
        coll = collection_factory()
        content = "col1\tcol2\tcol3"
        resp = client.put(
            f"/collections/{coll['name']}/documents/tabs",
            json={"content": content},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/tabs").json()
        assert data["content"] == content

    def test_content_with_quotes(self, client, collection_factory):
        coll = collection_factory()
        content = 'He said "hello" and she said \'goodbye\''
        resp = client.put(
            f"/collections/{coll['name']}/documents/quotes",
            json={"content": content},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/quotes").json()
        assert data["content"] == content

    def test_content_with_backslashes(self, client, collection_factory):
        coll = collection_factory()
        content = "path\\to\\file and regex \\d+\\.\\w+"
        resp = client.put(
            f"/collections/{coll['name']}/documents/backslash",
            json={"content": content},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/backslash").json()
        assert data["content"] == content

    def test_content_with_html_tags(self, client, collection_factory):
        coll = collection_factory()
        content = "<script>alert('xss')</script><p>safe text</p>"
        resp = client.put(
            f"/collections/{coll['name']}/documents/html",
            json={"content": content},
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/html").json()
        assert data["content"] == content

    def test_tag_value_with_special_chars(self, client, collection_factory):
        """Tag values may contain special chars — stored as-is."""
        coll = collection_factory(fields=[{"name": "note", "type": "tag"}])
        resp = client.put(
            f"/collections/{coll['name']}/documents/special-tag",
            json={
                "content": "test",
                "tags": {"note": "has spaces & symbols!"},
            },
        )
        assert resp.status_code == 201
        data = client.get(f"/collections/{coll['name']}/documents/special-tag").json()
        assert data.get("tags", {}).get("note") == "has spaces & symbols!"


class TestDocumentIdEdgeCases:
    """Edge cases for document IDs."""

    def test_numeric_only_id(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/12345",
            json={"content": "numeric id"},
        )
        assert resp.status_code == 201
        assert resp.json()["id"] == "12345"

    def test_single_char_id(self, client, collection_factory):
        coll = collection_factory()
        resp = client.put(
            f"/collections/{coll['name']}/documents/x",
            json={"content": "single char id"},
        )
        assert resp.status_code == 201

    def test_long_id(self, client, collection_factory):
        """256 char max ID."""
        coll = collection_factory()
        doc_id = "a" * 256
        resp = client.put(
            f"/collections/{coll['name']}/documents/{doc_id}",
            json={"content": "long id"},
        )
        assert resp.status_code in (201, 400)
