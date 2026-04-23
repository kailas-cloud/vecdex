#!/usr/bin/env python3
# pyright: reportMissingImports=false
"""Run a chunked SciFact retrieval benchmark against a live vecdex instance."""

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
from datasets import load_dataset
from tokenizers import Tokenizer


REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_TOKENIZER_PATH = REPO_ROOT / "models" / "all-MiniLM-L6-v2" / "tokenizer.json"
DEFAULT_OUTPUT_DIR = Path(__file__).resolve().parent / "output" / "latest"
DEFAULT_BASE_URL = "http://localhost:8080"
DEFAULT_API_KEY = "test-api-key"
DEFAULT_COLLECTION = "scifact-benchmark"
DEFAULT_QRELS_SPLIT = "test"
DEFAULT_CHUNK_SIZE = 256
DEFAULT_OVERLAP = 0
CHUNK_GROUP_PRESETS = {
    "A": {"chunk_size": 128, "overlap": 16},
    "B": {"chunk_size": 256, "overlap": 32},
}
DEFAULT_TOP_K = 500
DEFAULT_LIMIT = 100
DEFAULT_MODES = ("semantic", "hybrid")
DEFAULT_BATCH_SIZE = 100
DEFAULT_SMOKE_QUERIES = 5
DEFAULT_SEARCH_RETRIES = 8
DEFAULT_SEARCH_RETRY_SLEEP = 1.5
PARENT_DOC_ID_TAG = "parent_doc_id"
CHUNK_INDEX_NUMERIC = "chunk_index"


def log(message: str) -> None:
    print(message, flush=True)


@dataclass(frozen=True)
class ChunkDocument:
    chunk_id: str
    source_doc_id: str
    chunk_idx: int
    content: str
    token_count: int


@dataclass(frozen=True)
class SearchHit:
    chunk_id: str
    source_doc_id: str
    chunk_idx: int
    score: float
    rank: int


