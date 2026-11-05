#!/usr/bin/env python3
# pyright: reportMissingImports=false
"""Run a source-document SciFact retrieval benchmark against a live vecdex instance."""

from __future__ import annotations

import argparse
import csv
import json
import math
import statistics
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import quote

import httpx


DEFAULT_OUTPUT_DIR = Path(__file__).resolve().parent / "output" / "latest"
DEFAULT_BASE_URL = "http://localhost:8080"
DEFAULT_API_KEY = "test-api-key"
DEFAULT_COLLECTION = "scifact-benchmark"
DEFAULT_QRELS_SPLIT = "test"
DEFAULT_LIMIT = 100
DEFAULT_MODES = ("semantic", "hybrid")
DEFAULT_BATCH_SIZE = 100
DEFAULT_SMOKE_QUERIES = 5
DEFAULT_SEARCH_RETRIES = 8
DEFAULT_SEARCH_RETRY_SLEEP = 1.5


def log(message: str) -> None:
    print(message, flush=True)


@dataclass(frozen=True)
class SourceDocument:
    id: str
    content: str


class VecdexClient:
    """Minimal REST client for the benchmark harness."""

    def __init__(self, base_url: str, api_key: str, timeout_sec: float) -> None:
        self.base_url = base_url.rstrip("/")
        self._client = httpx.Client(
            base_url=self.base_url,
            timeout=httpx.Timeout(timeout_sec),
            headers={"Authorization": f"Bearer {api_key}"},
        )

    def close(self) -> None:
        self._client.close()

    def health(self) -> dict[str, Any]:
        response = self._client.get("/health")
        response.raise_for_status()
        return response.json()

    def delete_collection(self, name: str) -> None:
        response = self._client.delete(f"/api/v2/collections/{quote(name, safe='')}")
        if response.status_code not in (204, 404):
            raise httpx.HTTPStatusError(
                f"delete collection failed: {response.status_code} {response.text}",
                request=response.request,
                response=response,
            )

    def create_collection(self, name: str) -> dict[str, Any]:
        response = self._client.post("/api/v2/collections", json={"name": name})
        response.raise_for_status()
        return response.json()

    def batch_upsert(self, collection: str, documents: list[dict[str, Any]]) -> dict[str, Any]:
        response = self._client.post(
            f"/api/v2/collections/{quote(collection, safe='')}/documents/batch-upsert",
            json={"documents": documents},
        )
        response.raise_for_status()
        payload = response.json()
        failed = int(payload.get("failed", 0))
        if failed:
            items = payload.get("items", [])
            errors = [item for item in items if item.get("status") == "error"]
            raise RuntimeError(f"batch upsert failed for {failed} items: {errors[:3]}")
        items = payload.get("items", [])
        if payload.get("succeeded") != len(documents) or len(items) != len(documents):
            raise RuntimeError("batch upsert did not acknowledge every source")
        for document, item in zip(documents, items, strict=True):
            if item.get("id") != document["id"] or item.get("status") != "ok" or not item.get("version"):
                raise RuntimeError(f"invalid source write receipt: {item}")
        return payload

    def search(
        self,
        collection: str,
        query: str,
        mode: str,
        limit: int,
    ) -> dict[str, Any]:
        response = self._client.post(
            f"/api/v2/collections/{quote(collection, safe='')}/search",
            json={
                "query": query,
                "mode": mode,
                "limit": limit,
                "max_content_bytes": 1,
            },
        )
        response.raise_for_status()
        return response.json()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL, help="vecdex base URL, e.g. http://localhost:8080")
    parser.add_argument("--api-key", default=DEFAULT_API_KEY, help="vecdex API key")
    parser.add_argument("--collection", default=DEFAULT_COLLECTION, help="collection name used for the benchmark")
    parser.add_argument("--output-dir", default=str(DEFAULT_OUTPUT_DIR), help="directory where benchmark artifacts are written")
    parser.add_argument("--dataset-cache-dir", default=None, help="optional Hugging Face datasets cache dir")
    parser.add_argument("--dataset", default="scifact", help="dataset label written to the report")
    parser.add_argument("--qrels-split", default=DEFAULT_QRELS_SPLIT, help="SciFact qrels split to evaluate")
    parser.add_argument("--limit", type=int, default=DEFAULT_LIMIT, help="vecdex limit search parameter")
    parser.add_argument("--batch-size", type=int, default=DEFAULT_BATCH_SIZE, help="batch upsert size")
    parser.add_argument("--modes", nargs="+", choices=("semantic", "hybrid", "keyword"), default=list(DEFAULT_MODES), help="search modes to evaluate")
    parser.add_argument("--smoke-queries", type=int, default=DEFAULT_SMOKE_QUERIES, help="number of test queries used by the search readiness probe")
    parser.add_argument("--search-retries", type=int, default=DEFAULT_SEARCH_RETRIES, help="retries for the readiness probe")
    parser.add_argument("--search-retry-sleep", type=float, default=DEFAULT_SEARCH_RETRY_SLEEP, help="seconds between search retries")
    parser.add_argument("--http-timeout", type=float, default=120.0, help="HTTP timeout in seconds")
    parser.add_argument("--ingest-only", action="store_true", help="stop after ingesting the full source corpus")
    return parser.parse_args()


