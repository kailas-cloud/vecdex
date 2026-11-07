"""Storage fixtures for memory skills inspired by Observational Memory failures.

These cases preserve authored facts and provenance. They do not claim that a
scripted test evaluates an LLM's extraction quality or temporal reasoning.
"""
import pytest

from mcp_client import write_key

CASES = [
    ("question", "Does Alex prefer tea?", "question", "unknown"),
    ("intent", "Alex plans to send the report on 2026-11-01.", "intent", "unconfirmed"),
    ("observation", "Alex sent the report on 2026-11-02; receipt R-42.", "observation", "confirmed"),
    ("negative", "Alex does not use the production account for tests.", "fact", "negative"),
    ("correction", "Alex now prefers tea; this replaces the earlier coffee preference.", "correction", "confirmed"),
]


@pytest.mark.p0
@pytest.mark.parametrize("document_id,text,kind,status", CASES)
def test_semantic_distinctions_are_explicit_sources(mcp, collection_factory, document_id, text, kind, status):
    context = collection_factory()["context_id"]
    metadata = {"kind": kind, "status": status, "source_ref": f"conversation:test:{document_id}", "observed_at": "2026-11-06T12:00:00Z"}
    saved = mcp.create(context, document_id, text, metadata=metadata)
    read = mcp.read(context, document_id, include_metadata=True)
    assert read["content"] == text and read["metadata"] == metadata
    assert read["version"] == saved["version"]


@pytest.mark.p0
def test_correction_is_explicit_and_retains_other_memories(mcp, collection_factory):
    context = collection_factory()["context_id"]
    original = mcp.create(context, "preference", "Alex prefers coffee", metadata={"source_ref": "conversation:1"})
    other = mcp.create(context, "work", "Alex works in finance", metadata={"source_ref": "conversation:2"})
    corrected = mcp.update(context, "preference", original["version"], content="Alex now prefers tea", metadata={"source_ref": "conversation:3", "replaces_version": original["version"]})
    assert corrected["version"] != original["version"]
    assert mcp.read(context, "work")["version"] == other["version"]
    assert mcp.read(context, "preference", include_metadata=True)["metadata"]["replaces_version"] == original["version"]
