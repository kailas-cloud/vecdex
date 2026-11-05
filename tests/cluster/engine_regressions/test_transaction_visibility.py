"""Native engine-only regression: no Vecdex server, SDK or document protocol."""

import contextlib
import json
import os
import socket
import subprocess
import time
import uuid
from pathlib import Path

import pytest


class ServerError(RuntimeError):
    pass


class MissingIndexedSources(AssertionError):
    """Only the proved engine invariant may turn into an expected failure."""


OFFICIAL_IMAGE = "valkey/valkey-bundle:9.1.2@sha256:1c39295cd8e3a34fc33fb46de235cf5e7bad530525b1a697eaf9f49e1c42747c"


class Connection:
    def __init__(self, address):
        host, port = address.rsplit(":", 1)
        self.socket = socket.create_connection((host, int(port)), timeout=5)
        self.file = self.socket.makefile("rb")

    def close(self):
        self.file.close()
        self.socket.close()

    def command(self, *args):
        encoded = [str(arg).encode() for arg in args]
        self.socket.sendall(b"*%d\r\n" % len(encoded) + b"".join(
            b"$%d\r\n" % len(arg) + arg + b"\r\n" for arg in encoded))
        return self.read()

    def read(self):
        line = self.file.readline()
        if not line.endswith(b"\r\n"):
            raise ConnectionError("incomplete native RESP reply")
        kind, value = line[:1], line[1:-2]
        if kind == b"-":
            raise ServerError(value.decode())
        if kind == b"+":
            return value.decode()
        if kind == b":":
            return int(value)
        if kind == b"$":
            length = int(value)
            if length == -1:
                return None
            payload = self.file.read(length + 2)
            assert len(payload) == length + 2 and payload[-2:] == b"\r\n"
            return payload[:-2].decode()
        if kind == b"*":
            length = int(value)
            return None if length == -1 else [self.read() for _ in range(length)]
        raise AssertionError(f"unsupported native RESP type {kind!r}")


def pairs(items):
    assert len(items) % 2 == 0, items
    return dict(zip(items[::2], items[1::2], strict=True))


def info(text):
    return dict(line.split(":", 1) for line in text.splitlines()
                if line and not line.startswith("#") and ":" in line)


def wait_for_index(nodes, index):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        ready = True
        for node in nodes.values():
            try:
                fields = pairs(node.command("FT.INFO", index, "LOCAL"))
                ready &= fields["state"] == "ready" and fields["backfill_in_progress"] == "0"
            except ServerError as error:
                if "not found" not in str(error).lower():
                    raise
                ready = False
        if ready:
            return
        time.sleep(0.05)
    pytest.fail("native index schema did not converge before reproducing the defect")


def own_key(seed, prefix, start, end):
    for counter in range(100000):
        key = f"{prefix}{{native-{counter}}}"
        if start <= seed.command("CLUSTER", "KEYSLOT", key) <= end:
            return key
    pytest.fail("cannot find a native fixture key in the primary slot range")