def main() -> int:
    benchmark_started_at = time.perf_counter()
    args = parse_args()
    validate_args(args)

    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    log("[info] loading SciFact corpus, queries, and qrels from Hugging Face")
    corpus, queries, qrels = load_scifact_data(args.dataset_cache_dir, args.qrels_split)
    log(
        f"[info] loaded corpus={len(corpus)} docs, queries={len(queries)} test queries, qrels={sum(len(v) for v in qrels.values())}"
    )

    documents, corpus_stats = build_source_documents(corpus)
    verify_qrels_coverage(corpus, qrels)

    client = VecdexClient(args.base_url, args.api_key, args.http_timeout)
    try:
        wait_for_health(client, retries=args.search_retries, sleep_sec=args.search_retry_sleep)
        reset_collection(client, args.collection)
        ingest_stats = ingest_documents(client, args.collection, documents, batch_size=args.batch_size)
        if args.ingest_only:
            log(f"[done] ingest complete for collection={args.collection}; skipping evaluation (--ingest-only)")
            return 0
        probe_search_readiness(
            client=client,
            collection=args.collection,
            sample_queries=list(queries.items())[: args.smoke_queries],
            modes=args.modes,
            limit=args.limit,
            retries=args.search_retries,
            sleep_sec=args.search_retry_sleep,
        )

        log("[info] running full evaluation")
        per_query_rows: list[dict[str, Any]] = []
        summary_by_mode: dict[str, Any] = {}
        performance_by_mode: dict[str, Any] = {}
        evaluation_started_at = time.perf_counter()

        for mode in args.modes:
            log(f"[info] mode={mode}: evaluating {len(queries)} queries")
            mode_started_at = time.perf_counter()
            mode_rows, mode_summary = evaluate_mode(
                client=client,
                collection=args.collection,
                mode=mode,
                queries=queries,
                qrels=qrels,
                limit=args.limit,
            )
            per_query_rows.extend(mode_rows)
            summary_by_mode[mode] = mode_summary
            mode_elapsed = time.perf_counter() - mode_started_at
            performance_by_mode[mode] = {
                "elapsed_sec": mode_elapsed,
                "query_count": len(mode_rows),
                "queries_per_sec": len(mode_rows) / mode_elapsed if mode_elapsed > 0 else 0.0,
            }

        evaluation_elapsed = time.perf_counter() - evaluation_started_at
        total_elapsed = time.perf_counter() - benchmark_started_at

        summary = {
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "dataset": args.dataset,
            "qrels_split": args.qrels_split,
            "collection": args.collection,
            "base_url": args.base_url,
            "config": {
                "api_version": "v2",
                "fragmentation": "server",
                "limit": args.limit,
                "batch_size": args.batch_size,
                "modes": list(args.modes),
            },
            "corpus_stats": corpus_stats,
            "metrics_by_mode": summary_by_mode,
            "performance": {
                "ingest": ingest_stats,
                "evaluation": {
                    "elapsed_sec": evaluation_elapsed,
                    "queries_total": len(queries) * len(args.modes),
                    "queries_per_sec": (len(queries) * len(args.modes)) / evaluation_elapsed if evaluation_elapsed > 0 else 0.0,
                    "by_mode": performance_by_mode,
                },
                "total_elapsed_sec": total_elapsed,
            },
        }

        write_outputs(
            output_dir=output_dir,
            summary=summary,
            per_query_rows=per_query_rows,
            queries=queries,
            qrels=qrels,
        )
        log(f"[done] benchmark artifacts written to {output_dir}")
    finally:
        client.close()

    return 0


