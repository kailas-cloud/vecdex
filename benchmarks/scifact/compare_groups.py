#!/usr/bin/env python3
"""Compare SciFact benchmark outputs across arbitrary chunking groups."""

from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any


QUALITY_METRICS = ("ndcg@10", "mrr@10", "recall@10", "recall@20")
SERIES_COLORS = (
    "#4C78A8",
    "#F58518",
    "#54A24B",
    "#E45756",
    "#72B7B2",
    "#EECA3B",
    "#B279A2",
    "#FF9DA6",
    "#9D755D",
    "#BAB0AC",
)
MODE_COLORS = {
    "semantic": "#4C78A8",
    "hybrid": "#F58518",
    "keyword": "#54A24B",
}


@dataclass(frozen=True)
class GroupResult:
    label: str
    summary_path: Path
    summary: dict[str, Any]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--summary",
        action="append",
        default=[],
        metavar="LABEL=PATH",
        help="benchmark summary input, may be repeated",
    )
    parser.add_argument("--group-a-summary", default=None, help=argparse.SUPPRESS)
    parser.add_argument("--group-b-summary", default=None, help=argparse.SUPPRESS)
    parser.add_argument("--output-dir", required=True, help="directory for comparison outputs")
    args = parser.parse_args()
    if not args.summary and not (args.group_a_summary and args.group_b_summary):
        parser.error("provide at least one --summary LABEL=PATH pair")
    return args


def main() -> int:
    args = parse_args()
    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    results = load_results(args)
    write_summary(output_dir / "comparison.json", results)
    write_report(output_dir / "comparison.md", results)
    write_plots(output_dir / "plots", results)
    return 0


def load_results(args: argparse.Namespace) -> list[GroupResult]:
    raw_items = list(args.summary)
    if not raw_items and args.group_a_summary and args.group_b_summary:
        raw_items = [f"A={args.group_a_summary}", f"B={args.group_b_summary}"]

    results: list[GroupResult] = []
    seen_labels: set[str] = set()
    for raw_item in raw_items:
        if "=" not in raw_item:
            raise ValueError(f"invalid --summary value {raw_item!r}; expected LABEL=PATH")
        label, raw_path = raw_item.split("=", 1)
        label = label.strip()
        if not label:
            raise ValueError(f"invalid --summary value {raw_item!r}; label is empty")
        if label in seen_labels:
            raise ValueError(f"duplicate summary label: {label}")
        seen_labels.add(label)
        summary_path = Path(raw_path.strip()).resolve()
        summary = json.loads(summary_path.read_text(encoding="utf-8"))
        results.append(GroupResult(label=label, summary_path=summary_path, summary=summary))
    return results


