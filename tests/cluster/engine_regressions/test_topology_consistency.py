"""Destructive native-only reproducer; recreate the disposable fixture afterward."""

import concurrent.futures
import json
import os
import re
import subprocess
import threading
import time
import uuid
from pathlib import Path

import pytest

from test_transaction_visibility import Connection, OFFICIAL_IMAGE, ServerError, wait_for_index


class ConsistencyMapAbort(AssertionError):
    """Only a proved Search consistency-check abort is an expected failure."""


def docker(container, *args, timeout=15):
    return subprocess.run(["docker", "exec", container, *args], capture_output=True,
                          text=True, timeout=timeout, check=False)


def consistency_abort_signature(log):
    if "crashed by signal: 6" not in log:
        return None
    if re.search(
            r"/usr/lib/valkey/libsearch\.so[^\n]*LogMessageFatal[^\n]*\n"
            r"/usr/lib/valkey/libsearch\.so[^\n]*PerformSlotConsistencyCheck[^\n]*\n"
            r"/usr/lib/valkey/libsearch\.so[^\n]*SearchIndexPartition", log):
        return "symbolized-consistency-check"
    # The pinned official AMD64 build inlines the check. These exact relative
    # frames identify its cold fatal path, not an arbitrary Search SIGABRT.
    if re.search(
            r"/usr/lib/valkey/libsearch\.so[^\n]*LogMessageFatalD1Ev[^\n]*\n"
            r"/usr/lib/valkey/libsearch\.so\(\+0x3c725e\)[^\n]*\n"
            r"/usr/lib/valkey/libsearch\.so[^\n]*SearchIndexPartition[^\n]*\+0x555\)", log):
        return "pinned-amd64-inlined-consistency-check"
    return None


def test_native_consistent_search_survives_topology_changes(request, record_property):
    if os.environ.get("VECDEX_E2E_ENGINE_TOPOLOGY_REPRO") != "1":
        pytest.skip("separate opt-in to destructive engine-only topology regression required")
    container = os.environ.get("VECDEX_E2E_CLUSTER_CONTAINER")
    assert container, "isolated official fixture container required"
    image = subprocess.check_output(
        ["docker", "inspect", container, "--format", "{{.Config.Image}}"], text=True, timeout=10).strip()
    assert image == OFFICIAL_IMAGE, image
    addresses = [f"127.0.0.1:{port}" for port in range(17389, 17397)]
    nodes = {address: Connection(address) for address in addresses}
    try:
        seed = nodes[addresses[0]]
        slots = seed.command("CLUSTER", "SLOTS")
        assert len({row[2][2] for row in slots}) == 3, "fresh 3+3 fixture required"
        for address, node in nodes.items():
            assert node.command("PING") == "PONG", address
            node.command("CONFIG", "SET", "search.writer-threads", 1, "search.max-worker-suspension-secs", 0)
            previous_log = docker(container, "cat", f"/tmp/search-{address.rsplit(':', 1)[1]}/server.log")
            assert "VALKEY BUG REPORT START" not in previous_log.stdout, "recreate fixture after a previous crash"
        modules = {address: node.command("INFO", "modules") for address, node in nodes.items()}
        if all("module:name=search,ver=66049," in raw for raw in modules.values()):
            request.node.add_marker(pytest.mark.xfail(
                strict=True, raises=ConsistencyMapAbort,
                reason="Search 1.2.1 PerformSlotConsistencyCheck aborts while cluster participants change; "
                       "see tests/cluster/KNOWN_FAILURES.md and saved native crash stacks"))
        index = "engine-topology-" + uuid.uuid4().hex
        seed.command("FT.CREATE", index, "ON", "HASH", "PREFIX", 1, index + ":", "SCHEMA", "status", "TAG")
        wait_for_index({address: nodes[address] for address in addresses[:6]}, index)
        stop = threading.Event()
        statistics = {}
        report_dir = Path(__file__).resolve().parents[2] / "reports"
        report_dir.mkdir(parents=True, exist_ok=True)
        phases = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
            workers = [executor.submit(search_while_moving, address, index, stop) for address in addresses[:3]]
            try:
                for phase in ["expand", "contract"]:
                    result = docker(container, "/bin/sh", "/tests/topology.sh", phase, timeout=120)
                    phases.append({"phase": phase, "returncode": result.returncode,
                                   "stdout": result.stdout, "stderr": result.stderr})
                    if result.returncode:
                        break
            finally:
                stop.set()
                for address, worker in zip(addresses[:3], workers, strict=True):
                    statistics[address] = worker.result(timeout=10)
        proven_aborts, signatures = [], {}
        for port in range(17389, 17397):
            result = docker(container, "cat", f"/tmp/search-{port}/server.log")
            assert result.returncode == 0, result.stderr
            (report_dir / f"engine-topology-{port}.log").write_text(result.stdout)
            signature = consistency_abort_signature(result.stdout)
            if signature:
                proven_aborts.append(port)
                signatures[str(port)] = signature
        evidence = {"image": image, "module_info": modules, "index": index,
                    "query": f"FT.SEARCH {index} @status:{{run}} ALLSHARDS CONSISTENT NOCONTENT LIMIT 0 1",
                    "configuration": {"search.writer-threads": 1, "search.max-worker-suspension-secs": 0},
                    "topology": phases, "native_queries": statistics, "consistency_map_aborted_nodes": proven_aborts,
                    "crash_signatures": signatures}
        (report_dir / "engine-topology.json").write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n")
        record_property("engine_evidence", json.dumps(evidence, sort_keys=True))
        if proven_aborts:
            raise ConsistencyMapAbort(f"official Search aborted in consistency check on {proven_aborts}; "
                                      f"native-only evidence in {report_dir / 'engine-topology.json'}")
        assert len(phases) == 2 and all(phase["returncode"] == 0 for phase in phases), phases
        assert sum(value["successful"] for value in statistics.values()) > 0, statistics
        seed.command("FT.DROPINDEX", index)
    finally:
        for node in nodes.values():
            node.close()


def search_while_moving(address, index, stop):
    successful, errors = 0, {}
    while not stop.is_set():
        try:
            node = Connection(address)
            try:
                while not stop.is_set():
                    reply = node.command("FT.SEARCH", index, "@status:{run}",
                                         "ALLSHARDS", "CONSISTENT", "NOCONTENT", "LIMIT", 0, 1)
                    assert reply == [0], reply  # The index is deliberately empty.
                    successful += 1
                    time.sleep(0.01)
            finally:
                node.close()
        except (ServerError, OSError, ConnectionError) as error:
            name = type(error).__name__
            errors[name] = errors.get(name, 0) + 1
            time.sleep(0.01)
    return {"successful": successful, "errors": errors}