def validate_args(args: argparse.Namespace) -> None:
    if args.limit <= 0 or args.limit > 100:
        raise ValueError("--limit must be within vecdex limits (1..100)")
    if args.batch_size <= 0 or args.batch_size > 100:
        raise ValueError("--batch-size must be within vecdex batch upsert limits (1..100)")
    if args.search_retries <= 0:
        raise ValueError("--search-retries must be positive")
    if args.smoke_queries <= 0:
        raise ValueError("--smoke-queries must be positive")
    if args.search_retry_sleep < 0:
        raise ValueError("--search-retry-sleep cannot be negative")


def load_scifact_data(cache_dir: str | None, qrels_split: str) -> tuple[dict[str, str], dict[str, str], dict[str, dict[str, int]]]:
    dataset_kwargs: dict[str, Any] = {}
    if cache_dir:
        dataset_kwargs["cache_dir"] = cache_dir

    corpus_rows = load_with_fallbacks(
        specs=[
            ("BeIR/scifact", "corpus", "corpus"),
            ("BeIR/scifact", None, "corpus"),
        ],
        **dataset_kwargs,
    )
    queries_rows = load_with_fallbacks(
        specs=[
            ("BeIR/scifact", "queries", "queries"),
            ("BeIR/scifact", None, "queries"),
        ],
        **dataset_kwargs,
    )
    qrels_rows = load_with_fallbacks(
        specs=[
            ("BeIR/scifact-qrels", "default", qrels_split),
            ("BeIR/scifact-qrels", None, qrels_split),
        ],
        **dataset_kwargs,
    )

    corpus = {str(row["_id"]): canonicalize_document(row.get("title", ""), row.get("text", "")) for row in corpus_rows}

    qrels: dict[str, dict[str, int]] = {}
    for row in qrels_rows:
        query_id = str(row["query-id"])
        doc_id = str(row["corpus-id"])
        score = int(row["score"])
        if score <= 0:
            continue
        qrels.setdefault(query_id, {})[doc_id] = score

    query_ids = set(qrels)
    queries = {
        str(row["_id"]): (row.get("text") or "").strip()
        for row in queries_rows
        if str(row["_id"]) in query_ids
    }

    missing_queries = sorted(query_ids.difference(queries))
    if missing_queries:
        raise RuntimeError(f"missing queries for qrels ids: {missing_queries[:10]}")

    missing_docs = sorted(
        {
            doc_id
            for relevant_docs in qrels.values()
            for doc_id in relevant_docs
            if doc_id not in corpus
        }
    )
    if missing_docs:
        raise RuntimeError(f"missing corpus docs referenced by qrels: {missing_docs[:10]}")

    return corpus, sort_mapping_by_id(queries), sort_nested_qrels(qrels)


def load_with_fallbacks(
    specs: list[tuple[str, str | None, str]],
    **dataset_kwargs: Any,
) -> list[dict[str, Any]]:
    from datasets import load_dataset

    last_error: Exception | None = None
    for dataset_name, config_name, split_name in specs:
        try:
            kwargs = dict(dataset_kwargs)
            if config_name is None:
                dataset = load_dataset(dataset_name, split=split_name, **kwargs)
            else:
                dataset = load_dataset(dataset_name, config_name, split=split_name, **kwargs)
            return [dict(row) for row in dataset]
        except Exception as exc:  # noqa: BLE001 - we want the final loader error
            last_error = exc
    raise RuntimeError(f"failed to load dataset using specs={specs}") from last_error


def canonicalize_document(title: str, text: str) -> str:
    title = (title or "").strip()
    text = (text or "").strip()
    if title and text:
        return f"{title}\n\n{text}"
    return title or text


