"""Pinned SDK compatibility, including results observed by the real Strands Agent.

The deterministic model checks adapter and workflow contracts. It is deliberately
not an LLM reasoning benchmark and does not need cloud model credentials.
"""
import asyncio
import importlib.metadata
import json
import time

import httpx2
import pytest
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client
from strands import Agent
from strands.models import Model
from strands.vended_plugins.skills import AgentSkills
from strands.tools.mcp import MCPClient as StrandsMCPClient

from mcp_client import LATEST, write_key

pytestmark = pytest.mark.p0


def test_pinned_python_mcp_sdk(mcp, collection_factory):
    assert importlib.metadata.version("mcp") == "2.3.0"
    context = collection_factory()["context_id"]
    async def scenario():
        async with httpx2.AsyncClient(headers={"Authorization": f"Bearer {mcp.api_key}"}) as http:
            async with streamable_http_client(mcp.base_url + "/mcp", http_client=http) as (read, write):
                async with ClientSession(read, write, read_timeout_seconds=60) as session:
                    discovered = await session.discover()
                    assert LATEST in discovered.supported_versions
                    result = await session.call_tool("memory_create", {"context_id": context, "id": "python-sdk", "content": "official Python SDK source", "idempotency_key": write_key()})
                    assert not result.is_error
                    data = result.structured_content["data"]
                    assert data["id"] == "python-sdk" and data["version"]
                    assert json.loads(result.content[0].text) == result.structured_content
                    source = await session.read_resource(data["uri"])
                    assert "official Python SDK source" in source.contents[0].text
    asyncio.run(scenario())


class WorkflowModel(Model):
    """Choose each next tool from data actually delivered in Agent messages."""
    def __init__(self, context_id):
        self.context_id = context_id
        self.turn = 0
        self.first_version = None
        self.page = None
        self.observed = []
        self.observed_result_bytes = 0
        self.text = "evidence 中文 " * 40

    def update_config(self, **model_config):
        pass

    def get_config(self):
        return {"model_id": "deterministic-mcp-contract"}

    async def structured_output(self, output_model, prompt, system_prompt=None, **kwargs):
        raise NotImplementedError("This contract uses the normal tool loop")
        yield  # pragma: no cover

    def last_result(self, messages):
        for message in reversed(messages):
            for block in message["content"]:
                if "toolResult" in block:
                    result = block["toolResult"]
                    texts = [part["text"] for part in result["content"] if "text" in part]
                    assert texts, "MCP result never reached the model as text"
                    envelope = json.loads(texts[0])
                    self.observed_result_bytes += len(json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode())
                    self.observed.append(envelope)
                    return envelope
        raise AssertionError("previous tool result was lost")

    def next_call(self, messages):
        result = self.last_result(messages) if self.turn else None
        if self.turn and self.turn != 9:
            assert result["ok"], result
        context = {"context_id": self.context_id}
        if self.turn == 0:
            return "memory_create", {**context, "id": "strands-a", "content": self.text, "idempotency_key": write_key()}
        if self.turn == 1:
            self.first_version = result["data"]["version"]
            assert result["data"]["id"] == "strands-a"
            return "memory_create", {**context, "id": "strands-b", "content": "second source", "idempotency_key": write_key()}
        if self.turn == 2:
            return "memory_read", {**context, "id": "strands-a", "max_bytes": 64}
        if self.turn == 3:
            self.page = result["data"]
            assert not self.page["complete"] and self.page["version"] == self.first_version
            return "memory_read", {**context, "id": self.page["id"], "offset": self.page["next_offset"], "version": self.page["version"], "max_bytes": 4096}
        if self.turn == 4:
            assert self.page["content"] + result["data"]["content"] == self.text
            return "memory_list", {**context, "limit": 1}
        if self.turn == 5:
            self.page = result["data"]
            assert self.page["next_cursor"]
            return "memory_list", {**context, "limit": 1, "cursor": self.page["next_cursor"]}
        if self.turn == 6:
            assert result["data"]["items"][0]["id"] != self.page["items"][0]["id"]
            return "memory_update", {**context, "id": "strands-a", "if_version": self.first_version, "content": "corrected evidence", "idempotency_key": write_key()}
        if self.turn == 7:
            assert result["data"]["version"] != self.first_version
            self.latest_version = result["data"]["version"]
            return "memory_update", {**context, "id": "strands-a", "if_version": self.first_version, "content": "stale evidence", "idempotency_key": write_key()}
        if self.turn == 8:
            # Error is observed on the next model invocation, just like success.
            raise AssertionError("unreachable")
        return None

    async def stream(self, messages, tool_specs=None, system_prompt=None, **kwargs):
        if self.turn == 8:
            result = self.last_result(messages)
            assert not result["ok"]
            assert result["error"]["code"] == "version_conflict"
            assert result["error"]["current_version"] == self.latest_version
            assert result["error"]["action"]
            call = None
        else:
            assert {"memory_create", "memory_read", "memory_update", "memory_list"} <= {spec["name"] for spec in tool_specs}
            call = self.next_call(messages)
        self.turn += 1
        yield {"messageStart": {"role": "assistant"}}
        if call:
            name, arguments = call
            yield {"contentBlockStart": {"contentBlockIndex": 0, "start": {"toolUse": {"toolUseId": f"call-{self.turn}", "name": name}}}}
            yield {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"toolUse": {"input": json.dumps(arguments)}}}}
        else:
            yield {"contentBlockStart": {"contentBlockIndex": 0, "start": {}}}
            yield {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"text": "MCP workflow complete"}}}
        yield {"contentBlockStop": {"contentBlockIndex": 0}}
        yield {"messageStop": {"stopReason": "tool_use" if call else "end_turn"}}
        yield {"metadata": {"usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2}, "metrics": {"latencyMs": 0}}}


class SkilledWorkflowModel(WorkflowModel):
    def __init__(self, context_id, instructions):
        super().__init__(context_id)
        self.instructions = instructions
        self.skill_requested = False
        self.skill_received = False
        self.skill_result_bytes = 0

    async def stream(self, messages, tool_specs=None, system_prompt=None, **kwargs):
        if not self.skill_requested:
            assert "memory-use" in str(system_prompt) + str(kwargs.get("system_prompt_content"))
            assert "skills" in {spec["name"] for spec in tool_specs}
            self.skill_requested = True
            yield {"messageStart": {"role": "assistant"}}
            yield {"contentBlockStart": {"contentBlockIndex": 0, "start": {"toolUse": {"toolUseId": "activate-skill", "name": "skills"}}}}
            yield {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"toolUse": {"input": '{"skill_name":"memory-use"}'}}}}
            yield {"contentBlockStop": {"contentBlockIndex": 0}}
            yield {"messageStop": {"stopReason": "tool_use"}}
            return
        if not self.skill_received:
            text = "\n".join(part["text"] for message in messages for block in message["content"]
                             if "toolResult" in block for part in block["toolResult"]["content"] if "text" in part)
            assert self.instructions in text
            results = [block["toolResult"] for message in messages for block in message["content"] if "toolResult" in block]
            assert len(results) == 1
            self.skill_result_bytes = len(json.dumps(results[0], ensure_ascii=False, separators=(",", ":")).encode())
            self.skill_received = True
        async for event in super().stream(messages, tool_specs, system_prompt, **kwargs):
            yield event


