"""Durable owner-scoped contexts and generation-bound collection lifecycle."""
import datetime

import pytest

from conftest import close_context, eventually, unique_name
from mcp_client import MCPClient, write_key

pytestmark = pytest.mark.p0


def test_context_reconnect_cas_receipts_and_no_resurrection(mcp, collection_factory):
    col = collection_factory()
    args = {"name": "durable", "collection": col["name"], "generation": col["generation"], "idempotency_key": write_key()}
    first = mcp.call("context_create", args)
    context = first["context"]
    before = datetime.datetime.fromisoformat(context["created_at"].replace("Z", "+00:00"))
    expires = datetime.datetime.fromisoformat(context["expires_at"].replace("Z", "+00:00"))
    assert expires - before == datetime.timedelta(days=7)
    with MCPClient(mcp.base_url, mcp.api_key) as resumed:
        read = resumed.call("context_get", {"context_id": context["context_id"]})
        assert read["version"] == context["version"] and read["expires_at"] == context["expires_at"]
    update_args = {"context_id": context["context_id"], "expected_version": context["version"], "name": "new name", "expires_at": None, "idempotency_key": write_key()}
    update = mcp.call("context_update", update_args)
    assert update["context"]["expires_at"] is None
    replay = mcp.call("context_create", args)
    assert replay["replayed"] and replay["context"]["version"] == context["version"]
    assert replay["current_version"] == update["context"]["version"]
    mcp.error("context_update", {**update_args, "idempotency_key": write_key()}, "version_conflict")
    mcp.error("context_create", {**args, "name": "changed"}, "idempotency_conflict")
    close_context(mcp, context["context_id"])
    for tool, request in (("context_create", args), ("context_update", update_args)):
        replay = mcp.call(tool, request)
        assert replay["replayed"] and replay["current_state"] == "absent"
    mcp.error("context_get", {"context_id": context["context_id"]}, "not_found")


def test_context_expiry_absolute(mcp, collection_factory):
    col = collection_factory()
    at = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=1)
    args = {"name": "ephemeral", "collection": col["name"], "generation": col["generation"], "expires_at": at.isoformat(), "idempotency_key": write_key()}
    first = mcp.call("context_create", args)
    context = first["context"]
    assert mcp.call("context_get", {"context_id": context["context_id"]})["expires_at"] == context["expires_at"]
    eventually(lambda: mcp.error("context_get", {"context_id": context["context_id"]}, "not_found"))
    replay = mcp.call("context_create", args)
    assert replay["replayed"] and replay["current_state"] == "absent"


def test_collection_generation_fences_context_and_resource(mcp, collection_factory):
    col = collection_factory()
    context = col["context_id"]
    source = mcp.create(context, "record", "old generation")
    context_args = {"name": "receipt", "collection": col["name"], "generation": col["generation"], "idempotency_key": write_key()}
    receipt = mcp.call("context_create", context_args)
    mcp.error("collection_delete", {"name": col["name"], "generation": col["generation"], "confirm_name": "wrong"}, "invalid_argument")
    mcp.call("collection_delete", {"name": col["name"], "generation": col["generation"], "confirm_name": col["name"]})
    assert mcp.call("context_get", {"context_id": context})["stale"]
    mcp.error("memory_read", {"context_id": context, "id": "record"}, "stale_context")
    assert mcp.call("context_create", context_args)["context"]["context_id"] == receipt["context"]["context_id"]
    close_context(mcp, receipt["context"]["context_id"])
    new = collection_factory(name=col["name"])
    assert new["generation"] != col["generation"]
    mcp.error("memory_create", {"context_id": context, "id": "record", "content": "wrong generation", "idempotency_key": write_key()}, "stale_context")
    response = mcp.wire("resources/read", {"uri": source["uri"]})
    assert "error" in response


def test_context_catalog_pagination_and_pins(mcp, collection_factory, context_factory):
    col = collection_factory()
    document = mcp.create(col["context_id"], "pin", "evidence")
    contexts = [context_factory(col, pins=[{"document_id": "pin", "version": document["version"]}], expires_at=None) for _ in range(3)]
    seen, cursor = set(), None
    for _ in range(1000):
        page = mcp.call("context_list", {"limit": 2, **({"cursor": cursor} if cursor else {})})
        assert len(page["items"]) <= 2
        for context in page["items"]:
            assert context["context_id"] not in seen
            seen.add(context["context_id"])
        cursor = page.get("next_cursor")
        if not cursor:
            break
    assert {context["context_id"] for context in contexts} <= seen
    read = mcp.call("context_get", {"context_id": contexts[0]["context_id"]})
    assert read["pins"] == [{"document_id": "pin", "version": document["version"]}]
    assert "content" not in str(read["pins"])


@pytest.mark.parametrize("fields", [
    {"generation": "wrong"}, {"name": ""}, {"pins": [{"document_id": "x", "version": "v"}] * 101},
    {"search": {"mode": "unknown"}}, {"expires_at": "yesterday"}, {"idempotency_key": ""},
])
def test_context_invalid_input(mcp, collection_factory, fields):
    col = collection_factory()
    result = mcp.error("context_create", {"name": "valid", "collection": col["name"], "generation": col["generation"], "idempotency_key": write_key(), **fields})
    assert result["code"] in ("invalid_argument", "stale_context")


def test_verify_pins_exposes_current_stale_and_missing(mcp, collection_factory, context_factory):
    col = collection_factory()
    current = mcp.create(col["context_id"], "current", "current evidence")
    changed = mcp.create(col["context_id"], "changed", "old evidence")
    updated = mcp.update(col["context_id"], "changed", changed["version"], content="new evidence")
    context = context_factory(col, pins=[{"document_id": "current", "version": current["version"]}, {"document_id": "changed", "version": changed["version"]}, {"document_id": "missing", "version": "old-version"}])
    result = mcp.call("context_get", {"context_id": context["context_id"], "verify_pins": True})
    states = {pin["document_id"]: pin for pin in result["pin_states"]}
    assert {key: value["state"] for key, value in states.items()} == {"current": "current", "changed": "stale", "missing": "missing"}
    assert states["changed"]["current_version"] == updated["version"]
    assert states["changed"]["version"] == changed["version"]