def sort_mapping_by_id(mapping: dict[str, str]) -> dict[str, str]:
    return dict(sorted(mapping.items(), key=lambda item: sortable_id(item[0])))


def sort_nested_qrels(qrels: dict[str, dict[str, int]]) -> dict[str, dict[str, int]]:
    sorted_qrels: dict[str, dict[str, int]] = {}
    for query_id, rels in sorted(qrels.items(), key=lambda item: sortable_id(item[0])):
        sorted_qrels[query_id] = dict(sorted(rels.items(), key=lambda item: sortable_id(item[0])))
    return sorted_qrels


def sortable_id(value: str) -> tuple[int, str]:
    try:
        return (0, f"{int(value):020d}")
    except ValueError:
        return (1, value)


def build_source_documents(corpus: dict[str, str]) -> tuple[list[SourceDocument], dict[str, Any]]:
    documents = []
    sizes = []
    for doc_id, content in corpus.items():
        if not content.strip():
            raise RuntimeError(f"corpus doc {doc_id} has empty canonical text")
        size = len(content.encode("utf-8"))
        if size > 163840:
            raise RuntimeError(f"corpus doc {doc_id} exceeds the source size limit: {size} bytes")
        documents.append(SourceDocument(id=doc_id, content=content))
        sizes.append(size)
    if not documents:
        raise RuntimeError("the source corpus is empty")
    return documents, {
        "document_count": len(documents),
        "content_bytes": sum(sizes),
        "max_document_bytes": max(sizes),
        "min_document_bytes": min(sizes),
    }


def verify_qrels_coverage(corpus: dict[str, str], qrels: dict[str, dict[str, int]]) -> None:
    qrel_doc_ids = {
        doc_id
        for relevant_docs in qrels.values()
        for doc_id, score in relevant_docs.items()
        if score > 0
    }
    missing = sorted(qrel_doc_ids.difference(corpus))
    if missing:
        raise RuntimeError(f"qrels docs missing from corpus: {missing[:10]}")


def wait_for_health(client: VecdexClient, retries: int, sleep_sec: float) -> None:
    last_error: Exception | None = None
    for attempt in range(1, retries + 1):
        try:
            health = client.health()
            print(f"[info] vecdex health attempt={attempt}: {health.get('status', 'unknown')}")
            return
        except Exception as exc:  # noqa: BLE001
            last_error = exc
            print(f"[warn] health probe attempt={attempt} failed: {exc}", file=sys.stderr, flush=True)
            time.sleep(sleep_sec)
    raise RuntimeError("vecdex health probe failed") from last_error


def reset_collection(client: VecdexClient, collection: str) -> None:
    log(f"[info] resetting collection {collection!r}")
    client.delete_collection(collection)
    client.create_collection(collection)


def ingest_documents(
    client: VecdexClient,
    collection: str,
    documents: list[SourceDocument],
    batch_size: int,
) -> dict[str, Any]:
    total = len(documents)
    started_at = time.perf_counter()
    log(f"[info] ingest start: collection={collection} documents={total} batch_size={batch_size}")
    for start in range(0, total, batch_size):
        batch = documents[start : start + batch_size]
        client.batch_upsert(collection, [{"id": doc.id, "content": doc.content} for doc in batch])
        done = start + len(batch)
        elapsed = time.perf_counter() - started_at
        log(f"[info] ingest progress={done}/{total} elapsed={elapsed:.2f}s")
    elapsed = time.perf_counter() - started_at
    return {
        "document_count": total,
        "content_bytes": sum(len(doc.content.encode("utf-8")) for doc in documents),
        "batch_count": math.ceil(total / batch_size),
        "elapsed_sec": elapsed,
        "documents_per_sec": total / elapsed if elapsed > 0 else 0.0,
    }


