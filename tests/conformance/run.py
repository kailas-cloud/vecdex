#!/usr/bin/env python3
"""Run pinned upstream scenarios against the real Vecdex MCP transport.

A scoped pass is not a full requirement-set conformance claim. Raw upstream
FAILURE/SKIPPED checks remain in the report even when a documented fixture
limitation explains them.
"""

import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]


def classify_check(scenario, check, manifest):
    """Only exact upstream untestable evidence can qualify a fixture limitation."""
    limitations = manifest["fixture_limitations"].get(scenario, {})
    if (check["id"] in limitations and check["status"] == "FAILURE"
            and check.get("details", {}).get("untestable") is True):
        return "fixture_limitation"
    if (check["status"] == "SKIPPED"
            and check["id"] in manifest["optional_skips"].get(scenario, [])):
        return "optional_not_advertised"
    if check["status"] in ("SUCCESS", "INFO"):
        return "passed" if check["status"] == "SUCCESS" else "info"
    return "failed"


def run_scenario(job, args, manifest):
    version, scenario = job
    output = args.output / version / scenario
    output.mkdir(parents=True)
    command = ["node", str(args.upstream / "dist/index.js"), "server", "--url", args.url,
               "--scenario", scenario, "--spec-version", version,
               "--output-dir", str(output), "--timeout", "30000"]
    if scenario in manifest.get("extension_scenarios", []):
        command.append("--force")
    with (output / "runner.log").open("w") as log:
        try:
            completed = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=45, check=False)
            exit_code = completed.returncode
        except subprocess.TimeoutExpired:
            exit_code = 124
    check_files = list(output.rglob("checks.json"))
    if len(check_files) != 1:
        checks = [{"id": "runner-results", "status": "FAILURE",
                   "errorMessage": f"expected one checks.json; found {len(check_files)}, exit={exit_code}"}]
    else:
        checks = json.loads(check_files[0].read_text())
    if not checks:
        checks = [{"id": "runner-empty", "status": "FAILURE", "errorMessage": "scenario produced no checks"}]
    classified = [{**check, "vecdex_classification": classify_check(scenario, check, manifest)} for check in checks]
    counts = Counter(check["vecdex_classification"] for check in classified)
    # An upstream crash cannot become a pass just because it wrote partial output.
    if exit_code not in (0, 1):
        counts["failed"] += 1
    print(f"{version} {scenario}: {dict(counts)} (upstream exit {exit_code})", flush=True)
    return {"profile": version, "scenario": scenario, "upstream_exit_code": exit_code,
            "counts": dict(counts), "checks": classified}


def start_fixture(args):
    binary = args.fixture_binary
    if binary is None:
        binary = args.output / "vecdex-conformance-fixture"
        env = dict(os.environ)
        env.setdefault("GOCACHE", str(Path(tempfile.gettempdir()) / "vecdex-go-cache"))
        subprocess.run(["go", "build", "-o", str(binary), "./tests/conformance/fixture"],
                       cwd=ROOT, env=env, check=True)
    ready_file = args.output / "fixture.url"
    log = (args.output / "fixture.log").open("w")
    env = {**os.environ, "VECDEX_CONFORMANCE_READY_FILE": str(ready_file)}
    process = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    for _ in range(300):
        if ready_file.exists() and ready_file.read_text().strip():
            args.url = ready_file.read_text().strip()
            return process, log
        if process.poll() is not None:
            log.close()
            raise RuntimeError(f"fixture failed; read {args.output / 'fixture.log'}")
        time.sleep(0.1)
    process.terminate()
    process.wait(timeout=5)
    log.close()
    raise RuntimeError("fixture did not become ready in 30 seconds")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--upstream", required=True, type=Path, help="checkout at manifest's exact commit, npm ci completed")
    parser.add_argument("--url", help="test-only already-authenticated MCP endpoint; default launches loopback fixture")
    parser.add_argument("--fixture-binary", type=Path, help="prebuilt fixture, otherwise go build is used")
    parser.add_argument("--output", type=Path, default=ROOT / "tests/reports/conformance")
    args = parser.parse_args()
    args.upstream = args.upstream.resolve()
    args.output = args.output.resolve() / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    args.output.mkdir(parents=True)
    manifest = json.loads((HERE / "manifest.json").read_text())
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=args.upstream, text=True).strip()
    if commit != manifest["upstream_commit"]:
        raise RuntimeError(f"upstream commit {commit} differs from pinned {manifest['upstream_commit']}")
    fixture = None
    fixture_log = None
    try:
        if args.url is None:
            fixture, fixture_log = start_fixture(args)
        jobs = [(version, scenario) for version, scenarios in manifest["profiles"].items() for scenario in scenarios]
        with ThreadPoolExecutor(max_workers=4) as pool:
            results = list(pool.map(lambda job: run_scenario(job, args, manifest), jobs))
    finally:
        if fixture is not None:
            fixture.terminate()
            fixture.wait(timeout=5)
            fixture_log.close()
    counts = Counter()
    for result in results:
        counts.update(result["counts"])
    summary = {"upstream_commit": commit, "full_conformance": False,
               "scope": "selected production-contract scenarios; fixture limitations remain untested",
               "scope_notes": manifest.get("scope_notes", []),
               "scoped_checks_passed": counts["failed"] == 0, "counts": dict(counts),
               "excluded_groups": manifest["excluded_groups"], "results": results}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"Scoped result: {dict(counts)}. Full conformance is NOT established.\nReport: {args.output / 'summary.json'}")
    return int(counts["failed"] > 0)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f"conformance runner: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
