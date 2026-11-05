"""Native evidence for the public Search module's atomic-migration capability."""

import contextlib
import json
import os
import subprocess
import uuid
from pathlib import Path

import pytest

from test_transaction_visibility import Connection, OFFICIAL_IMAGE, ServerError, own_key, wait_for_index


def test_official_search_rejects_atomic_migration_without_moving_data(record_property):
    if os.environ.get("VECDEX_E2E_ENGINE_ASM_REPRO") != "1":
        pytest.skip("explicit isolated official fixture opt-in required")
    container = os.environ.get("VECDEX_E2E_CLUSTER_CONTAINER")
    assert container, "isolated fixture container required"
    image = subprocess.check_output(
        ["docker", "inspect", container, "--format", "{{.Config.Image}}"], text=True, timeout=10
    ).strip()
    assert image == OFFICIAL_IMAGE, image
    with contextlib.ExitStack() as cleanup:
        seed = Connection("127.0.0.1:17389")
        cleanup.callback(seed.close)
        slots = seed.command("CLUSTER", "SLOTS")
        assert len({row[2][2] for row in slots}) == 3, "fresh three-primary fixture required"
        nodes = {}
        for row in slots:
            for node in row[2:]:
                address = f"{node[0]}:{node[1]}"
                if address not in nodes:
                    nodes[address] = Connection(address)
                    cleanup.callback(nodes[address].close)
        source_row, target_row = slots[:2]
        source = nodes[f"{source_row[2][0]}:{source_row[2][1]}"]
        target = nodes[f"{target_row[2][0]}:{target_row[2][1]}"]
        index = "engine-asm-" + uuid.uuid4().hex
        prefix = index + ":indexed:"
        key = own_key(seed, prefix, source_row[0], source_row[1])
        slot = seed.command("CLUSTER", "KEYSLOT", key)
        modules = {address: node.command("INFO", "modules") for address, node in nodes.items()}
        assert all("module:name=search,ver=66049," in value for value in modules.values()), modules
        seed.command("FT.CREATE", index, "ON", "HASH", "PREFIX", 1, prefix, "SCHEMA", "status", "TAG")
        try:
            wait_for_index(nodes, index)
            source.command("HSET", key, "status", "stored")
            query = ["FT.SEARCH", index, "@status:{stored}", "ALLSHARDS", "CONSISTENT", "NOCONTENT", "LIMIT", 0, 10]
            # One active writer is the separately tested public configuration workaround.
            before = seed.command(*query)
            assert before == [1, key], before
            command = ["CLUSTER", "MIGRATESLOTS", "SLOTSRANGE", slot, slot, "NODE", target.command("CLUSTER", "MYID")]
            with pytest.raises(ServerError) as rejected:
                source.command(*command)
            expected = (
                "ERR The module search does not support atomic slot migrations. "
                "Please ensure all modules have declared support for atomic slot migration and try again"
            )
            assert str(rejected.value) == expected, str(rejected.value)
            after = seed.command(*query)
            stored = source.command("HGET", key, "status")
            migrations = source.command("CLUSTER", "GETSLOTMIGRATIONS")
            assert stored == "stored" and before == after and migrations == []
            assert seed.command("CLUSTER", "SLOTS") == slots, "rejected ASM changed slot ownership"
            evidence = {
                "image": image, "core": source.command("INFO", "server"), "modules": modules,
                "command": command, "server_error": expected, "native_query": query,
                "search_before": before, "search_after": after, "stored_value_after": stored,
                "migration_jobs_after": migrations, "slot_ownership_unchanged": True,
                "vecdex_or_sdk_used": False,
            }
            report = Path(__file__).resolve().parents[2] / "reports" / "engine-asm-capability.json"
            report.parent.mkdir(parents=True, exist_ok=True)
            report.write_text(json.dumps(evidence, indent=2) + "\n")
            record_property("engine_evidence", json.dumps(evidence, sort_keys=True))
        finally:
            seed.command("FT.DROPINDEX", index)
            source.command("DEL", key)
