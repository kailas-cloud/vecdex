# REST to MCP test migration

The numbered REST suites were replaced with native MCP tests. No test client
translates REST calls into MCP or supplies hidden document versions. Source
updates and deletes always pass the version observed by the caller. The shared
wire client checks the equality of structured content and the JSON text result
on every tool call it makes, including errors. This table maps retained behavior;
it does not claim a one-for-one replacement of every old HTTP test.

| Previous suite | Current coverage |
| --- | --- |
| `01_health` | Retained operator health and Prometheus checks. |
| `02_auth`, `03_tracing`, `11_error_contract` | `test_mcp_protocol.py`: Bearer auth, reader role, owner isolation, request IDs, protocol errors versus tool errors, schema validation, removed REST paths. |
| `04_collections` | `test_mcp_context.py`: collection generation fence, delete confirmation, recreate safety; fixtures exercise create/get/delete on every lifecycle. `test_mcp_protocol.py` checks discovery. |
| `05_documents`, `14_data_integrity`, `18_write_regressions` | `test_mcp_memory.py`: exact JSON values, merge patch, metadata projection, create-only writes, CAS conflicts, retry fingerprints, no resurrection, bounds and complete source preservation on provider failure. |
| `06_search`, `07_filters`, `16_similar` | `test_mcp_search.py`: semantic, keyword and hybrid retrieval, exact/range/negative/disjunction filters, field type checks, defaults and overrides, aggregate content and result budgets, source versions and similarity exclusion. |
| `08_batch`, `17_chunked_ingest` | Batch and ingest are embedded SDK interfaces. `pkg/sdk/batch_version_integration_test.go`, `ingest_integration_test.go` and `ingest_test.go` preserve CAS, source lifecycle and batch/parallel ingestion coverage. |
| `09_usage` | `test_mcp_protocol.py` checks the month accounting contract; `test_mcp_providers.py` checks reservation on unknown token usage, cache hits and shared replica accounting. The isolated low-budget fixture rejects embedding work while reads, keyword search and metadata edits remain usable. |
| `10_rate_limiting` | Removed the nine strict xfails for an unimplemented REST feature. This migration does not claim rate limiting is implemented. MCP input and result bounds have active assertions; the Python suite does not directly test the HTTP request-body size cap. |
| `12_e2e_flows`, `13_concurrent` | `test_mcp_memory.py`, `test_mcp_context.py` and `test_mcp_clients.py`: lifecycle, reconnect, page continuation, correction, one-winner CAS and identical concurrent retries. |
| `19_auto_fragments` | `test_mcp_memory.py` reconstructs UTF-8 source pages; `test_mcp_search.py` finds the last fragment, patches indexed metadata, replaces long content and verifies old fragments disappear. Internal fragments remain private. |
| `20_embedding_space` | `test_mcp_providers.py` checks incompatible embedder rejection while reads, metadata patches and keyword search remain available. |
| `21_document_cursor` | `test_mcp_memory.py` checks bounded source identity pages without duplicates; contexts also have bounded cursor coverage. |
| `22_bedrock` | `test_mcp_providers.py` preserves full V4 input, document/query purpose, unknown usage reservations, cache and replica checks. Native batch splitting/order is covered by `internal/transport/bedrock/embedder_test.go:TestContractAndNativeBatches`; completed-prefix failure by `internal/usecase/batch/service_test.go:TestUpsertKeepsCompletedEmbeddingPrefixOnLaterFailure`. |
| `23_expiry` | `test_mcp_memory.py`: absolute expiry, explicit clear, already-expired writes, retry after expiry and ABA protection. `test_mcp_context.py`: absolute context lifetime, no read renewal, persistent setting and surviving receipts. |

The Go SDK also retains native integration tests for JSON metadata, idempotency,
source expiry, source conditions and versions. Vector input/output stays in the
SDK and its conversion/domain tests; MCP intentionally returns no vectors.
HTTP embedding token headers were replaced with `memory_status.embedding_usage`
and provider mock evidence. The protocol tests assert current capabilities,
paged discovery, resources, prompts, text/structured parity and output limits.

`test_mcp_clients.py` exercises the exact pinned official MCP and Strands
packages. Its deterministic Strands model sees and uses actual server results;
it is not a mock MCP transport and is not an LLM reasoning benchmark. It also
materializes the MCP skill bundle, uses the real Strands `AgentSkills` loader,
and verifies skill metadata and full activated instructions in model inputs.
`test_mcp_semantic_fixtures.py` adds explicit provenance, temporal intent versus
confirmed outcome, negation and correction fixtures inspired by Observational
Memory concerns. It verifies storage semantics, not automatic extraction.

Provider-specific tests skip when their fixture URL is absent. ONNX also skips
the mock-provider failure marker. Application behavior is not hidden behind
xfails. Run every Compose profile before treating the provider contracts as
verified. [Client smoke documentation](clients/README.md) describes the separate
Codex run and its verifier limits. Semantic handoff quality is not automatically
scored by either the storage fixtures or the Codex verifier.
