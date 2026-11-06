"""Source and candidate-scope contracts, without a dataset download or model."""

import unittest

from mcp.types import CallToolResult

import run


class RecordingSession:
    def __init__(self, responder):
        self.calls = []
        self.responder = responder

    async def call_tool(self, name, arguments):
        self.calls.append((name, arguments))
        data = self.responder(name, arguments)
        return CallToolResult(content=[], structured_content={"ok": True, "data": data})


def scoped_client(session):
    client = run.VecdexClient(session, "2026-07-28")
    client.contexts["papers"] = {
        "context_id": "ctx-1", "version": "ctx-version", "collection": "papers", "generation": "gen-1",
    }
    return client


class SourceBenchmarkTests(unittest.IsolatedAsyncioTestCase):
    async def test_whole_source_original_id_and_candidate_context_reach_mcp(self):
        content = "  SciFact 中文 🚀\n" * 2000 + "unique final paragraph"
        documents, stats = run.build_source_documents({"42": content})
        self.assertEqual(stats["document_count"], 1)
        self.assertEqual(stats["content_bytes"], len(content.encode()))

        def respond(name, arguments):
            if name == "memory_create":
                return {"id": "42", "created": True, "version": "opaque-version"}
            return {"items": [{"id": "42", "content": ""}], "context_id": "ctx-1", "truncated": False}

        session = RecordingSession(respond)
        client = scoped_client(session)
        ingest = await run.ingest_documents(client, "papers", documents, 100)
        result = await client.search("papers", "evidence", "semantic", 100)
        create_name, create = session.calls[0]
        self.assertEqual(create_name, "memory_create")
        self.assertEqual(create["context_id"], "ctx-1")
        self.assertEqual(create["id"], "42")
        self.assertEqual(create["content"], content)
        self.assertTrue(create["idempotency_key"])
        self.assertEqual(session.calls[1], ("memory_search", {
            "context_id": "ctx-1", "query": "evidence", "mode": "semantic", "limit": 100,
            "max_content_bytes": 1, "max_output_bytes": run.BENCHMARK_RESULT_BUDGET,
        }))
        self.assertEqual(ingest["create_call_count"], 1)
        self.assertEqual(run.parse_ranked_doc_ids(result), ["42"])
        with self.assertRaises(KeyError):
            await client.search("another-collection", "evidence", "semantic", 100)
        self.assertEqual(len(session.calls), 2)

    async def test_evaluation_keeps_empty_results_and_does_not_retry_failures(self):
        class EmptyClient:
            calls = 0

            async def search(self, *args, **kwargs):
                self.calls += 1
                return {"items": []}

        client = EmptyClient()
        rows, summary = await run.evaluate_mode(client, "papers", "semantic", {"1": "evidence"}, {"1": {"42": 1}}, 100)
        self.assertEqual(client.calls, 1)
        self.assertEqual(rows[0]["num_results"], 0)
        self.assertEqual(summary["recall@10"], 0)

        class FailingClient:
            calls = 0

            async def search(self, *args, **kwargs):
                self.calls += 1
                raise RuntimeError("coordinator failed")

        failed = FailingClient()
        with self.assertRaisesRegex(RuntimeError, "coordinator failed"):
            await run.evaluate_mode(failed, "papers", "semantic", {"1": "evidence"}, {"1": {"42": 1}}, 100)
        self.assertEqual(failed.calls, 1)

    async def test_invalid_source_receipts_and_rankings_fail(self):
        for receipt in [{}, {"id": "wrong", "created": True, "version": "v"},
                        {"id": "42", "created": False, "version": "v"}, {"id": "42", "created": True}]:
            with self.subTest(receipt=receipt):
                client = scoped_client(RecordingSession(lambda name, args: receipt))
                with self.assertRaisesRegex(RuntimeError, "invalid source write receipt"):
                    await client.create_sources("papers", [{"id": "42", "content": "whole source"}])
        for response in [{}, {"items": [{}]}, {"items": [{"id": "42"}, {"id": "42"}]}]:
            with self.subTest(response=response), self.assertRaises(RuntimeError):
                run.parse_ranked_doc_ids(response)

    async def test_search_rejects_truncated_results_and_wrong_scope(self):
        for payload in [{"items": [], "context_id": "ctx-1", "truncated": True},
                        {"items": [], "context_id": "another-context", "truncated": False}]:
            with self.subTest(payload=payload):
                client = scoped_client(RecordingSession(lambda name, args: payload))
                with self.assertRaises(RuntimeError):
                    await client.search("papers", "evidence", "semantic", 100)

    async def test_collection_reset_fences_generation_and_context_cleanup_uses_cas(self):
        def respond(name, arguments):
            if name == "collection_get":
                return {"state": "active", "generation": "old-generation"}
            if name == "collection_create":
                return {"name": "papers", "generation": "new-generation"}
            if name == "context_create":
                return {"context": {"context_id": "new-context", "version": "context-version",
                                    "collection": "papers", "generation": "new-generation"}}
            return {"deleted": True}

        session = RecordingSession(respond)
        client = run.VecdexClient(session, "2026-07-28")
        await run.reset_collection(client, "papers")
        await client.release_contexts()
        self.assertEqual(session.calls[1], ("collection_delete", {
            "name": "papers", "generation": "old-generation", "confirm_name": "papers",
        }))
        self.assertEqual(session.calls[3][1]["generation"], "new-generation")
        self.assertEqual(session.calls[-1][0], "context_delete")
        self.assertEqual(session.calls[-1][1]["expected_version"], "context-version")

    async def test_mcp_domain_errors_are_not_empty_rankings(self):
        class ErrorSession:
            async def call_tool(self, name, arguments):
                return CallToolResult(content=[], is_error=True,
                    structured_content={"ok": False, "error": {"code": "stale_context"}})

        with self.assertRaisesRegex(RuntimeError, "stale_context"):
            await scoped_client(ErrorSession()).search("papers", "evidence", "semantic", 100)
