"""Document lifecycle contracts exercised only through MCP tools."""
import concurrent.futures
import os
import time

import pytest

from conftest import eventually
from mcp_client import write_key

pytestmark = pytest.mark.p0


def test_create_read_cas_delete_and_replay(mcp, collection_factory):
    context = collection_factory()["context_id"]
    key = write_key()
    first = mcp.create(context, "record", "original", idempotency_key=key)
    assert first["id"] == "record" and first["version"]
    assert mcp.create(context, "record", "original", idempotency_key=key)["version"] == first["version"]
    mcp.error("memory_create", {"context_id": context, "id": "record", "content": "changed", "idempotency_key": key}, "idempotency_conflict")
    mcp.error("memory_create", {"context_id": context, "id": "record", "content": "duplicate", "idempotency_key": write_key()}, "already_exists")
    assert mcp.read(context, "record")["content"] == "original"
    updated = mcp.update(context, "record", first["version"], content="corrected")
    assert updated["version"] != first["version"]
    conflict = mcp.error("memory_update", {"context_id": context, "id": "record", "if_version": first["version"], "content": "stale", "idempotency_key": write_key()}, "version_conflict")
    assert conflict["current_version"] == updated["version"]
    delete_key = write_key()
    mcp.delete(context, "record", updated["version"], idempotency_key=delete_key)
    mcp.delete(context, "record", updated["version"], idempotency_key=delete_key)
    mcp.error("memory_read", {"context_id": context, "id": "record"}, "not_found")
    mcp.create(context, "record", "original", idempotency_key=key)
    mcp.error("memory_read", {"context_id": context, "id": "record"}, "not_found")


def test_exact_json_metadata_and_merge_patch(mcp, collection_factory):
    context = collection_factory()["context_id"]
    metadata = {"large": 9007199254740993, "nested": {"keep": True, "remove": "x"}, "array": [1, None, {"v": "中文"}], "null": None}
    first = mcp.create(context, "metadata", "source", metadata=metadata)
    assert "metadata" not in mcp.read(context, "metadata")
    assert mcp.read(context, "metadata", include_metadata=True)["metadata"] == metadata
    second = mcp.update(context, "metadata", first["version"], metadata={"nested": {"remove": None, "new": 2}, "array": [3]})
    expected = {**metadata, "nested": {"keep": True, "new": 2}, "array": [3]}
    assert mcp.read(context, "metadata", include_metadata=True)["metadata"] == expected
    third = mcp.update(context, "metadata", second["version"], content="new source")
    assert mcp.read(context, "metadata", include_metadata=True)["metadata"] == expected
    projected = mcp.read(context, "metadata", include_content=False, metadata_fields=["large", "nested"])
    assert "content" not in projected
    assert projected["metadata"] == {key: expected[key] for key in ("large", "nested")}
    mcp.update(context, "metadata", third["version"], metadata=None)
    assert mcp.read(context, "metadata", include_metadata=True).get("metadata") in ({}, None)


def test_utf8_full_source_pages_are_version_bound(mcp, collection_factory):
    context = collection_factory()["context_id"]
    content = "Начало 中文 🚀\n" * 2300 + "tailwordfragment"
    first = mcp.create(context, "long", content)
    assert mcp.read_all(context, "long", max_bytes=777)["content"] == content
    page = mcp.read(context, "long", max_bytes=777)
    assert len(page["content"].encode()) <= 777
    assert page["next_offset"] == len(page["content"].encode())
    mcp.error("memory_read", {"context_id": context, "id": "long", "offset": page["next_offset"]}, "invalid_argument")
    mcp.update(context, "long", first["version"], content="short replacement")
    mcp.error("memory_read", {"context_id": context, "id": "long", "offset": page["next_offset"], "version": first["version"]}, "version_conflict")
    assert mcp.read_all(context, "long")["content"] == "short replacement"


def test_list_cursor_metadata_and_collection_isolation(mcp, collection_factory):
    a, b = collection_factory(), collection_factory()
    for index in range(7):
        mcp.create(a["context_id"], f"doc-{index}", f"source {index}", metadata={"ordinal": index})
    mcp.create(b["context_id"], "doc-0", "other collection")
    seen, cursor = [], None
    while True:
        args = {"context_id": a["context_id"], "limit": 2, "include_metadata": True}
        if cursor:
            args["cursor"] = cursor
        page = mcp.call("memory_list", args)
        assert len(page["items"]) <= 2
        for item in page["items"]:
            assert item["version"] and "content" not in item
            assert "ordinal" in item["metadata"]
        seen.extend(item["id"] for item in page["items"])
        cursor = page.get("next_cursor")
        if not cursor:
            break
    assert len(seen) == len(set(seen)) == 7
    assert mcp.read(b["context_id"], "doc-0")["content"] == "other collection"