@pytest.mark.parametrize("writers", [1, 2], ids=["supported-config-workaround", "upstream-1651"])
def test_small_transaction_is_indexed_without_local_search(writers, request, record_property):
    addresses = os.environ.get("VECDEX_E2E_VALKEY_CLUSTER_ADDRS", "")
    if not addresses:
        pytest.skip("explicit isolated Search cluster addresses required")
    if os.environ.get("VECDEX_E2E_ENGINE_REPRO") != "1":
        pytest.skip("explicit opt-in to temporary fixture CONFIG SET required")
    container = os.environ.get("VECDEX_E2E_CLUSTER_CONTAINER")
    if not container:
        pytest.skip("isolated official fixture container required for image provenance")
    image = subprocess.check_output(
        ["docker", "inspect", container, "--format", "{{.Config.Image}}"], text=True, timeout=10).strip()
    assert image == OFFICIAL_IMAGE, f"engine evidence requires the pinned public image, got {image}"
    with contextlib.ExitStack() as cleanup:
        seed = Connection(addresses.split(",")[0])
        cleanup.callback(seed.close)
        slots = seed.command("CLUSTER", "SLOTS")
        nodes, primaries = {}, {}
        for row in slots:
            for descriptor in row[2:]:
                address = f"{descriptor[0]}:{descriptor[1]}"
                if address not in nodes:
                    nodes[address] = Connection(address)
                    cleanup.callback(nodes[address].close)
            primaries[f"{row[2][0]}:{row[2][1]}"] = (row[0], row[1])
        assert len(primaries) >= 3, "three primaries required for the engine reproducer"
        assert len(nodes) >= 6, "replicas required to cover coordinator target selection"
        versions = {address: info(node.command("INFO", "modules")) for address, node in nodes.items()}
        known_version = all("module:name=search,ver=66049," in node.command("INFO", "modules")
                            for node in nodes.values())
        if writers == 2 and known_version:
            request.node.add_marker(pytest.mark.xfail(
                strict=True, raises=MissingIndexedSources,
                reason="Search 1.2.1 #1651: MULTI/EXEC mutations below writer-threads remain unindexed; "
                       "native HGET proves hashes exist. https://github.com/valkey-io/valkey-search/issues/1651"))
        for node in nodes.values():
            previous = pairs(node.command("CONFIG", "GET", "search.writer-threads"))["search.writer-threads"]
            node.command("CONFIG", "SET", "search.writer-threads", writers)
            cleanup.callback(node.command, "CONFIG", "SET", "search.writer-threads", previous)
        index = "engine-native-" + uuid.uuid4().hex
        prefix = index + ":indexed:"
        seed.command("FT.CREATE", index, "ON", "HASH", "PREFIX", 1, prefix, "SCHEMA", "status", "TAG")
        cleanup.callback(seed.command, "FT.DROPINDEX", index)
        wait_for_index(nodes, index)
        keys = []
        for address, (start, end) in primaries.items():
            node = nodes[address]
            key = own_key(seed, prefix, start, end)
            marker = key.replace(":indexed:", ":marker:")
            node.command("MULTI")
            assert node.command("HSET", key, "status", "run") == "QUEUED"
            assert node.command("HSET", marker, "commit", "1") == "QUEUED"
            assert node.command("EXEC") == [1, 1]
            cleanup.callback(node.command, "DEL", key, marker)
            assert node.command("HGET", key, "status") == "run", "source key lost before search"
            assert node.command("HGET", marker, "commit") == "1", "transaction marker missing"
            keys.append(key)
        time.sleep(1)
        # No FT.SEARCH has run: it would drain the local private MULTI queue.
        snapshots = {address: pairs(node.command("FT.INFO", index, "LOCAL"))
                     for address, node in nodes.items()}
        for address, fields in snapshots.items():
            assert fields["num_docs"] == 1, (address, fields)
            assert all(fields[field] == "0" for field in
                       ["backfill_in_progress", "mutation_queue_size", "hash_indexing_failures"]), (address, fields)
            assert fields["state"] == "ready", (address, fields)
        reply = seed.command("FT.SEARCH", index, "@status:{run}",
                             "ALLSHARDS", "CONSISTENT", "NOCONTENT", "LIMIT", 0, 100)
        evidence = {"image": image, "index": index, "writer_threads": writers, "keys": keys,
                    "node_versions": versions, "local_indexes_before_search": snapshots,
                    "native_search_reply": reply, "expected_count": len(keys),
                    "commands": ["MULTI", "HSET indexed-key status run", "HSET same-slot-marker commit 1", "EXEC",
                                 "HGET indexed-key status", "FT.INFO index LOCAL",
                                 "FT.SEARCH index @status:{run} ALLSHARDS CONSISTENT NOCONTENT LIMIT 0 100"]}
        record_property("engine_evidence", json.dumps(evidence, sort_keys=True))
        report = Path(__file__).resolve().parents[2] / "reports" / f"engine-multi-{writers}.json"
        report.parent.mkdir(parents=True, exist_ok=True)
        report.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n")
        stale = {address: fields for address, fields in snapshots.items() if int(fields["num_records"]) != 1}
        if reply[0] != len(keys) or stale:
            raise MissingIndexedSources(
                f"native committed hashes exist, search returned {reply[0]}/{len(keys)}; "
                f"unindexed participants={list(stale)}; evidence={report}")
