# SciFact Benchmark Harness

The harness uploads each original SciFact paper as one source document through
the MCP `memory_create` tool. Vecdex prepares private fragments when the embedding model rejects a
long input. Search rankings use the original corpus IDs for qrels evaluation.

The client uses the official Python MCP SDK 2.3.0 and Streamable HTTP at `/mcp`.

It loads Hugging Face `BeIR/scifact` and `BeIR/scifact-qrels`, recreates the chosen
collection, creates a durable context bound to its generation, uploads the whole
corpus with individual create-only calls, and evaluates semantic and hybrid
search through that context. Each evaluation query runs once. Only the readiness probe retries;
search errors fail the run, and empty rankings count toward the metrics.

## Local setup

Start the official Valkey stack with the ONNX embedder from the repository root:

```bash
cd tests
docker compose --profile valkey-onnx up --build -d valkey vecdex-valkey-onnx
cd ..
python3 -m venv .venv-bench
source .venv-bench/bin/activate
pip install -r benchmarks/scifact/requirements.txt
```

Wait for Vecdex to become healthy, then run:

```bash
python benchmarks/scifact/run.py \
  --base-url http://localhost:8080 \
  --api-key test-api-key \
  --collection scifact-benchmark \
  --limit 100 \
  --modes semantic hybrid \
  --output-dir benchmarks/scifact/output/latest
```

The token must have the admin role. The target collection is inspected and
deleted with its exact generation before recreation. Context cleanup uses its
exact version and leaves the corpus collection available for inspection. `--ingest-only` stops after
uploading the corpus. `--dataset-cache-dir` selects an existing Hugging Face
cache. `--batch-size` controls local progress reporting groups (1–100, default
100); it does not issue a bulk MCP call. Use the embedded Go SDK for production
bulk ingestion. Search has one
`--limit`, default 100, maximum 100. The benchmark requests a one-byte combined
snippet budget because metrics need ranked IDs only. It explicitly requests
the supported 256 KiB total response budget to fit up to 100 source identities.
A response still truncated by that budget fails the run; it must not silently
measure a shorter ranking.

Client chunking flags and `--top-k` have been removed. To compare fragmentation
settings, configure the server separately and use distinct output directories.

## Outputs

- `summary.json`: negotiated MCP version, collection generation, source byte counts, timings and metrics by mode
- `per_query.csv`: nDCG@10, MRR@10, recall@10/20 and source IDs for every query
- `report.md`: aggregate metrics and qualitative examples
- `plots/`: aggregate metrics, per-query distributions and result counts

Run the local protocol and metric checks without downloading the dataset:

```bash
python -m unittest discover -s benchmarks/scifact -p 'test_*.py'
```

Stop the local stack when finished:

```bash
cd tests
docker compose --profile valkey-onnx down -v
```