def test_expiry_clear_replay_and_aba(mcp, collection_factory):
    context = collection_factory()["context_id"]
    key = write_key()
    expiry = int(time.time() * 1000) + 800
    first = mcp.create(context, "expires", "short lived", expires_at=expiry, idempotency_key=key)
    assert mcp.read(context, "expires")["expires_at"] == expiry
    def gone():
        mcp.error("memory_read", {"context_id": context, "id": "expires"}, "not_found")
    eventually(gone)
    replay = mcp.create(context, "expires", "short lived", expires_at=expiry, idempotency_key=key)
    assert replay["version"] == first["version"]
    gone()
    second = mcp.create(context, "expires", "new lifetime", expires_at=int(time.time() * 1000) + 60000)
    assert second["version"] != first["version"]
    mcp.error("memory_delete", {"context_id": context, "id": "expires", "if_version": first["version"], "idempotency_key": write_key()}, "version_conflict")
    mcp.update(context, "expires", second["version"], expires_at=None)
    assert mcp.read(context, "expires")["expires_at"] == 0
    mcp.create(context, "past", "already expired", expires_at=int(time.time() * 1000) - 1000)
    mcp.error("memory_read", {"context_id": context, "id": "past"}, "not_found")


def test_concurrent_cas_and_identical_retry(mcp, collection_factory):
    context = collection_factory()["context_id"]
    key = write_key()
    def create(_):
        args = {"context_id": context, "id": "race", "content": "original", "idempotency_key": key}
        for _ in range(20):
            result = mcp.result("memory_create", args)["structuredContent"]
            if result["ok"]:
                return result["data"]
            assert result["error"]["code"] == "operation_in_progress" and result["error"]["retryable"]
            time.sleep(0.05)
        raise AssertionError("identical mutation did not finish")
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        receipts = list(pool.map(create, range(8)))
    assert len({receipt["version"] for receipt in receipts}) == 1
    version = receipts[0]["version"]
    def update(index):
        return mcp.result("memory_update", {"context_id": context, "id": "race", "if_version": version, "content": f"winner {index}", "idempotency_key": write_key()})["structuredContent"]
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        results = list(pool.map(update, range(8)))
    assert sum(result["ok"] for result in results) == 1
    assert {result["error"]["code"] for result in results if not result["ok"]} == {"version_conflict"}


@pytest.mark.parametrize("fields", [
    {"id": "bad/slash"}, {"id": ""}, {"content": ""}, {"content": "x" * (160 * 1024 + 1)},
    {"metadata": [1, 2]}, {"metadata": {"huge": "x" * (64 * 1024 + 1)}},
    {"expires_at": -1}, {"unexpected": True}, {"idempotency_key": ""},
])
def test_invalid_memory_inputs(mcp, collection_factory, fields):
    args = {"context_id": collection_factory()["context_id"], "id": "valid", "content": "valid content", "idempotency_key": write_key(), **fields}
    mcp.error("memory_create", args, "invalid_argument")


@pytest.mark.skipif(os.getenv("VECDEX_EMBEDDING_BACKEND") == "onnx", reason="failure marker belongs to the mock provider")
def test_provider_failure_preserves_complete_source(mcp, collection_factory):
    context = collection_factory()["context_id"]
    first = mcp.create(context, "atomic", "original", metadata={"keep": True})
    failed = "safe fragment " * 4000 + " MOCK_FAIL_FRAGMENT"
    mcp.error("memory_update", {"context_id": context, "id": "atomic", "if_version": first["version"], "content": failed, "metadata": {"keep": False}, "idempotency_key": write_key()}, "temporarily_unavailable")
    after = mcp.read_all(context, "atomic")
    assert (after["version"], after["content"], after["metadata"]) == (first["version"], "original", {"keep": True})


def test_update_receipt_replay_after_delete(mcp, collection_factory):
    context = collection_factory()["context_id"]
    first = mcp.create(context, "receipt", "original")
    key = write_key()
    updated = mcp.update(context, "receipt", first["version"], content="corrected", idempotency_key=key)
    assert not updated["replayed"]
    replay = mcp.update(context, "receipt", first["version"], content="corrected", idempotency_key=key)
    assert replay["replayed"] and replay["version"] == updated["version"]
    mcp.error("memory_update", {"context_id": context, "id": "receipt", "if_version": first["version"], "content": "different", "idempotency_key": key}, "idempotency_conflict")
    mcp.delete(context, "receipt", updated["version"])
    replay = mcp.update(context, "receipt", first["version"], content="corrected", idempotency_key=key)
    assert replay["replayed"] and replay["version"] == updated["version"]
    mcp.error("memory_read", {"context_id": context, "id": "receipt"}, "not_found")


def test_document_cursor_rejects_other_collection(mcp, collection_factory):
    a, b = collection_factory(), collection_factory()
    for name in ("one", "two"):
        mcp.create(a["context_id"], name, "cursor evidence " + name)
    page = mcp.call("memory_list", {"context_id": a["context_id"], "limit": 1})
    assert page["next_cursor"]
    mcp.error("memory_list", {"context_id": b["context_id"], "cursor": page["next_cursor"], "limit": 1}, "invalid_argument")
