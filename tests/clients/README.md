# Agent client checks

These checks use pinned clients against a running disposable Vecdex `/mcp`
endpoint. The Python contracts use `mcp==2.3.0` and
`strands-agents==1.64.0` from [the locked test environment](../uv.lock).
The Codex runner requires `codex-cli 0.167.0` exactly.

## Codex with a real model

Run [codex_smoke.py](codex_smoke.py) from the repository root. Put the pinned
Codex binary on `PATH`, or pass its path with `--codex`. Codex must already be
authenticated, and `CODEX_SMOKE_MODEL` must name a model available to that account.
Set `VECDEX_API_KEY` to an admin key for the disposable fixture: the scenario
creates a collection as well as contexts and memories.

```bash
python3 tests/clients/codex_smoke.py run \
  --codex codex \
  --model "$CODEX_SMOKE_MODEL" \
  --url http://localhost:8080/mcp \
  --output "$PWD/tests/reports/clients/codex-smoke"
```

The output directory must not exist. Each run creates a unique collection and
retains it with a persistent context for inspection. The runner does not start
the server or remove this data. Remove the disposable fixture after review, or
delete only the recorded collection and context using their current generation
and version.

The runner copies the repository's skill bundle into a temporary
`.agents/skills` directory and runs the fixed [scenario](codex-smoke.prompt.txt).
Codex uses `--ignore-user-config`, an ephemeral execution, a `read-only` shell
sandbox, `approval_policy="on-request"`, and
`approvals_reviewer="auto_review"`. Eligible permission requests follow the
normal automatic review process; sandbox boundaries still apply. See
[official OpenAI approval guidance](https://learn.chatgpt.com/docs/agent-approvals-security#common-sandbox-and-approval-combinations).
The read-only shell sandbox does not make the configured MCP tools read-only;
the scenario explicitly authorizes writes to its own disposable collection.
The server credential is supplied through an environment variable.

The run writes `prompt.txt`, `events.jsonl`, and `stderr.log`. After successful
execution and verification, it also writes `summary.json`, which includes the
selected model, elapsed time, call counts, result bytes and reported token usage.
The subprocess has a 600-second timeout. Failed runs retain the files already
written so their events and errors can be inspected.

Verify a saved event stream without calling a model or server:

```bash
python3 tests/clients/codex_smoke.py verify \
  tests/reports/clients/codex-smoke/events.jsonl
```

Verification requires a completed turn and final JSON. It checks text/structured
result parity where structured tool results are present, a real version
conflict, a committed updated version, a current pinned handoff version, and
at least one catalog cursor and content continuation. The final source ID list
and loaded skill URI are checked against the model's final report. The verifier
does not independently check every page, re-read live storage, or score the
handoff's factual accuracy. The `run` command enforces the CLI version;
`verify` alone does not authenticate the provenance of a supplied event file.
This is a model-driven compatibility smoke test, not a broad reasoning benchmark.

## Strands with a deterministic model

From the repository root, use the locked test environment and an existing
fixture:

```bash
VECDEX_BASE_URL=http://localhost:8080 \
  uv run --project tests --frozen pytest \
  tests/test_mcp_clients.py -v -s -o junit_family=legacy \
  --junitxml=tests/reports/clients/strands.xml
```

The fixture uses `test-api-key` by default; set `VECDEX_API_KEY` for another
admin key. The test uses the real Strands `Agent`, `MCPClient` and `AgentSkills`
plugin. It downloads the published skill bundle, checks that the model sees the
skill metadata, activates the skill, and checks the exact instructions in the
model's input. Its deterministic model then makes eight MCP calls using returned
IDs, versions, offsets and cursors, including an intentional stale-version error.
No model-provider credentials are needed for this test.

The Strands test emits JUnit properties and a compact JSON record with client
versions, elapsed agent time, eight MCP calls, one skill activation and
`model_visible_result_bytes`. That byte count is the compact UTF-8 JSON size of
the tool-result blocks observed by the deterministic model, including the skill
result. Agent time covers initialization, the workflow and shutdown, excluding
fixture creation and skill download. These measures are observations of this
adapter test, not LLM token costs or performance comparisons with Codex.
