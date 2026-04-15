# Valkey Search 1.2: live hybrid query examples

Validated on 2026-04-15 against:

- `valkey/valkey-bundle:unstable`
- `search` module version `66048` from `MODULE LIST`
- local fixture data matching `tests/conftest.py:populated_collection`
- query shapes taken from `tests/test_06_search.py`, `tests/test_07_filters.py`, and `tests/test_12_e2e_flows.py`

This file is intentionally practical. These are the raw `FT.SEARCH` forms that were actually executed against a local Valkey instance and returned results. The goal is to have a known-good reference before changing the Valkey e2e path.

## Fixture collection used for validation

Index schema:

```text
FT.CREATE vecdex:hybrid_probe:idx
  ON HASH
  PREFIX 1 vecdex:hybrid_probe:
  SCHEMA
    category TAG
    __n:priority AS priority NUMERIC
    __content TEXT
    __vector AS vector VECTOR HNSW 6 TYPE FLOAT32 DIM 1024 DISTANCE_METRIC COSINE
```

Documents:

| id | category | priority | content |
| --- | --- | ---: | --- |
| `doc-1` | `programming` | `10` | `Python is a programming language used for web development` |
| `doc-2` | `programming` | `8` | `Go is a statically typed language designed at Google` |
| `doc-3` | `infrastructure` | `9` | `Kubernetes orchestrates containerized applications` |
| `doc-4` | `database` | `7` | `Redis is an in-memory data store for caching` |
| `doc-5` | `infrastructure` | `6` | `Docker packages applications into containers` |

Query vectors were generated with the same deterministic stub embedder used by `tests/mock_embedder/server.py`.

Notation below:

- `<BLOB>` means the binary `FLOAT32` vector passed through `PARAMS 2 BLOB <BLOB>`.
- `RETURN 4 __content category priority __vector_score` is used to make responses easy to inspect.
- All examples use `DIALECT 2`.

## Known-good raw queries

### 1. Semantic baseline

This is the current Valkey KNN form already used in the repo.

```text
FT.SEARCH vecdex:hybrid_probe:idx "*=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `5`

Top hits for query vector `container`:

- `doc-3` `Kubernetes orchestrates containerized applications`
- `doc-4` `Redis is an in-memory data store for caching`
- `doc-1` `Python is a programming language used for web development`

### 2. Hybrid query from `test_hybrid_scores_in_range`

```text
FT.SEARCH vecdex:hybrid_probe:idx "programming language=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `5`

Observed order on 2026-04-15:

1. `doc-4`
2. `doc-5`
3. `doc-2`
4. `doc-3`
5. `doc-1`

This is the important proof that Valkey accepts bare text terms on the left side of `=>[KNN ...]`.

### 3. Hybrid default-mode style query from `test_hybrid_is_default_mode`

```text
FT.SEARCH vecdex:hybrid_probe:idx "containerized applications=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `5`

Top hits:

- `doc-2`
- `doc-4`
- `doc-3`

### 4. Hybrid with pre-KNN tag filter only

This is the safest translation for `filters.must=[{key: "category", match: "programming"}]` when the text term itself should not restrict the result set.

```text
FT.SEARCH vecdex:hybrid_probe:idx "(@category:{programming})=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `2`

Observed hits:

- `doc-1`
- `doc-2`

This shape maps directly to the pre-filter expectation in `tests/test_12_e2e_flows.py::TestPreKNNFilterFlow`.

### 5. Hybrid with tag filter plus text term

This is the direct hybrid form for `tests/test_07_filters.py::test_filters_with_hybrid_mode` once the text term actually matches something.

```text
FT.SEARCH vecdex:hybrid_probe:idx "(@category:{programming} programming)=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `2`

Observed hits:

- `doc-2`
- `doc-1`

Important: the same shape with `technology` instead of `programming` returned `0` results on the same dataset.

### 6. Hybrid with tag and numeric `must` filters

```text
FT.SEARCH vecdex:hybrid_probe:idx "(@category:{programming} @priority:[8 +inf] programming)=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `2`

