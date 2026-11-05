"""Keep a native key in IMPORTING/MIGRATING to isolate the search handoff gap."""

import json
import os
import subprocess
import time
import uuid
from pathlib import Path

import pytest

from test_transaction_visibility import Connection, OFFICIAL_IMAGE, ServerError, info, own_key, pairs, wait_for_index


class MissingTransferredSource(AssertionError):
    """The imported hash exists, but successful fanout omits the source."""


def wait_record_counts(nodes, index, expected):
    deadline = time.monotonic() + 10
    snapshots = {}
    while time.monotonic() < deadline:
        snapshots = {address: pairs(node.command("FT.INFO", index, "LOCAL")) for address, node in nodes.items()}
        if all(int(fields["num_records"]) == expected.get(address, 0)
               and fields["state"] == "ready"
               and all(fields[name] == "0" for name in
                       ["backfill_in_progress", "mutation_queue_size", "hash_indexing_failures"])
               for address, fields in snapshots.items()):
            return snapshots
        time.sleep(0.05)
    pytest.fail(f"native indexes did not reach the known handoff state: {snapshots}")


def test_fanout_rejects_an_incomplete_slot_handoff(request, record_property):
    if os.environ.get("VECDEX_E2E_ENGINE_HANDOFF_REPRO") != "1":
        pytest.skip("separate opt-in to native destructive slot handoff required")
    container = os.environ.get("VECDEX_E2E_CLUSTER_CONTAINER")
    assert container, "isolated public fixture required"
    image = subprocess.check_output(
        ["docker", "inspect", container, "--format", "{{.Config.Image}}"], text=True, timeout=10).strip()
    assert image == OFFICIAL_IMAGE, image
    nodes = {}
    try:
        seed = Connection("127.0.0.1:17389")
        nodes["127.0.0.1:17389"] = seed
        slots = seed.command("CLUSTER", "SLOTS")
        primaries = {row[2][2] for row in slots}
        assert len(primaries) == 3, "fresh three-primary fixture required"
        for row in slots:
            for descriptor in row[2:]:
                address = f"{descriptor[0]}:{descriptor[1]}"
                if address not in nodes:
                    nodes[address] = Connection(address)
        modules = {address: node.command("INFO", "modules") for address, node in nodes.items()}
        assert all(pairs(node.command("CONFIG", "GET", "search.writer-threads"))["search.writer-threads"] == "1"
                   for node in nodes.values()), "use the public one-writer workaround fixture"
        if all("module:name=search,ver=66049," in raw for raw in modules.values()):
            request.node.add_marker(pytest.mark.xfail(
                strict=True, raises=MissingTransferredSource,
                reason="Search 1.2.1 ALLSHARDS CONSISTENT omits a stored importing source before slot adoption; "
                       "native hash/local-index/fanout evidence in tests/reports/engine-handoff.json"))
        index = "engine-handoff-" + uuid.uuid4().hex
        prefix = index + ":indexed:"
        source_row = next(row for row in slots if row[2][1] == 17389)
        key = own_key(seed, prefix, source_row[0], source_row[1])
        source_id = seed.command("CLUSTER", "MYID")
        slot = seed.command("CLUSTER", "KEYSLOT", key)
        seed.command("FT.CREATE", index, "ON", "HASH", "PREFIX", 1, prefix, "SCHEMA", "status", "TAG")
        wait_for_index(nodes, index)
        assert seed.command("HSET", key, "status", "run") == 1
        assert seed.command("WAIT", 1, 2000) >= 1
        source_addresses = {f"{descriptor[0]}:{descriptor[1]}" for descriptor in source_row[2:]}
        before = wait_record_counts(nodes, index, dict.fromkeys(source_addresses, 1))
        query = ("FT.SEARCH", index, "@status:{run}", "ALLSHARDS", "CONSISTENT", "NOCONTENT", "LIMIT", 0, 10)
        assert seed.command(*query) == [1, key]
        joined = subprocess.run(["docker", "exec", container, "valkey-cli", "--cluster", "add-node",
                                 "127.0.0.1:17395", "127.0.0.1:17389"],
                                capture_output=True, text=True, timeout=30, check=True)
        target = Connection("127.0.0.1:17395")
        nodes["127.0.0.1:17395"] = target
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            states = [info(node.command("CLUSTER", "INFO")) for node in nodes.values()]
            if all(state.get("cluster_state") == "ok" and state.get("cluster_known_nodes") == "7"
                   and state.get("cluster_slots_ok") == "16384" for state in states):
                break
            time.sleep(0.05)
        else:
            pytest.fail("new importing primary did not join the complete seven-node view")
        target_id = target.command("CLUSTER", "MYID")
        assert target.command("CLUSTER", "SETSLOT", slot, "IMPORTING", source_id) == "OK"
        assert seed.command("CLUSTER", "SETSLOT", slot, "MIGRATING", target_id) == "OK"
        assert seed.command("MIGRATE", "127.0.0.1", 17395, key, 0, 5000) == "OK"
        assert seed.command("WAIT", 1, 2000) >= 1
        assert target.command("ASKING") == "OK"
        assert target.command("HGET", key, "status") == "run"
        after = wait_record_counts({address: node for address, node in nodes.items()
                                    if address != "127.0.0.1:17395"}, index, {})
        # A new zero-slot primary may not receive the distributed schema until
        # slot adoption. Capture that engine state rather than masking it with
        # a premature readiness wait or manually creating a second schema.
        local_reply, local_error = None, None
        try:
            after["127.0.0.1:17395"] = pairs(target.command("FT.INFO", index, "LOCAL"))
            local_reply = target.command("FT.SEARCH", index, "@status:{run}", "LOCALONLY", "NOCONTENT", "LIMIT", 0, 10)
        except ServerError as error:
            if "not found" not in str(error).lower():
                raise
            local_error = str(error)
        reply, rejected = None, None
        try:
            reply = seed.command(*query)
        except ServerError as error:
            # A consistency failure is acceptable during handoff; an unknown
            # command, bad index, transport failure or unrelated error is not.
            if not any(word in str(error).lower() for word in ["consistency", "fingerprint", "tryagain", "topology"]):
                raise
            rejected = str(error)
        evidence = {"image": image, "modules": modules, "index": index, "key": key, "slot": slot,
                    "source_id": source_id, "target_id": target_id,
                    "join_output": joined.stdout, "before": before, "after": after,
                    "target_hget": "run", "target_local_search": local_reply, "target_local_error": local_error,
                    "distributed_query": query, "distributed_reply": reply, "distributed_rejection": rejected,
                    "cluster_nodes": seed.command("CLUSTER", "NODES"),
                    "commands": ["HSET key status run", "WAIT 1 2000",
                                 "CLUSTER SETSLOT slot IMPORTING source-id (target)",
                                 "CLUSTER SETSLOT slot MIGRATING target-id (source)",
                                 "MIGRATE 127.0.0.1 17395 key 0 5000", "WAIT 1 2000",
                                 "ASKING; HGET key status (target)", "FT.INFO index LOCAL (all nodes)",
                                 "FT.SEARCH index @status:{run} LOCALONLY NOCONTENT LIMIT 0 10 (target)",
                                 "FT.SEARCH index @status:{run} ALLSHARDS CONSISTENT NOCONTENT LIMIT 0 10 (source)"]}
        report = Path(__file__).resolve().parents[2] / "reports" / "engine-handoff.json"
        report.parent.mkdir(parents=True, exist_ok=True)
        report.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n")
        record_property("engine_evidence", json.dumps(evidence, sort_keys=True))
        if reply is not None and reply != [1, key]:
            raise MissingTransferredSource(f"stored importing source omitted from native fanout: {reply}; {report}")
        # The open slot is deliberate evidence. This disposable fixture must be
        # recreated afterward, rather than hiding the failure by finishing handoff.
    finally:
        for node in nodes.values():
            node.close()
