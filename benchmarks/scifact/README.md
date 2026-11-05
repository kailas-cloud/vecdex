# SciFact Benchmark Harness

The harness uploads each original SciFact paper as one source document through
API v2. Vecdex prepares private fragments when the embedding model rejects a
long input. Search rankings use the original corpus IDs for qrels evaluation.

It loads Hugging Face `BeIR/scifact` and `BeIR/scifact-qrels`, recreates the chosen
collection, uploads the whole corpus in batches, and evaluates semantic and
hybrid search. Each evaluation query runs once. Only the readiness probe retries;
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

The target collection is deleted and recreated. `--ingest-only` stops after
uploading the corpus. `--dataset-cache-dir` selects an existing Hugging Face
cache. Batch size defaults to 100 and must be within 1–100. Search has one
`--limit`, default 100, maximum 100. The benchmark requests a one-byte combined
snippet budget because metrics need ranked IDs only.

Client chunking flags and `--top-k` have been removed. To compare fragmentation
settings, configure the server separately and use distinct output directories.

## Outputs

- `summary.json`: config, source byte counts, timings and metrics by mode
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