Observed hits:

- `doc-2` with `priority=8`
- `doc-1` with `priority=10`

### 7. Hybrid with `must_not`

```text
FT.SEARCH vecdex:hybrid_probe:idx "(-@category:{database} programming)=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `1`

Observed hit:

- `doc-1`

### 8. Hybrid with `should` plus `must_not`

This matches the filter-builder style used by the semantic tests: OR group in parentheses, then negative numeric range.

```text
FT.SEARCH vecdex:hybrid_probe:idx "((@category:{programming} | @category:{infrastructure}) -@priority:[-inf (8] programming)=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Observed result count: `1`

Observed hit:

- `doc-1`

## Text-only forms that actually work

These are useful because the current Valkey BM25 path in the repo is built with the wrong left-hand syntax.

### Bare token

```text
FT.SEARCH vecdex:hybrid_probe:idx programming \
  RETURN 3 __content category priority LIMIT 0 5 DIALECT 2
```

Observed result count: `1`

### Multiple tokens

```text
FT.SEARCH vecdex:hybrid_probe:idx "programming language" \
  RETURN 3 __content category priority LIMIT 0 5 DIALECT 2
```

Observed result count: `1`

### Quoted phrase

```text
FT.SEARCH vecdex:hybrid_probe:idx '"programming language"' \
  RETURN 3 __content category priority LIMIT 0 5 DIALECT 2
```

Observed result count: `1`

## Forms that failed in live testing

### `@__content:(...)` is rejected

Both of these failed with `Invalid Query Syntax`:

```text
FT.SEARCH vecdex:hybrid_probe:idx "@__content:(programming language)" ...
FT.SEARCH vecdex:hybrid_probe:idx "@__content:(container)=>[KNN 5 @vector $BLOB]" ...
```

That matters because the current Valkey implementation in `internal/db/valkey/search.go` builds BM25 as:

```text
@__content:(<escaped query>)
```

and that form is not accepted by the tested Valkey Search 1.2 build.

### Hybrid with lexical term that has no text matches can return zero

This exact command returned `0` results on the validated fixture set:

```text
FT.SEARCH vecdex:hybrid_probe:idx "container=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

Likewise, this query also returned `0` on the same dataset:

```text
FT.SEARCH vecdex:hybrid_probe:idx "(@category:{programming} technology)=>[KNN 5 @vector $BLOB]" \
  RETURN 4 __content category priority __vector_score \
  PARAMS 2 BLOB <BLOB> DIALECT 2
```

So for Valkey e2e, test data and lexical query strings need to be chosen carefully. A hybrid query whose text part has no matches is not a safe fixture.

## What to use when fixing e2e

Use these as the first candidates for Valkey hybrid coverage:

- `"programming language=>[KNN 5 @vector $BLOB]"`
- `"containerized applications=>[KNN 5 @vector $BLOB]"`
- `"(@category:{programming})=>[KNN 5 @vector $BLOB]"`
- `"(@category:{programming} programming)=>[KNN 5 @vector $BLOB]"`
- `"(@category:{programming} @priority:[8 +inf] programming)=>[KNN 5 @vector $BLOB]"`
- `"(-@category:{database} programming)=>[KNN 5 @vector $BLOB]"`
- `"((@category:{programming} | @category:{infrastructure}) -@priority:[-inf (8] programming)=>[KNN 5 @vector $BLOB]"`

Avoid these until proven otherwise:

- `@__content:(...)`
- hybrid lexical terms that do not exist in the fixture text

## Minimal conclusions

- Valkey Search 1.2 does accept hybrid `FT.SEARCH` with free-text on the left of `=>[KNN ...]`.
- The currently implemented Valkey BM25 query shape in this repo is wrong for the tested build.
- Pre-KNN tag/numeric filters work inside the left-hand expression and can be combined with hybrid queries.
- For Valkey, working hybrid fixtures should use lexical terms that are present in document text, otherwise the query may return `0` results even when vector neighbors exist.