def probe_search_readiness(
    client: VecdexClient,
    collection: str,
    sample_queries: list[tuple[str, str]],
    modes: list[str],
    limit: int,
    retries: int,
    sleep_sec: float,
) -> None:
    if not sample_queries:
        raise RuntimeError("no sample queries available for readiness probe")

    for mode in modes:
        for attempt in range(1, retries + 1):
            query_id, query_text = sample_queries[(attempt - 1) % len(sample_queries)]
            try:
                payload = client.search(collection, query_text, mode=mode, limit=limit)
                if payload.get("items"):
                    log(
                        f"[info] readiness probe passed for mode={mode} on query={query_id} attempt={attempt}"
                    )
                    break
            except Exception as exc:  # noqa: BLE001
                print(
                    f"[warn] readiness probe failed for mode={mode} attempt={attempt}: {exc}",
                    file=sys.stderr,
                    flush=True,
                )
            time.sleep(sleep_sec)
        else:
            raise RuntimeError(f"search readiness probe did not return results for mode={mode}")


def evaluate_mode(
    client: VecdexClient,
    collection: str,
    mode: str,
    queries: dict[str, str],
    qrels: dict[str, dict[str, int]],
    limit: int,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    query_count = len(queries)

    for idx, (query_id, query_text) in enumerate(queries.items(), start=1):
        payload = client.search(collection, query_text, mode=mode, limit=limit)
        ranked_doc_ids = parse_ranked_doc_ids(payload)
        relevant = qrels.get(query_id, {})
        metrics = compute_metrics(ranked_doc_ids, relevant)

        rows.append(
            {
                "mode": mode,
                "query_id": query_id,
                "query_text": query_text,
                "num_relevant_docs": len(relevant),
                "num_results": len(ranked_doc_ids),
                "ndcg@10": metrics["ndcg@10"],
                "mrr@10": metrics["mrr@10"],
                "recall@10": metrics["recall@10"],
                "recall@20": metrics["recall@20"],
                "relevant_doc_ids": json.dumps(sorted(relevant, key=sortable_id)),
                "top_doc_ids": json.dumps(ranked_doc_ids[:20]),
            }
        )

        if idx == 1 or idx == query_count or idx % 25 == 0:
            log(f"[info] mode={mode}: processed {idx}/{query_count} queries")

    summary = summarize_mode(rows)
    return rows, summary


def parse_ranked_doc_ids(payload: dict[str, Any]) -> list[str]:
    items = payload.get("items")
    if not isinstance(items, list):
        raise RuntimeError("search response has no ranked items array")
    ids = []
    for item in items:
        if not isinstance(item, dict) or not isinstance(item.get("id"), str) or not item["id"]:
            raise RuntimeError(f"search response contains an invalid source: {item}")
        ids.append(item["id"])
    if len(ids) != len(set(ids)):
        raise RuntimeError("search response contains duplicate source IDs")
    return ids


def compute_metrics(ranked_doc_ids: list[str], relevant: dict[str, int]) -> dict[str, float]:
    relevant_ids = {doc_id for doc_id, score in relevant.items() if score > 0}
    return {
        "ndcg@10": ndcg_at_k(ranked_doc_ids, relevant, 10),
        "mrr@10": mrr_at_k(ranked_doc_ids, relevant_ids, 10),
        "recall@10": recall_at_k(ranked_doc_ids, relevant_ids, 10),
        "recall@20": recall_at_k(ranked_doc_ids, relevant_ids, 20),
    }


def ndcg_at_k(ranked_doc_ids: list[str], relevant: dict[str, int], k: int) -> float:
    actual = dcg_at_k(ranked_doc_ids, relevant, k)
    ideal_rels = sorted((score for score in relevant.values() if score > 0), reverse=True)[:k]
    ideal = 0.0
    for idx, rel in enumerate(ideal_rels, start=1):
        ideal += (2**rel - 1) / math.log2(idx + 1)
    if ideal == 0.0:
        return 0.0
    return actual / ideal


def dcg_at_k(ranked_doc_ids: list[str], relevant: dict[str, int], k: int) -> float:
    dcg = 0.0
    for idx, doc_id in enumerate(ranked_doc_ids[:k], start=1):
        rel = relevant.get(doc_id, 0)
        if rel <= 0:
            continue
        dcg += (2**rel - 1) / math.log2(idx + 1)
    return dcg


def mrr_at_k(ranked_doc_ids: list[str], relevant_ids: set[str], k: int) -> float:
    for idx, doc_id in enumerate(ranked_doc_ids[:k], start=1):
        if doc_id in relevant_ids:
            return 1.0 / idx
    return 0.0


def recall_at_k(ranked_doc_ids: list[str], relevant_ids: set[str], k: int) -> float:
    if not relevant_ids:
        return 0.0
    hits = sum(1 for doc_id in ranked_doc_ids[:k] if doc_id in relevant_ids)
    return hits / len(relevant_ids)


def summarize_mode(rows: list[dict[str, Any]]) -> dict[str, Any]:
    if not rows:
        return {
            "query_count": 0,
            "ndcg@10": 0.0,
            "mrr@10": 0.0,
            "recall@10": 0.0,
            "recall@20": 0.0,
            "avg_results": 0.0,
            "best_queries": [],
            "worst_queries": [],
        }

    def avg(metric: str) -> float:
        return sum(float(row[metric]) for row in rows) / len(rows)

    ranked_rows = sorted(
        rows,
        key=lambda row: (
            float(row["ndcg@10"]),
            float(row["mrr@10"]),
            float(row["recall@10"]),
            -len(json.loads(row["top_doc_ids"])),
            sortable_id(str(row["query_id"])),
        ),
    )

    return {
        "query_count": len(rows),
        "ndcg@10": avg("ndcg@10"),
        "mrr@10": avg("mrr@10"),
        "recall@10": avg("recall@10"),
        "recall@20": avg("recall@20"),
        "avg_results": avg("num_results"),
        "best_queries": [
            format_query_summary(row)
            for row in reversed(ranked_rows[-5:])
        ],
        "worst_queries": [
            format_query_summary(row)
            for row in ranked_rows[:5]
        ],
    }


def format_query_summary(row: dict[str, Any]) -> dict[str, Any]:
    return {
        "query_id": row["query_id"],
        "query_text": row["query_text"],
        "ndcg@10": row["ndcg@10"],
        "mrr@10": row["mrr@10"],
        "recall@10": row["recall@10"],
        "recall@20": row["recall@20"],
        "top_doc_ids": json.loads(row["top_doc_ids"]),
        "relevant_doc_ids": json.loads(row["relevant_doc_ids"]),
    }


def write_outputs(
    output_dir: Path,
    summary: dict[str, Any],
    per_query_rows: list[dict[str, Any]],
    queries: dict[str, str],
    qrels: dict[str, dict[str, int]],
) -> None:
    summary_path = output_dir / "summary.json"
    per_query_path = output_dir / "per_query.csv"
    report_path = output_dir / "report.md"

    summary_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    write_per_query_csv(per_query_path, per_query_rows)
    report_path.write_text(build_report(summary, per_query_rows, queries, qrels), encoding="utf-8")
    write_plots(output_dir / "plots", summary, per_query_rows)


def write_per_query_csv(path: Path, rows: list[dict[str, Any]]) -> None:
    if not rows:
        raise RuntimeError("per-query rows are empty")
    fieldnames = [
        "mode",
        "query_id",
        "query_text",
        "num_relevant_docs",
        "num_results",
        "ndcg@10",
        "mrr@10",
        "recall@10",
        "recall@20",
        "relevant_doc_ids",
        "top_doc_ids",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fieldnames)
        writer.writeheader()
        writer.writerows(rows)


def write_plots(plots_dir: Path, summary: dict[str, Any], rows: list[dict[str, Any]]) -> None:
    try:
        import matplotlib

        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError as exc:
        raise RuntimeError(
            "matplotlib is required to generate benchmark plots; install benchmarks/scifact/requirements.txt"
        ) from exc

    plots_dir.mkdir(parents=True, exist_ok=True)
    write_aggregate_metrics_plot(plots_dir, summary, plt)
    write_per_query_metric_boxplot(plots_dir, rows, plt, metric="ndcg@10", title="Per-query nDCG@10")
    write_per_query_metric_boxplot(plots_dir, rows, plt, metric="mrr@10", title="Per-query MRR@10")
    write_result_count_histogram(plots_dir, rows, plt)


def write_aggregate_metrics_plot(plots_dir: Path, summary: dict[str, Any], plt: Any) -> None:
    metrics_by_mode = summary["metrics_by_mode"]
    modes = list(metrics_by_mode)
    metric_names = ["ndcg@10", "mrr@10", "recall@10", "recall@20"]
    bar_width = 0.18
    x_positions = list(range(len(metric_names)))

    fig, ax = plt.subplots(figsize=(10, 6))
    for mode_idx, mode in enumerate(modes):
        offsets = [x + (mode_idx - (len(modes) - 1) / 2) * bar_width for x in x_positions]
        values = [float(metrics_by_mode[mode][metric]) for metric in metric_names]
        bars = ax.bar(offsets, values, width=bar_width, label=mode)
        for bar, value in zip(bars, values, strict=True):
            ax.text(
                bar.get_x() + bar.get_width() / 2,
                value + 0.01,
                f"{value:.3f}",
                ha="center",
                va="bottom",
                fontsize=9,
            )

    ax.set_title("SciFact aggregate retrieval metrics by vecdex mode")
    ax.set_ylabel("score")
    ax.set_ylim(0, 1.05)
    ax.set_xticks(x_positions)
    ax.set_xticklabels(metric_names)
    ax.legend(title="mode")
    ax.grid(axis="y", alpha=0.25)
    fig.tight_layout()
    fig.savefig(plots_dir / "aggregate_metrics.png", dpi=180)
    plt.close(fig)


def write_per_query_metric_boxplot(
    plots_dir: Path,
    rows: list[dict[str, Any]],
    plt: Any,
    metric: str,
    title: str,
) -> None:
    rows_by_mode: dict[str, list[dict[str, Any]]] = {}
    for row in rows:
        rows_by_mode.setdefault(str(row["mode"]), []).append(row)

    modes = list(rows_by_mode)
    data = [[float(row[metric]) for row in rows_by_mode[mode]] for mode in modes]

    fig, ax = plt.subplots(figsize=(9, 6))
    box = ax.boxplot(data, patch_artist=True, tick_labels=modes)
    palette = ["#4C78A8", "#F58518", "#54A24B", "#E45756"]
    for patch, color in zip(box["boxes"], palette, strict=False):
        patch.set_facecolor(color)
        patch.set_alpha(0.6)

    for idx, mode in enumerate(modes, start=1):
        values = data[idx - 1]
        mean_value = statistics.fmean(values) if values else 0.0
        ax.scatter(idx, mean_value, color="black", s=25, zorder=3)
        ax.text(idx + 0.06, mean_value, f"mean={mean_value:.3f}", fontsize=9, va="center")

    ax.set_title(title)
    ax.set_ylabel(metric)
    ax.set_ylim(0, 1.05)
    ax.grid(axis="y", alpha=0.25)
    fig.tight_layout()
    output_name = metric.replace("@", "_at_").replace("/", "_") + "_boxplot.png"
    fig.savefig(plots_dir / output_name, dpi=180)
    plt.close(fig)


def write_result_count_histogram(plots_dir: Path, rows: list[dict[str, Any]], plt: Any) -> None:
    rows_by_mode: dict[str, list[int]] = {}
    for row in rows:
        rows_by_mode.setdefault(str(row["mode"]), []).append(int(row["num_results"]))

    fig, ax = plt.subplots(figsize=(10, 6))
    bins = 20
    for mode, values in rows_by_mode.items():
        ax.hist(values, bins=bins, alpha=0.45, label=mode)

    ax.set_title("Documents returned per query")
    ax.set_xlabel("documents returned")
    ax.set_ylabel("query count")
    ax.legend(title="mode")
    ax.grid(axis="y", alpha=0.25)
    fig.tight_layout()
    fig.savefig(plots_dir / "result_count_histogram.png", dpi=180)
    plt.close(fig)


def build_report(
    summary: dict[str, Any],
    per_query_rows: list[dict[str, Any]],
    queries: dict[str, str],
    qrels: dict[str, dict[str, int]],
) -> str:
    lines: list[str] = []
    lines.append("# SciFact Source Benchmark")
    lines.append("")
    lines.append("## Run Summary")
    lines.append("")
    lines.append(f"- Generated at: {summary['generated_at']}")
    lines.append(f"- Dataset: {summary['dataset']}")
    lines.append(f"- Qrels split: {summary['qrels_split']}")
    lines.append(f"- Collection: `{summary['collection']}`")
    lines.append(f"- Vecdex base URL: {summary['base_url']}")
    lines.append("")
    lines.append("## Corpus Diagnostics")
    lines.append("")
    corpus_stats = summary["corpus_stats"]
    lines.append(f"- Documents: {corpus_stats['document_count']}")
    lines.append(f"- Source bytes: {corpus_stats['content_bytes']}")
    lines.append(f"- Min document bytes: {corpus_stats['min_document_bytes']}")
    lines.append(f"- Max document bytes: {corpus_stats['max_document_bytes']}")
    lines.append("- Fragmentation: server-managed")
    lines.append("")
    lines.append("## Metrics")
    lines.append("")
    lines.append("| Mode | nDCG@10 | MRR@10 | Recall@10 | Recall@20 | Avg returned docs |")
    lines.append("| --- | ---: | ---: | ---: | ---: | ---: |")
    for mode, metrics in summary["metrics_by_mode"].items():
        lines.append(
            f"| {mode} | {metrics['ndcg@10']:.4f} | {metrics['mrr@10']:.4f} | "
            f"{metrics['recall@10']:.4f} | {metrics['recall@20']:.4f} | "
            f"{metrics['avg_results']:.2f} |"
        )

    rows_by_mode: dict[str, list[dict[str, Any]]] = {}
    for row in per_query_rows:
        rows_by_mode.setdefault(str(row["mode"]), []).append(row)

    for mode, rows in rows_by_mode.items():
        lines.append("")
        lines.append(f"## {mode.title()} Best Queries")
        lines.append("")
        for row in summary["metrics_by_mode"][mode]["best_queries"]:
            lines.extend(format_report_query_block(row))

        lines.append("")
        lines.append(f"## {mode.title()} Worst Queries")
        lines.append("")
        for row in summary["metrics_by_mode"][mode]["worst_queries"]:
            lines.extend(format_report_query_block(row))

        lines.append("")
        lines.append(f"## {mode.title()} Qualitative Examples")
        lines.append("")
        for row in select_qualitative_examples(rows, queries, qrels):
            lines.extend(format_report_query_block(format_query_summary(row)))

    lines.append("")
    return "\n".join(lines)


def select_qualitative_examples(
    rows: list[dict[str, Any]],
    queries: dict[str, str],
    qrels: dict[str, dict[str, int]],
) -> list[dict[str, Any]]:
    del queries  # query text is already embedded in the row
    del qrels
    if not rows:
        return []

    ranked = sorted(
        rows,
        key=lambda row: (
            float(row["ndcg@10"]),
            float(row["mrr@10"]),
            float(row["recall@10"]),
            sortable_id(str(row["query_id"])),
        ),
    )

    examples: list[dict[str, Any]] = []
    for candidate in (ranked[0], ranked[len(ranked) // 2], ranked[-1]):
        if all(existing["query_id"] != candidate["query_id"] for existing in examples):
            examples.append(candidate)
    return examples


def format_report_query_block(row: dict[str, Any]) -> list[str]:
    top_doc_ids = row["top_doc_ids"]
    if isinstance(top_doc_ids, str):
        top_doc_ids = json.loads(top_doc_ids)
    relevant_doc_ids = row["relevant_doc_ids"]
    if isinstance(relevant_doc_ids, str):
        relevant_doc_ids = json.loads(relevant_doc_ids)

    return [
        f"- Query `{row['query_id']}`: {row['query_text']}",
        (
            f"  nDCG@10={float(row['ndcg@10']):.4f}, "
            f"MRR@10={float(row['mrr@10']):.4f}, "
            f"Recall@10={float(row['recall@10']):.4f}, "
            f"Recall@20={float(row['recall@20']):.4f}"
        ),
        f"  Relevant docs: {', '.join(relevant_doc_ids[:10]) if relevant_doc_ids else '(none)'}",
        f"  Top docs: {', '.join(top_doc_ids[:10]) if top_doc_ids else '(none)'}",
    ]


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt as exc:
        print("[error] interrupted", file=sys.stderr)
        raise SystemExit(130) from exc