def write_summary(path: Path, results: list[GroupResult]) -> None:
    payload = {
        "groups": {
            result.label: {
                "summary_path": str(result.summary_path),
                "chunk_group": result.summary["config"].get("chunk_group"),
                "chunk_size": result.summary["config"]["chunk_size"],
                "overlap": result.summary["config"]["overlap"],
                "corpus_stats": result.summary.get("corpus_stats", {}),
                "performance": result.summary.get("performance", {}),
                "metrics_by_mode": result.summary["metrics_by_mode"],
            }
            for result in results
        }
    }
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_report(path: Path, results: list[GroupResult]) -> None:
    lines = ["# SciFact Chunking Comparison", ""]
    lines.append("## Config")
    lines.append("")
    lines.append("| Group | Chunk Size | Overlap | Chunks | Avg Chunks/Doc | Total Tokens |")
    lines.append("| --- | ---: | ---: | ---: | ---: | ---: |")
    for result in results:
        config = result.summary["config"]
        corpus_stats = result.summary.get("corpus_stats", {})
        ingest = result.summary.get("performance", {}).get("ingest", {})
        lines.append(
            f"| {result.label} | {int(config['chunk_size'])} | {int(config['overlap'])} | "
            f"{int(corpus_stats.get('chunk_count', 0))} | "
            f"{float(corpus_stats.get('avg_chunks_per_doc', 0.0)):.2f} | "
            f"{int(ingest.get('total_tokens', 0))} |"
        )

    lines.append("")
    lines.append("## Quality")
    lines.append("")
    lines.append("| Group | Mode | nDCG@10 | MRR@10 | Recall@10 | Recall@20 |")
    lines.append("| --- | --- | ---: | ---: | ---: | ---: |")
    for result in results:
        for mode in sorted(result.summary["metrics_by_mode"]):
            metrics = result.summary["metrics_by_mode"][mode]
            lines.append(
                f"| {result.label} | {mode} | "
                f"{float(metrics['ndcg@10']):.4f} | "
                f"{float(metrics['mrr@10']):.4f} | "
                f"{float(metrics['recall@10']):.4f} | "
                f"{float(metrics['recall@20']):.4f} |"
            )

    lines.append("")
    lines.append("## Speed")
    lines.append("")
    lines.append("| Group | Ingest sec | Chunks/sec | Tokens/sec | Eval sec | Eval qps | Total sec |")
    lines.append("| --- | ---: | ---: | ---: | ---: | ---: | ---: |")
    for result in results:
        performance = result.summary.get("performance", {})
        ingest = performance.get("ingest", {})
        evaluation = performance.get("evaluation", {})
        lines.append(
            f"| {result.label} | "
            f"{float(ingest.get('elapsed_sec', 0.0)):.2f} | "
            f"{float(ingest.get('chunks_per_sec', 0.0)):.2f} | "
            f"{float(ingest.get('tokens_per_sec', 0.0)):.2f} | "
            f"{float(evaluation.get('elapsed_sec', 0.0)):.2f} | "
            f"{float(evaluation.get('queries_per_sec', 0.0)):.2f} | "
            f"{float(performance.get('total_elapsed_sec', 0.0)):.2f} |"
        )

    lines.append("")
    lines.append("## Search Speed By Mode")
    lines.append("")
    lines.append("| Group | Mode | Eval sec | Queries/sec |")
    lines.append("| --- | --- | ---: | ---: |")
    for result in results:
        evaluation_by_mode = result.summary.get("performance", {}).get("evaluation", {}).get("by_mode", {})
        for mode in sorted(evaluation_by_mode):
            stats = evaluation_by_mode[mode]
            lines.append(
                f"| {result.label} | {mode} | "
                f"{float(stats.get('elapsed_sec', 0.0)):.2f} | "
                f"{float(stats.get('queries_per_sec', 0.0)):.2f} |"
            )

    lines.append("")
    lines.append("## Best Runs")
    lines.append("")
    for mode in collect_modes(results):
        best_ndcg = max(results, key=lambda result: float(result.summary["metrics_by_mode"][mode]["ndcg@10"]))
        best_recall = max(results, key=lambda result: float(result.summary["metrics_by_mode"][mode]["recall@10"]))
        fastest_search = max(
            results,
            key=lambda result: float(
                result.summary.get("performance", {}).get("evaluation", {}).get("by_mode", {}).get(mode, {}).get("queries_per_sec", 0.0)
            ),
        )
        lines.append(
            f"- {mode}: best nDCG@10={best_ndcg.label} "
            f"({float(best_ndcg.summary['metrics_by_mode'][mode]['ndcg@10']):.4f}), "
            f"best Recall@10={best_recall.label} "
            f"({float(best_recall.summary['metrics_by_mode'][mode]['recall@10']):.4f}), "
            f"fastest search={fastest_search.label} "
            f"({float(fastest_search.summary.get('performance', {}).get('evaluation', {}).get('by_mode', {}).get(mode, {}).get('queries_per_sec', 0.0)):.2f} qps)"
        )

    fastest_ingest = max(results, key=lambda result: float(result.summary.get("performance", {}).get("ingest", {}).get("tokens_per_sec", 0.0)))
    fastest_total = min(results, key=lambda result: float(result.summary.get("performance", {}).get("total_elapsed_sec", float("inf"))))
    lines.append(
        f"- ingest throughput winner: {fastest_ingest.label} "
        f"({float(fastest_ingest.summary.get('performance', {}).get('ingest', {}).get('tokens_per_sec', 0.0)):.2f} tokens/sec)"
    )
    lines.append(
        f"- wall-clock winner: {fastest_total.label} "
        f"({float(fastest_total.summary.get('performance', {}).get('total_elapsed_sec', 0.0)):.2f}s total)"
    )

    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def write_plots(plots_dir: Path, results: list[GroupResult]) -> None:
    try:
        import matplotlib

        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError as exc:
        raise RuntimeError("matplotlib is required for comparison plots") from exc

    plots_dir.mkdir(parents=True, exist_ok=True)
    write_quality_plot(plots_dir / "quality_metrics_by_group.png", results, plt)
    write_ingest_speed_plot(plots_dir / "ingest_speed_by_group.png", results, plt)
    write_search_speed_plot(plots_dir / "search_speed_by_group.png", results, plt)


