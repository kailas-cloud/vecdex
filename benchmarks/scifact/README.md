# SciFact Benchmark Harness

This directory contains a standalone Python harness for running a chunked retrieval benchmark against a live `vecdex` instance using Hugging Face `BeIR/scifact` plus `BeIR/scifact-qrels`.

The harness:

- loads the full SciFact corpus and test qrels
- builds chunked documents using the local `all-MiniLM-L6-v2` tokenizer
- recreates one vecdex collection with chunk metadata
- indexes all chunks via REST batch upserts
- evaluates `semantic` and `hybrid` search
- collapses chunk hits back to source-document rankings
- writes `summary.json`, `per_query.csv`, `report.md`, and PNG plots under `plots/`

## Prerequisites

- Docker with Compose
- Python 3.12+
- A running `vecdex` instance backed by the local ONNX embedder

The recommended local stack for this benchmark is the existing `valkey-onnx` profile:

```bash
cd /Users/chistopat/GolandProjects/vecdex/tests
docker compose --profile valkey-onnx up --build -d valkey vecdex-valkey-onnx
```

Wait until `vecdex-valkey-onnx` is healthy, then run the harness from the repository root.

## Install benchmark dependencies

```bash
python3 -m venv .venv-bench
source .venv-bench/bin/activate
pip install -r benchmarks/scifact/requirements.txt
```

## Run

Named chunking presets for test runs:

- Group `A`: `128` tokens, overlap `16`
- Group `B`: `256` tokens, overlap `32`

Example: group `A`

```bash
python benchmarks/scifact/run.py \
  --base-url http://localhost:8080 \
  --api-key test-api-key \
  --collection scifact-benchmark-a \
  --chunk-group A \
  --modes semantic hybrid \
  --output-dir benchmarks/scifact/output/group-a
```

Example: group `B`

```bash
python benchmarks/scifact/run.py \
  --base-url http://localhost:8080 \
  --api-key test-api-key \
  --collection scifact-benchmark-b \
  --chunk-group B \
  --modes semantic hybrid \
  --output-dir benchmarks/scifact/output/group-b
```

## Important behavior

- The harness deletes and recreates the target collection before indexing.
- `--chunk-group` applies a named preset and overrides manual `--chunk-size` / `--overlap` values.
- `--chunk-size` counts total tokens per chunk, including tokenizer-added special tokens.
- Chunking uses the local [`tokenizer.json`](/Users/chistopat/GolandProjects/vecdex/models/all-MiniLM-L6-v2/tokenizer.json) so chunk length matches the ONNX model path used by `vecdex`.
- Search is evaluated at document level:
  - vecdex returns chunk hits
  - hits are grouped by `parent_doc_id`
  - each document keeps the best chunk score
  - ties break by earlier chunk rank, then lower `chunk_index`

## Output artifacts

- `summary.json`: run config, corpus diagnostics, and aggregate metrics by mode
- `per_query.csv`: one row per query and mode with metrics plus top/relevant doc ids
- `report.md`: compact Markdown summary with best/worst qualitative examples
- `plots/aggregate_metrics.png`: grouped bar chart for the aggregate metrics
- `plots/ndcg_at_10_boxplot.png`: per-query nDCG@10 distribution by mode
- `plots/mrr_at_10_boxplot.png`: per-query MRR@10 distribution by mode
- `plots/unique_docs_histogram.png`: diagnostic histogram for chunk-hit diversity

## Defaults

- Dataset: `scifact`
- Qrels split: `test`
- Chunk size: `256`
- Overlap: `0`
- Chunk groups:
  - `A`: chunk size `128`, overlap `16`
  - `B`: chunk size `256`, overlap `32`
- `top_k`: `500`
- `limit`: `100`
- Modes: `semantic hybrid`

## Tear down

```bash
cd /Users/chistopat/GolandProjects/vecdex/tests
docker compose --profile valkey-onnx down -v
```
