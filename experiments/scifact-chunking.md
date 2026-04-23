# SciFact Chunking Experiments

Date: 2026-04-23

## Goal

Measure how chunking settings affect SciFact retrieval quality and benchmark cost against the live vecdex Valkey+ONNX stack.

Artifacts:
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-a`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-b`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/comparison`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-256-0`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-256-8`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-256-16`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-256-32`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/group-256-64`
- `/Users/chistopat/GolandProjects/vecdex/benchmarks/scifact/output/comparison-256-overlaps`

## Experiment 1: A/B Chunk Groups

Compared:
- Group A: `128/16`
- Group B: `256/32`

Results:
| Group | Mode | nDCG@10 | MRR@10 | Recall@10 |
| --- | --- | ---: | ---: | ---: |
| A `128/16` | semantic | 0.6575 | 0.6259 | 0.7755 |
| A `128/16` | hybrid | 0.6575 | 0.6259 | 0.7755 |
| B `256/32` | semantic | 0.6520 | 0.6125 | 0.7919 |
| B `256/32` | hybrid | 0.6532 | 0.6141 | 0.7919 |

Short readout:
- `128/16` was better on ranking quality
- `256/32` was better on recall
- `256/32` was much cheaper operationally than `128/16`

Observed delta from `A` to `B`:
- ranking got slightly worse:
  about `-0.0056` nDCG@10 and `-0.0135` MRR@10 in semantic mode
- recall got slightly better:
  about `+0.0164` Recall@10

Conclusion:
- Smaller chunks improved ranking a bit, but `128/16` was too expensive for the gain.
- `256/32` became the practical candidate for follow-up testing.

## Experiment 2: Fixed Chunk Size 256, Overlap Sweep

Compared:
- `256/0`
- `256/8`
- `256/16`
- `256/32`
- `256/64`

Quality summary:

| Group | Mode | nDCG@10 | MRR@10 | Recall@10 |
| --- | --- | ---: | ---: | ---: |
| 256/0 | semantic | 0.6409 | 0.6024 | 0.7821 |
| 256/0 | hybrid | 0.6421 | 0.6041 | 0.7821 |
| 256/8 | semantic | 0.6371 | 0.5977 | 0.7831 |
| 256/8 | hybrid | 0.6383 | 0.5994 | 0.7831 |
| 256/16 | semantic | 0.6395 | 0.5943 | 0.7931 |
| 256/16 | hybrid | 0.6408 | 0.5960 | 0.7931 |
| 256/32 | semantic | 0.6520 | 0.6125 | 0.7919 |
| 256/32 | hybrid | 0.6532 | 0.6141 | 0.7919 |
| 256/64 | semantic | 0.6478 | 0.6079 | 0.7836 |
| 256/64 | hybrid | 0.6490 | 0.6095 | 0.7836 |

Speed summary:

| Group | Ingest sec | Tokens/sec | Eval sec | Total sec |
| --- | ---: | ---: | ---: | ---: |
| 256/0 | 157.50 | 11160.29 | 2.12 | 171.67 |
| 256/8 | 261.10 | 6863.45 | 1.73 | 274.77 |
| 256/16 | 181.35 | 10076.15 | 2.12 | 196.10 |
| 256/32 | 153.28 | 12401.03 | 1.48 | 166.25 |
| 256/64 | 198.07 | 10432.57 | 1.65 | 212.00 |

Key findings:
- Best overall quality: `256/32`
- Best ingest throughput: `256/32`
- Best total runtime: `256/32`
- Best recall: `256/16`
- `256/8` was the worst tradeoff: slower than baseline with no useful quality gain
- `hybrid` only slightly beat `semantic` in every group; the gap stayed small

Conclusion:
- Overlap alone does not create a large retrieval improvement.
- The best practical setting from this sweep is `256/32`.
- The gain versus `256/0` is real but small, so chunk overlap is not the main bottleneck anymore.

## Overall Takeaway

Chunking changes gave incremental improvement, not a step change.

What improved:
- `256/32` raised ranking quality versus `256/0`

What did not happen:
- No overlap setting produced a dramatic jump in relevance metrics
- `hybrid` did not materially separate from `semantic`

Likely next steps for larger gains:
- add a reranker on top of retrieval candidates
- improve document aggregation across chunks
- test a stronger embedding model
- normalize or rewrite scientific queries before retrieval