def write_quality_plot(path: Path, results: list[GroupResult], plt: Any) -> None:
    series: list[tuple[str, str, dict[str, Any]]] = []
    for result in results:
        for mode in collect_modes([result]):
            series.append((result.label, mode, result.summary["metrics_by_mode"][mode]))

    x_positions = list(range(len(QUALITY_METRICS)))
    bar_width = max(0.08, min(0.18, 0.85 / max(len(series), 1)))
    fig, ax = plt.subplots(figsize=(max(12, len(series) * 1.2), 6))

    for idx, (group_label, mode, metrics) in enumerate(series):
        offsets = [x + (idx - (len(series) - 1) / 2) * bar_width for x in x_positions]
        color = SERIES_COLORS[idx % len(SERIES_COLORS)]
        label = f"{group_label}/{mode}"
        values = [float(metrics[metric]) for metric in QUALITY_METRICS]
        bars = ax.bar(offsets, values, width=bar_width, label=label, color=color)
        for bar, value in zip(bars, values, strict=True):
            ax.text(
                bar.get_x() + bar.get_width() / 2,
                value + 0.008,
                f"{value:.3f}",
                ha="center",
                va="bottom",
                fontsize=7,
                rotation=90,
            )

    ax.set_title("SciFact quality metrics by chunking group and mode")
    ax.set_ylabel("score")
    ax.set_ylim(0, 1.05)
    ax.set_xticks(x_positions)
    ax.set_xticklabels(QUALITY_METRICS)
    ax.legend(title="group/mode", ncols=max(1, min(4, len(series))))
    ax.grid(axis="y", alpha=0.25)
    fig.tight_layout()
    fig.savefig(path, dpi=180)
    plt.close(fig)


def write_ingest_speed_plot(path: Path, results: list[GroupResult], plt: Any) -> None:
    labels = [result.label for result in results]
    ingest_elapsed = [float(result.summary.get("performance", {}).get("ingest", {}).get("elapsed_sec", 0.0)) for result in results]
    chunks_per_sec = [float(result.summary.get("performance", {}).get("ingest", {}).get("chunks_per_sec", 0.0)) for result in results]
    tokens_per_sec = [float(result.summary.get("performance", {}).get("ingest", {}).get("tokens_per_sec", 0.0)) for result in results]
    total_elapsed = [float(result.summary.get("performance", {}).get("total_elapsed_sec", 0.0)) for result in results]

    fig, axes = plt.subplots(2, 2, figsize=(14, 8))
    chart_specs = [
        (axes[0][0], ingest_elapsed, "Ingest elapsed (sec)", "#4C78A8"),
        (axes[0][1], chunks_per_sec, "Ingest chunks/sec", "#F58518"),
        (axes[1][0], tokens_per_sec, "Ingest tokens/sec", "#54A24B"),
        (axes[1][1], total_elapsed, "Total benchmark elapsed (sec)", "#E45756"),
    ]

    for ax, values, title, color in chart_specs:
        bars = ax.bar(labels, values, color=color)
        for bar, value in zip(bars, values, strict=True):
            ax.text(
                bar.get_x() + bar.get_width() / 2,
                value,
                f"{value:.1f}",
                ha="center",
                va="bottom",
                fontsize=8,
            )
        ax.set_title(title)
        ax.grid(axis="y", alpha=0.25)

    fig.tight_layout()
    fig.savefig(path, dpi=180)
    plt.close(fig)


def write_search_speed_plot(path: Path, results: list[GroupResult], plt: Any) -> None:
    modes = collect_modes(results)
    labels = [result.label for result in results]
    x_positions = list(range(len(labels)))
    bar_width = max(0.12, min(0.3, 0.8 / max(len(modes), 1)))

    fig, axes = plt.subplots(2, 1, figsize=(14, 9), sharex=True)

    for idx, mode in enumerate(modes):
        offsets = [x + (idx - (len(modes) - 1) / 2) * bar_width for x in x_positions]
        qps_values = [
            float(result.summary.get("performance", {}).get("evaluation", {}).get("by_mode", {}).get(mode, {}).get("queries_per_sec", 0.0))
            for result in results
        ]
        elapsed_values = [
            float(result.summary.get("performance", {}).get("evaluation", {}).get("by_mode", {}).get(mode, {}).get("elapsed_sec", 0.0))
            for result in results
        ]
        color = MODE_COLORS.get(mode, SERIES_COLORS[idx % len(SERIES_COLORS)])
        axes[0].bar(offsets, qps_values, width=bar_width, label=mode, color=color)
        axes[1].bar(offsets, elapsed_values, width=bar_width, label=mode, color=color)

    axes[0].set_title("Search throughput by chunking group")
    axes[0].set_ylabel("queries/sec")
    axes[0].grid(axis="y", alpha=0.25)
    axes[0].legend(title="mode")

    axes[1].set_title("Search wall-clock by chunking group")
    axes[1].set_ylabel("elapsed sec")
    axes[1].set_xticks(x_positions)
    axes[1].set_xticklabels(labels)
    axes[1].grid(axis="y", alpha=0.25)

    fig.tight_layout()
    fig.savefig(path, dpi=180)
    plt.close(fig)


def collect_modes(results: list[GroupResult]) -> list[str]:
    modes: set[str] = set()
    for result in results:
        modes.update(result.summary["metrics_by_mode"])
    return sorted(modes)


if __name__ == "__main__":
    raise SystemExit(main())
