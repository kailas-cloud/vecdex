#!/usr/bin/env python3
"""Run/verify a real Codex workflow against a disposable MCP fixture."""
import argparse
from collections import Counter
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
VERSION = "0.167.0"


def verify(path):
    events = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    calls = [event["item"] for event in events if event["type"] == "item.completed"
             and event.get("item", {}).get("type") == "mcp_tool_call"]
    assert events[-1]["type"] == "turn.completed", "Codex did not complete"
    final = next(event["item"]["text"] for event in reversed(events)
                 if event.get("item", {}).get("type") == "agent_message")
    final = json.loads(final.strip().removeprefix("```json").removesuffix("```").strip())
    assert final["conflict_code"] == "version_conflict", final
    assert final["source_version"] != final["current_version"]
    assert set(final["list_ids"]) == {"decision-1", "detail-2"}
    assert "skill://vecdex/memory-use/SKILL.md" in final["skill_resources_loaded"]
    envelopes = []
    for call in calls:
        result = call.get("result")
        if result and result.get("structured_content"):
            envelope = result["structured_content"]
            text = next(part["text"] for part in result["content"] if part["type"] == "text")
            assert json.loads(text) == envelope, "text/structured mismatch at Codex boundary"
            envelopes.append((call["tool"], envelope))
    assert any(name == "memory_update" and body.get("error", {}).get("code") == "version_conflict"
               for name, body in envelopes), "conflict was not observed in a real tool result"
    assert any(name == "memory_update" and body.get("data", {}).get("version") == final["current_version"]
               for name, body in envelopes), "current version was not committed by a real tool result"
    assert any(name == "context_get" and any(pin["document_id"] == final["handoff_id"]
               and pin["state"] == "current" and pin["version"] == final["handoff_version"]
               for pin in body.get("data", {}).get("pin_states", [])) for name, body in envelopes)
    assert any(name == "memory_list" and body.get("data", {}).get("next_cursor")
               for name, body in envelopes), "catalog continuation was not tested"
    assert any(name == "memory_read" and body.get("data", {}).get("next_offset")
               for name, body in envelopes), "text continuation was not tested"
    return {"client": "codex", "version": VERSION, "verified": True,
            "mcp_calls": len(calls), "calls_by_tool": dict(Counter(c["tool"] for c in calls)),
            "model_visible_result_bytes": sum(len(json.dumps(c.get("result"), ensure_ascii=False).encode())
                                              for c in calls if c.get("result")),
            "usage": events[-1].get("usage"), "result": final}


def run(args):
    version = subprocess.check_output([args.codex, "--version"], text=True).strip()
    if version != "codex-cli " + VERSION:
        raise RuntimeError(f"Expected pinned Codex {VERSION}; found {version}")
    key = os.environ["VECDEX_API_KEY"]
    args.output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="vecdex-codex-") as temp:
        work = Path(temp)
        shutil.copytree(ROOT / "internal/skills/bundle", work / ".agents/skills")
        prompt = (Path(__file__).with_name("codex-smoke.prompt.txt").read_text()
                  .replace("{{collection}}", "codex_smoke_" + uuid.uuid4().hex[:16]))
        (args.output / "prompt.txt").write_text(prompt)
        command = [args.codex, "exec", "--ignore-user-config", "--skip-git-repo-check", "--ephemeral",
                   "--sandbox", "read-only", "--model", args.model, "-C", str(work),
                   "-c", 'approval_policy="on-request"', "-c", 'approvals_reviewer="auto_review"',
                   "-c", "mcp_servers.vecdex.url=" + json.dumps(args.url),
                   "-c", 'mcp_servers.vecdex.bearer_token_env_var="VECDEX_SMOKE_TOKEN"', "--json", "-"]
        started = time.monotonic()
        with (args.output / "events.jsonl").open("w") as out, (args.output / "stderr.log").open("w") as err:
            subprocess.run(command, input=prompt, text=True, stdout=out, stderr=err, timeout=600,
                           env={**os.environ, "VECDEX_SMOKE_TOKEN": key}, check=True)
        report = verify(args.output / "events.jsonl")
        report["latency_seconds"] = time.monotonic() - started
        report["model"] = args.model
        (args.output / "summary.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    run_args = sub.add_parser("run", help="creates and retains one unique test collection for review")
    run_args.add_argument("--codex", default="codex")
    run_args.add_argument("--model", required=True)
    run_args.add_argument("--url", required=True, help="disposable fixture /mcp endpoint")
    run_args.add_argument("--output", required=True, type=Path)
    verify_args = sub.add_parser("verify", help="verify saved model-visible events without any network calls")
    verify_args.add_argument("events", type=Path)
    args = parser.parse_args()
    if args.action == "run":
        run(args)
    else:
        print(json.dumps(verify(args.events), indent=2))


if __name__ == "__main__":
    main()