@dataclass(frozen=True)
class AggregatedDoc:
    doc_id: str
    score: float
    best_rank: int
    best_chunk_idx: int


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
        response = self._client.delete(f"/collections/{quote(name, safe='')}")
        if response.status_code not in (204, 404):
            raise httpx.HTTPStatusError(
                f"delete collection failed: {response.status_code} {response.text}",
                request=response.request,
                response=response,
            )

    def create_collection(self, name: str) -> dict[str, Any]:
        response = self._client.post("/collections", json={"name": name})
        response.raise_for_status()
        return response.json()

    def batch_upsert(self, collection: str, documents: list[dict[str, Any]]) -> dict[str, Any]:
        response = self._client.post(
            f"/collections/{quote(collection, safe='')}/documents/batch-upsert",
            json={"documents": documents},
        )
        response.raise_for_status()
        payload = response.json()
        failed = int(payload.get("failed", 0))
        if failed:
            items = payload.get("items", [])
            errors = [item for item in items if item.get("status") == "error"]
            raise RuntimeError(f"batch upsert failed for {failed} items: {errors[:3]}")
        return payload

    def search(
        self,
        collection: str,
        query: str,
        mode: str,
        top_k: int,
        limit: int,
    ) -> dict[str, Any]:
        response = self._client.post(
            f"/collections/{quote(collection, safe='')}/documents/search",
            json={
                "query": query,
                "mode": mode,
                "top_k": top_k,
                "limit": limit,
                "include_vectors": False,
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
    parser.add_argument("--tokenizer-path", default=str(DEFAULT_TOKENIZER_PATH), help="path to tokenizer.json matching the local ONNX model")
    parser.add_argument("--dataset-cache-dir", default=None, help="optional Hugging Face datasets cache dir")
    parser.add_argument("--dataset", default="scifact", help="dataset label written to the report")
    parser.add_argument("--qrels-split", default=DEFAULT_QRELS_SPLIT, help="SciFact qrels split to evaluate")
    parser.add_argument(
        "--chunk-group",
        choices=sorted(CHUNK_GROUP_PRESETS),
        default=None,
        help="named chunking preset: A=128/16, B=256/32 (overrides --chunk-size/--overlap)",
    )
    parser.add_argument("--chunk-size", type=int, default=DEFAULT_CHUNK_SIZE, help="maximum tokens per chunk, including special tokens")
    parser.add_argument("--overlap", type=int, default=DEFAULT_OVERLAP, help="token overlap between adjacent chunks")
    parser.add_argument("--top-k", type=int, default=DEFAULT_TOP_K, help="vecdex top_k search parameter")
    parser.add_argument("--limit", type=int, default=DEFAULT_LIMIT, help="vecdex limit search parameter")
    parser.add_argument("--batch-size", type=int, default=DEFAULT_BATCH_SIZE, help="batch upsert size")
    parser.add_argument("--modes", nargs="+", choices=("semantic", "hybrid", "keyword"), default=list(DEFAULT_MODES), help="search modes to evaluate")
    parser.add_argument("--smoke-queries", type=int, default=DEFAULT_SMOKE_QUERIES, help="number of test queries used by the search readiness probe")
    parser.add_argument("--search-retries", type=int, default=DEFAULT_SEARCH_RETRIES, help="retries for readiness probe and empty-search retry wrapper")
    parser.add_argument("--search-retry-sleep", type=float, default=DEFAULT_SEARCH_RETRY_SLEEP, help="seconds between search retries")
    parser.add_argument("--http-timeout", type=float, default=120.0, help="HTTP timeout in seconds")
    parser.add_argument("--ingest-only", action="store_true", help="stop after building and ingesting the full chunked corpus")
    return parser.parse_args()


def main() -> int:
    benchmark_started_at = time.perf_counter()
    args = parse_args()
    apply_chunk_group_preset(args)
    validate_args(args)

    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    tokenizer_path = Path(args.tokenizer_path).resolve()
    if not tokenizer_path.is_file():
        raise FileNotFoundError(f"tokenizer not found: {tokenizer_path}")

    log(f"[info] tokenizer: {tokenizer_path}")
    if args.chunk_group:
        log(
            f"[info] chunk group={args.chunk_group}: "
            f"chunk_size={args.chunk_size} overlap={args.overlap}"
        )
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    # Match the Go embedder's raw token counting before it applies fixed-size padding.
    tokenizer.no_padding()
    tokenizer.no_truncation()
    special_tokens_per_text = len(tokenizer.encode("", add_special_tokens=True).ids)
    max_content_tokens = args.chunk_size - special_tokens_per_text
    if max_content_tokens <= 0:
        raise ValueError(
            f"chunk size {args.chunk_size} is too small for tokenizer special tokens ({special_tokens_per_text})"
        )

    log("[info] loading SciFact corpus, queries, and qrels from Hugging Face")
    corpus, queries, qrels = load_scifact_data(args.dataset_cache_dir, args.qrels_split)
    log(
        f"[info] loaded corpus={len(corpus)} docs, queries={len(queries)} test queries, qrels={sum(len(v) for v in qrels.values())}"
    )

    log("[info] building chunked corpus")
    chunks, corpus_stats = build_chunk_documents(
        corpus=corpus,
        tokenizer=tokenizer,
        chunk_size=args.chunk_size,
        overlap=args.overlap,
    )
    log(
        "[info] built "
        f"{corpus_stats['chunk_count']} chunks from {corpus_stats['document_count']} docs "
        f"(avg={corpus_stats['avg_chunks_per_doc']:.2f}, max_tokens={corpus_stats['max_chunk_tokens']})"
    )

    verify_qrels_coverage(corpus, qrels, chunks)

    client = VecdexClient(args.base_url, args.api_key, args.http_timeout)
    try:
        wait_for_health(client, retries=args.search_retries, sleep_sec=args.search_retry_sleep)
        reset_collection(client, args.collection)
        ingest_stats = ingest_chunks(client, args.collection, chunks, batch_size=args.batch_size)
        if args.ingest_only:
            log(f"[done] ingest complete for collection={args.collection}; skipping evaluation (--ingest-only)")
            return 0
        probe_search_readiness(
            client=client,
            collection=args.collection,
            sample_queries=list(queries.items())[: args.smoke_queries],
            modes=args.modes,
            top_k=args.top_k,
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
                top_k=args.top_k,
                limit=args.limit,
                retries=args.search_retries,
                sleep_sec=args.search_retry_sleep,
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
                "chunk_group": args.chunk_group,
                "chunk_size": args.chunk_size,
                "overlap": args.overlap,
                "top_k": args.top_k,
                "limit": args.limit,
                "batch_size": args.batch_size,
                "modes": list(args.modes),
                "tokenizer_path": str(tokenizer_path),
                "special_tokens_per_text": special_tokens_per_text,
                "max_content_tokens": max_content_tokens,
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


def apply_chunk_group_preset(args: argparse.Namespace) -> None:
    if not args.chunk_group:
        return

    preset = CHUNK_GROUP_PRESETS[args.chunk_group]
    args.chunk_size = int(preset["chunk_size"])
    args.overlap = int(preset["overlap"])


def validate_args(args: argparse.Namespace) -> None:
    if args.chunk_size <= 0:
        raise ValueError("--chunk-size must be positive")
    if args.overlap < 0:
        raise ValueError("--overlap cannot be negative")
    if args.top_k <= 0 or args.top_k > 500:
        raise ValueError("--top-k must be within vecdex limits (1..500)")
    if args.limit <= 0 or args.limit > 100:
        raise ValueError("--limit must be within vecdex limits (1..100)")
    if args.batch_size <= 0 or args.batch_size > 100:
        raise ValueError("--batch-size must be within vecdex batch upsert limits (1..100)")
    if args.search_retries <= 0:
        raise ValueError("--search-retries must be positive")


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


def build_chunk_documents(
    corpus: dict[str, str],
    tokenizer: Tokenizer,
    chunk_size: int,
    overlap: int,
) -> tuple[list[ChunkDocument], dict[str, Any]]:
    special_tokens_per_text = len(tokenizer.encode("", add_special_tokens=True).ids)
    max_content_tokens = chunk_size - special_tokens_per_text
    if overlap >= max_content_tokens:
        raise ValueError(
            f"overlap={overlap} must be smaller than effective chunk capacity={max_content_tokens}"
        )

    chunks: list[ChunkDocument] = []
    seen_chunk_ids: set[str] = set()
    chunk_counts_by_doc: dict[str, int] = {}
    max_chunk_tokens = 0
    min_chunk_tokens: int | None = None
    total_chunk_tokens = 0

    for doc_id, content in corpus.items():
        if not content.strip():
            raise RuntimeError(f"corpus doc {doc_id} has empty canonical text")

        doc_chunks = chunk_text(
            doc_id=doc_id,
            text=content,
            tokenizer=tokenizer,
            chunk_size=chunk_size,
            overlap=overlap,
        )
        if not doc_chunks:
            raise RuntimeError(f"document {doc_id} produced zero chunks")

        chunk_counts_by_doc[doc_id] = len(doc_chunks)
        for chunk in doc_chunks:
            if chunk.chunk_id in seen_chunk_ids:
                raise RuntimeError(f"duplicate chunk id generated: {chunk.chunk_id}")
            seen_chunk_ids.add(chunk.chunk_id)
            max_chunk_tokens = max(max_chunk_tokens, chunk.token_count)
            min_chunk_tokens = chunk.token_count if min_chunk_tokens is None else min(min_chunk_tokens, chunk.token_count)
            total_chunk_tokens += chunk.token_count
            chunks.append(chunk)

    document_count = len(corpus)
    chunk_count = len(chunks)
    avg_chunks_per_doc = chunk_count / document_count if document_count else 0.0
    avg_chunk_tokens = total_chunk_tokens / chunk_count if chunk_count else 0.0

    return chunks, {
        "document_count": document_count,
        "chunk_count": chunk_count,
        "avg_chunks_per_doc": avg_chunks_per_doc,
        "avg_chunk_tokens": avg_chunk_tokens,
        "max_chunk_tokens": max_chunk_tokens,
        "min_chunk_tokens": min_chunk_tokens or 0,
        "max_chunks_per_doc": max(chunk_counts_by_doc.values()) if chunk_counts_by_doc else 0,
        "min_chunks_per_doc": min(chunk_counts_by_doc.values()) if chunk_counts_by_doc else 0,
    }


def chunk_text(
    doc_id: str,
    text: str,
    tokenizer: Tokenizer,
    chunk_size: int,
    overlap: int,
) -> list[ChunkDocument]:
    special_tokens_per_text = len(tokenizer.encode("", add_special_tokens=True).ids)
    max_content_tokens = chunk_size - special_tokens_per_text

    token_ids = list(tokenizer.encode(text, add_special_tokens=False).ids)
    if not token_ids:
        raise RuntimeError(f"document {doc_id} could not be tokenized")

    chunks: list[ChunkDocument] = []
    token_start = 0
    chunk_idx = 0

    while token_start < len(token_ids):
        token_end = min(token_start + max_content_tokens, len(token_ids))
        accepted: ChunkDocument | None = None
        accepted_end = token_end

        while token_end > token_start:
            chunk_token_ids = token_ids[token_start:token_end]
            chunk_content = tokenizer.decode(chunk_token_ids, skip_special_tokens=True).strip()
            if not chunk_content:
                token_end -= 1
                continue

            token_count = len(tokenizer.encode(chunk_content, add_special_tokens=True).ids)
            if token_count <= chunk_size:
                accepted = ChunkDocument(
                    chunk_id=f"scifact-{doc_id}-c{chunk_idx}",
                    source_doc_id=str(doc_id),
                    chunk_idx=chunk_idx,
                    content=chunk_content,
                    token_count=token_count,
                )
                accepted_end = token_end
                break

            token_end -= 1

        if accepted is None:
            raise RuntimeError(
                f"unable to build a valid chunk for doc {doc_id} starting at token index {token_start}"
            )

        chunks.append(accepted)
        chunk_idx += 1

        if accepted_end >= len(token_ids):
            break

        next_token_start = accepted_end - overlap
        if next_token_start <= token_start:
            raise RuntimeError(
                f"invalid chunk progress for doc {doc_id}: start={token_start}, next={next_token_start}"
            )
        token_start = next_token_start

    return chunks


def verify_qrels_coverage(
    corpus: dict[str, str],
    qrels: dict[str, dict[str, int]],
    chunks: list[ChunkDocument],
) -> None:
    chunked_doc_ids = {chunk.source_doc_id for chunk in chunks}
    qrel_doc_ids = {
        doc_id
        for relevant_docs in qrels.values()
        for doc_id, score in relevant_docs.items()
        if score > 0
    }

    missing_from_corpus = sorted(doc_id for doc_id in qrel_doc_ids if doc_id not in corpus)
    if missing_from_corpus:
        raise RuntimeError(f"qrels docs missing from corpus: {missing_from_corpus[:10]}")

    missing_from_chunks = sorted(doc_id for doc_id in qrel_doc_ids if doc_id not in chunked_doc_ids)
    if missing_from_chunks:
        raise RuntimeError(f"qrels docs missing from chunked index: {missing_from_chunks[:10]}")


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


def ingest_chunks(
    client: VecdexClient,
    collection: str,
    chunks: list[ChunkDocument],
    batch_size: int,
) -> dict[str, Any]:
    total = len(chunks)
    total_tokens = sum(chunk.token_count for chunk in chunks)
    started_at = time.perf_counter()
    tokens_done = 0
    log(
        f"[info] ingest start: collection={collection} chunks={total} total_tokens={total_tokens} batch_size={batch_size}"
    )
    for start in range(0, total, batch_size):
        batch = chunks[start : start + batch_size]
        batch_started_at = time.perf_counter()
        batch_no = start // batch_size + 1
        batch_total = math.ceil(total / batch_size)
        batch_tokens = sum(chunk.token_count for chunk in batch)
        tokens_done += batch_tokens
        first_chunk_id = batch[0].chunk_id
        last_chunk_id = batch[-1].chunk_id
        log(
            f"[info] ingest batch {batch_no}/{batch_total} start: docs={len(batch)} tokens={batch_tokens} ids={first_chunk_id}..{last_chunk_id}"
        )
        documents = [
            {
                "id": chunk.chunk_id,
                "content": chunk.content,
                "tags": {PARENT_DOC_ID_TAG: chunk.source_doc_id},
                "numerics": {CHUNK_INDEX_NUMERIC: chunk.chunk_idx},
            }
            for chunk in batch
        ]
        client.batch_upsert(collection, documents)
        batch_elapsed = time.perf_counter() - batch_started_at
        elapsed = time.perf_counter() - started_at
        done = min(start + batch_size, total)
        remaining = total - done
        chunks_per_sec = done / elapsed if elapsed > 0 else 0.0
        tokens_per_sec = tokens_done / elapsed if elapsed > 0 else 0.0
        eta_sec = remaining / chunks_per_sec if chunks_per_sec > 0 else 0.0
        log(
            "[info] ingest batch "
            f"{batch_no}/{batch_total} done: progress={done}/{total} "
            f"batch_elapsed={batch_elapsed:.2f}s total_elapsed={elapsed:.2f}s "
            f"chunks_per_sec={chunks_per_sec:.2f} tokens_per_sec={tokens_per_sec:.2f} eta={eta_sec:.1f}s"
        )

    total_elapsed = time.perf_counter() - started_at
    log(
        f"[info] ingest complete: collection={collection} chunks={total} total_tokens={total_tokens} elapsed={total_elapsed:.2f}s"
    )
    return {
        "chunk_count": total,
        "total_tokens": total_tokens,
        "batch_count": math.ceil(total / batch_size),
        "elapsed_sec": total_elapsed,
        "chunks_per_sec": total / total_elapsed if total_elapsed > 0 else 0.0,
        "tokens_per_sec": total_tokens / total_elapsed if total_elapsed > 0 else 0.0,
    }


def probe_search_readiness(
    client: VecdexClient,
    collection: str,
    sample_queries: list[tuple[str, str]],
    modes: list[str],
    top_k: int,
    limit: int,
    retries: int,
    sleep_sec: float,
) -> None:
    if not sample_queries:
        raise RuntimeError("no sample queries available for readiness probe")

    sample_queries = sample_queries[: max(1, len(sample_queries))]
    for mode in modes:
        query_id, query_text = sample_queries[0]
        for attempt in range(1, retries + 1):
            try:
                payload = client.search(collection, query_text, mode=mode, top_k=top_k, limit=limit)
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
    top_k: int,
    limit: int,
    retries: int,
    sleep_sec: float,
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    query_count = len(queries)

    for idx, (query_id, query_text) in enumerate(queries.items(), start=1):
        payload = search_with_retry(
            client=client,
            collection=collection,
            query=query_text,
            mode=mode,
            top_k=top_k,
            limit=limit,
            retries=retries,
            sleep_sec=sleep_sec,
        )
        chunk_hits = parse_chunk_hits(payload)
        doc_ranking = aggregate_chunk_hits(chunk_hits)
        ranked_doc_ids = [doc.doc_id for doc in doc_ranking]
        relevant = qrels.get(query_id, {})
        metrics = compute_metrics(ranked_doc_ids, relevant)

        rows.append(
            {
                "mode": mode,
                "query_id": query_id,
                "query_text": query_text,
                "num_relevant_docs": len(relevant),
                "num_chunk_hits": len(chunk_hits),
                "unique_docs_in_top_chunks": len({hit.source_doc_id for hit in chunk_hits}),
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


def search_with_retry(
    client: VecdexClient,
    collection: str,
    query: str,
    mode: str,
    top_k: int,
    limit: int,
    retries: int,
    sleep_sec: float,
) -> dict[str, Any]:
    last_payload: dict[str, Any] | None = None
    last_error: Exception | None = None

    for attempt in range(1, retries + 1):
        try:
            payload = client.search(collection, query=query, mode=mode, top_k=top_k, limit=limit)
            last_payload = payload
            if payload.get("items") or attempt == retries:
                return payload
        except Exception as exc:  # noqa: BLE001
            last_error = exc
        time.sleep(sleep_sec)

    if last_payload is not None:
        return last_payload
    raise RuntimeError(f"search failed after {retries} retries for mode={mode}") from last_error


def parse_chunk_hits(payload: dict[str, Any]) -> list[SearchHit]:
    hits: list[SearchHit] = []
    for rank, item in enumerate(payload.get("items", []), start=1):
        tags = item.get("tags") or {}
        numerics = item.get("numerics") or {}
        source_doc_id = tags.get(PARENT_DOC_ID_TAG)
        if source_doc_id is None:
            raise RuntimeError(f"search hit missing {PARENT_DOC_ID_TAG} tag: {item}")

        raw_chunk_idx = numerics.get(CHUNK_INDEX_NUMERIC, 0)
        chunk_idx = int(raw_chunk_idx)
        hits.append(
            SearchHit(
                chunk_id=str(item.get("id", "")),
                source_doc_id=str(source_doc_id),
                chunk_idx=chunk_idx,
                score=float(item.get("score", 0.0)),
                rank=rank,
            )
        )
    return hits


def aggregate_chunk_hits(hits: list[SearchHit]) -> list[AggregatedDoc]:
    best_by_doc: dict[str, AggregatedDoc] = {}
    for hit in hits:
        candidate = AggregatedDoc(
            doc_id=hit.source_doc_id,
            score=hit.score,
            best_rank=hit.rank,
            best_chunk_idx=hit.chunk_idx,
        )
        current = best_by_doc.get(hit.source_doc_id)
        if current is None or is_better_aggregated_doc(candidate, current):
            best_by_doc[hit.source_doc_id] = candidate

    return sorted(
        best_by_doc.values(),
        key=lambda doc: (-doc.score, doc.best_rank, doc.best_chunk_idx, sortable_id(doc.doc_id)),
    )


def is_better_aggregated_doc(candidate: AggregatedDoc, current: AggregatedDoc) -> bool:
    if candidate.score != current.score:
        return candidate.score > current.score
    if candidate.best_rank != current.best_rank:
        return candidate.best_rank < current.best_rank
    if candidate.best_chunk_idx != current.best_chunk_idx:
        return candidate.best_chunk_idx < current.best_chunk_idx
    return sortable_id(candidate.doc_id) < sortable_id(current.doc_id)


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
            "avg_unique_docs_in_top_chunks": 0.0,
            "avg_chunk_hits": 0.0,
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
        "avg_unique_docs_in_top_chunks": avg("unique_docs_in_top_chunks"),
        "avg_chunk_hits": avg("num_chunk_hits"),
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
        "num_chunk_hits",
        "unique_docs_in_top_chunks",
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
    write_unique_docs_histogram(plots_dir, rows, plt)


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


def write_unique_docs_histogram(plots_dir: Path, rows: list[dict[str, Any]], plt: Any) -> None:
    rows_by_mode: dict[str, list[int]] = {}
    for row in rows:
        rows_by_mode.setdefault(str(row["mode"]), []).append(int(row["unique_docs_in_top_chunks"]))

    fig, ax = plt.subplots(figsize=(10, 6))
    bins = 20
    for mode, values in rows_by_mode.items():
        ax.hist(values, bins=bins, alpha=0.45, label=mode)

    ax.set_title("Unique documents present in top chunk hits per query")
    ax.set_xlabel("unique docs in top chunk hits")
    ax.set_ylabel("query count")
    ax.legend(title="mode")
    ax.grid(axis="y", alpha=0.25)
    fig.tight_layout()
    fig.savefig(plots_dir / "unique_docs_histogram.png", dpi=180)
    plt.close(fig)


def build_report(
    summary: dict[str, Any],
    per_query_rows: list[dict[str, Any]],
    queries: dict[str, str],
    qrels: dict[str, dict[str, int]],
) -> str:
    lines: list[str] = []
    lines.append("# SciFact Chunked Benchmark")
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
    lines.append(f"- Chunks: {corpus_stats['chunk_count']}")
    lines.append(f"- Avg chunks/doc: {corpus_stats['avg_chunks_per_doc']:.2f}")
    lines.append(f"- Avg chunk tokens: {corpus_stats['avg_chunk_tokens']:.2f}")
    lines.append(f"- Min chunk tokens: {corpus_stats['min_chunk_tokens']}")
    lines.append(f"- Max chunk tokens: {corpus_stats['max_chunk_tokens']}")
    lines.append("")
    lines.append("## Metrics")
    lines.append("")
    lines.append("| Mode | nDCG@10 | MRR@10 | Recall@10 | Recall@20 | Avg unique docs in top chunks | Avg chunk hits |")
    lines.append("| --- | ---: | ---: | ---: | ---: | ---: | ---: |")
    for mode, metrics in summary["metrics_by_mode"].items():
        lines.append(
            f"| {mode} | {metrics['ndcg@10']:.4f} | {metrics['mrr@10']:.4f} | "
            f"{metrics['recall@10']:.4f} | {metrics['recall@20']:.4f} | "
            f"{metrics['avg_unique_docs_in_top_chunks']:.2f} | {metrics['avg_chunk_hits']:.2f} |"
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