def test_real_strands_agent_receives_workflow_data(mcp, collection_factory, tmp_path, record_property):
    assert importlib.metadata.version("strands-agents") == "1.64.0"
    context = collection_factory()["context_id"]
    # Materialize the exact server bundle for Strands' real AgentSkills loader.
    manifest = next(skill for skill in mcp.all_pages("skills/list", "skills") if skill["frontmatter"]["name"] == "memory-use")
    root_uri = manifest["uri"].rsplit("/", 1)[0] + "/"
    skill_root = tmp_path / "memory-use"
    for resource in manifest["resources"]:
        relative = resource["uri"].removeprefix(root_uri)
        assert not relative.startswith("/") and ".." not in relative.split("/")
        path = skill_root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(mcp.resource(resource["uri"])["text"])
    source = (skill_root / "SKILL.md").read_text()
    instructions = source.split("---", 2)[2].strip()
    model = SkilledWorkflowModel(context, instructions)
    plugin = AgentSkills(skills=[skill_root], strict=True)
    connection = StrandsMCPClient(url=mcp.base_url + "/mcp", headers={"Authorization": f"Bearer {mcp.api_key}"})
    started = time.perf_counter()
    with Agent(model=model, tools=[connection], plugins=[plugin], callback_handler=None) as agent:
        result = agent("Exercise the memory lifecycle using the supplied context.")
    elapsed = time.perf_counter() - started
    assert "MCP workflow complete" in str(result)
    assert model.turn == 9 and len(model.observed) == 8 and model.skill_received
    assert mcp.read(context, "strands-a")["content"] == "corrected evidence"
    metrics = {
        "client": "strands",
        "strands_version": importlib.metadata.version("strands-agents"),
        "mcp_version": importlib.metadata.version("mcp"),
        "model_kind": "deterministic",
        "agent_elapsed_seconds": elapsed,
        "mcp_calls": len(model.observed),
        "skill_calls": int(model.skill_received),
        "model_visible_result_bytes": model.observed_result_bytes + model.skill_result_bytes,
    }
    for key, value in metrics.items():
        record_property(key, value)
    print(json.dumps({"strands_client_metrics": metrics}, separators=(",", ":")))
