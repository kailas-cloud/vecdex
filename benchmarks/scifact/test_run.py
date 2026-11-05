"""Contract checks for the benchmark, without a dataset download or model."""

import json
import unittest

import httpx

import run


class SourceBenchmarkTests(unittest.TestCase):
    def test_whole_source_and_original_id_reach_api_v2(self):
        content = "  SciFact 中文 🚀\n" * 2000 + "unique final paragraph"
        documents, stats = run.build_source_documents({"42": content})
        self.assertEqual(stats["document_count"], 1)
        self.assertEqual(stats["content_bytes"], len(content.encode()))
        requests = []

        def respond(request):
            requests.append((request.url.path, json.loads(request.content)))
            if request.url.path.endswith("batch-upsert"):
                return httpx.Response(200, json={"failed": 0, "succeeded": 1,
                    "items": [{"id": "42", "status": "ok", "version": "opaque-version"}]})
            return httpx.Response(200, json={"items": [{"id": "42", "snippet": ""}]})

        client = run.VecdexClient("http://vecdex.test", "key", 1)
        client.close()
        client._client = httpx.Client(base_url=client.base_url, transport=httpx.MockTransport(respond))
        try:
            run.ingest_documents(client, "papers", documents, 100)
            result = client.search("papers", "evidence", "semantic", 100)
        finally:
            client.close()
        self.assertEqual(requests[0], ("/api/v2/collections/papers/documents/batch-upsert",
            {"documents": [{"id": "42", "content": content}]}))
        self.assertEqual(requests[1], ("/api/v2/collections/papers/search",
            {"query": "evidence", "mode": "semantic", "limit": 100, "max_content_bytes": 1}))
        self.assertEqual(run.parse_ranked_doc_ids(result), ["42"])

    def test_evaluation_keeps_empty_results_and_does_not_retry_failures(self):
        class EmptyClient:
            calls = 0

            def search(self, *args, **kwargs):
                self.calls += 1
                return {"items": []}

        client = EmptyClient()
        rows, summary = run.evaluate_mode(client, "papers", "semantic", {"1": "evidence"}, {"1": {"42": 1}}, 100)
        self.assertEqual(client.calls, 1)
        self.assertEqual(rows[0]["num_results"], 0)
        self.assertEqual(summary["recall@10"], 0)

        class FailingClient:
            calls = 0

            def search(self, *args, **kwargs):
                self.calls += 1
                raise RuntimeError("coordinator failed")

        failed = FailingClient()
        with self.assertRaisesRegex(RuntimeError, "coordinator failed"):
            run.evaluate_mode(failed, "papers", "semantic", {"1": "evidence"}, {"1": {"42": 1}}, 100)
        self.assertEqual(failed.calls, 1)

    def test_partial_write_receipts_and_invalid_rankings_fail(self):
        client = run.VecdexClient("http://vecdex.test", "key", 1)
        client.close()
        client._client = httpx.Client(base_url=client.base_url,
            transport=httpx.MockTransport(lambda request: httpx.Response(200, json={"failed": 0, "items": []})))
        try:
            with self.assertRaisesRegex(RuntimeError, "acknowledge every source"):
                client.batch_upsert("papers", [{"id": "42", "content": "whole source"}])
        finally:
            client.close()
        for response in [{}, {"items": [{}]}, {"items": [{"id": "42"}, {"id": "42"}]}]:
            with self.subTest(response=response), self.assertRaises(RuntimeError):
                run.parse_ranked_doc_ids(response)
