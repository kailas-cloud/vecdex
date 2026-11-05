# Official Search acceptance and engine regressions

Use only official public Valkey/Search releases. A patched engine is not a deployable solution or acceptance target. The disposable fixture pins:

`valkey/valkey-bundle:9.1.2@sha256:1c39295cd8e3a34fc33fb46de235cf5e7bad530525b1a697eaf9f49e1c42747c`

The running image reports Valkey core 9.1.1 and Search 1.2.1 (`66049`). Check runtime versions as well as the image tag.

## Committed hashes missing from successful search (#1651)

The native reproducer in `engine_regressions/test_transaction_visibility.py` uses RESP commands directly. It does not start Vecdex or use its SDK, storage layout, chunking or document protocol.

On each of three primaries it commits one indexed HSET and one same-slot unindexed HSET through MULTI/EXEC. Both HGET checks succeed. After one second, every primary and replica reports `state=ready`, `num_docs=1`, `mutation_queue_size=0`, `backfill_in_progress=0` and `hash_indexing_failures=0`, but `num_records=0`. A native distributed FT.SEARCH then omits committed hashes. This reproduces [upstream #1651](https://github.com/valkey-io/valkey-search/issues/1651). The hashes remain stored; this is search omission, not established deletion of stored data.

Search 1.2.1 queues MULTI/EXEC mutations until their count reaches `writer-threads`. A local FT.SEARCH drains that queue, while a remote coordinator RPC bypasses it. FT.INFO does not include the private queue in its reported mutation count. Running local searches as a barrier would also encounter the separate fork-window deadlock described in [#1260](https://github.com/valkey-io/valkey-search/issues/1260).

The two-writer native case has **strict xfail**, limited to `MissingIndexedSources` on the known affected version. Setup, transport, malformed replies, non-ready indexes and unrelated assertion failures remain failures. An unexpected pass fails the suite. The one-writer case is an ordinary passing test of a supported configuration workaround. JSON evidence includes image provenance, commands, persisted keys, all local index snapshots and the native search reply. It is written to `tests/reports/engine-multi-1.json` and `engine-multi-2.json`; JUnit also embeds it.

On 2026-11-04 the official pinned image produced **1 passed, 1 xfailed**. The native response returned one of three committed sources with two writers; the one-writer case indexed and returned all three. Full application/topology acceptance with that configuration remains separate.

## Consistency checks can abort a node

On the same public image, `FT.INFO <index> PRIMARY ALLSHARDS CONSISTENT` immediately after distributed index creation aborted primary processes. During expansion, consistent FT.SEARCH also aborted a newly joined replica. Saved stacks ended in `Service::PerformSlotConsistencyCheck`. The [released source](https://github.com/valkey-io/valkey-search/blob/1.2.1/src/coordinator/server.cc) asserts that its cached cluster map and current shard exist instead of returning a retryable error when either is absent.

Vecdex probes FT.INFO LOCAL on each participant to avoid the observed distributed INFO path. This alone does not prove consistent search safe during topology changes. The application topology contract remains an ordinary acceptance test. The separate `engine_regressions/test_topology_consistency.py` creates an empty native TAG index and searches it directly over RESP while the external CLI changes topology. No Vecdex process or SDK participates. Its strict xfail accepts only a signal-6 abort whose saved stack contains both `PerformSlotConsistencyCheck` and `SearchIndexPartition` in the official Search module. Network failures, other crashes and topology script failures without that stack remain failures.

On 2026-11-04 this native reproducer aborted the newly joined primary 17395 and replica 17396 during expansion, before the contraction phase. There were 790 successful native queries before/during the failure. The full engine-only run completed in 25.27 seconds with **1 passed, 2 xfailed**. The ordinary application test independently passed expansion with 75 traffic cycles, then aborted primary 17395 during contraction. One successful expansion therefore does not establish safe online topology changes.

Image/module provenance, exact query, settings, CLI output, per-coordinator query counts and aborted ports are saved in `tests/reports/engine-topology.json`; complete per-node logs are saved as `engine-topology-<port>.log`. The snapshot and log files are generated evidence, not replacements for rerunning the reproducer.

## Successful search during an incomplete slot handoff

`engine_regressions/test_slot_handoff.py` reduces the capacity failure to one
native TAG hash. It first proves that all original primary/replica indexes are
ready and the distributed query returns that hash. It joins an empty official
primary, waits for every participant to know the complete seven-node topology,
sets IMPORTING/MIGRATING, and moves the hash with native MIGRATE. It deliberately
stops before assigning the slot to the new owner.

After the move, ASKING plus HGET on the target returns the stored value. Every
original participant reports a ready index with zero records, no pending mutations
and no indexing failures. The new zero-slot primary has not yet received the
index schema. Nevertheless, the original primary answers native FT.SEARCH
ALLSHARDS CONSISTENT successfully with `[0]`. The stored hash is not deleted;
it is outside the coordinator's searchable corpus during the handoff.

This proves a specific engine limitation: the consistency option does not fence
that legacy migration interval or require the incoming node's index readiness.
It is not evidence that the engine promises linearizable indexing. The required
Vecdex contract must still avoid successful incomplete replies during scaling.
The strict xfail accepts only a successful fanout that omits the proved imported
hash. Setup, membership, transport and unrelated server errors remain failures.
A complete result or an explicit consistency rejection passes the invariant and
therefore produces strict XPASS on the known affected version.

On the official pin, the reproducer completed with **1 xfailed in 3.58 seconds**.
Commands, image and module versions, before/after local indexes, target HGET,
local schema error, distributed reply and open-slot topology are saved in
`tests/reports/engine-handoff.json`. Recreate this disposable fixture afterward.
The earlier full application capacity runs remain ordinary failures: 63/64 semantic
sources in the first run and 62/64 keyword sources with distinct dense vectors in
the second. The second run's captured store reply already contained 62 rows before
Vecdex source/version filtering; all returned parent versions matched.

## Public configuration under evaluation

### Atomic slot migration is rejected by Search 1.2.1

Valkey 9 includes [atomic slot migration](https://valkey.io/topics/atomic-slot-migration/),
and the pinned bundle exposes `CLUSTER MIGRATESLOTS`, `GETSLOTMIGRATIONS` and
`CANCELSLOTMIGRATIONS`. However, its loaded Search module has not declared support.
A valid one-slot move between existing primaries is rejected with:

`ERR The module search does not support atomic slot migrations. Please ensure all modules have declared support for atomic slot migration and try again`

`engine_regressions/test_atomic_migration_capability.py` reproduces that rejection
without Vecdex or the SDK. It records the official image, runtime modules, exact
command and response, unchanged slot ownership, the persisted hash, and complete
native search replies before and after the rejected command. This is an ordinary
passing capability test, rather than an xfail for an unsupported feature. Evidence
is saved in `tests/reports/engine-asm-capability.json` and its JUnit report.

The supported operational path for this pinned bundle therefore still uses the
legacy migration commands. Core ASM documentation alone does not establish
compatibility with the loaded Search module. No module flag or engine binary is
patched to bypass the rejection.

The fixture uses one writer thread, two reader threads and `max-worker-suspension-secs=0`. One writer avoids the stranded small transaction batch. The suspension setting resumes workers at fork birth rather than waiting for a child-death callback on the main thread. Both are existing configuration options in the official binary. The fork-window contract below checks this configuration at runtime. Vecdex must not silently change production server configuration.

Readiness rejects the known Search 1.2.1 configuration unless every
participating primary and replica reports one active writer and
`search.max-worker-suspension-secs=0`. This includes standalone deployments, which
also use transactional ingestion and persistence. SDK/server startup preserves
the reason when its readiness deadline expires. The settings are an operator
responsibility; Vecdex does not issue CONFIG SET against
production servers. A single writer avoids the transaction visibility defect but
does not fix the independent coordinator crash during topology changes.

The `public-search-cluster` CI job runs native indexing, coordinator distribution
and SDK contracts first, then the isolated engine reproducers. It recreates the fixture between destructive handoff and crash cases, uses only the
official pinned fixture and uploads JUnit, JSON evidence, container provenance and
all node logs as `official-search-cluster-evidence`, even on unexpected failure.

## Capacity and index-memory reclamation on the public image

On 2026-11-05, `TestClusterMemoryReclamationIntegration` passed in 181.96 seconds
on the pinned official image, with 1536-dimensional distinct dense vectors,
unchanged HNSW defaults and a 128 MiB noeviction maxmemory setting on every node.
It reached a real OOM after 16,048 acknowledged bulk writes, expanded 3+3 to 4+4,
rebuilt old indexes through their replicas, verified the complete source/index
inventory and resumed 256 writes under the unchanged limits.

After slot migration, the four primary indexes contained 4,078, 4,053, 3,930 and
4,052 vectors. Every participant was below maxmemory, but the old primary RSS
remained 135.58, 137.80 and 150.95 MiB. Search still reported 5.72, 6.42 and
11.60 MiB of reclaimable graph memory. A replica-first full resync with the
public `skip-rdb-load` option, completed backfill and coordinated promotion
reduced the corresponding primary RSS to 105.32, 106.29 and 108.86 MiB. The new
shard remained at 115.70 MiB RSS. Reclaimable bytes became zero, live vector
counts were unchanged, and accounted Search/Valkey memory also decreased.

The complete catalog and index check covered 16,112 sources: all 16,048
acknowledged bulk documents plus 64 immutable controls. Every resumed write was
read back. Raw logs and phase measurements are generated as
`tests/reports/capacity-reclamation-official.log` and
`capacity-reclamation-phases.json`. RSS decrease is now an explicit acceptance
condition as well as an observed result in that captured run.

The external procedure first assigns one empty slot to the new primary and waits
for its index and replica. Payload migration uses a single-key CLI pipeline with
a bounded timeout. The prior multi-key pipeline stalled and triggered source
failover. No engine patch or local FT.DROPINDEX was used.

**Queries were drained during migration and rebuild.** This result proves the
capacity/reclamation path with that operational procedure. It does not establish
complete search during concurrent topology changes. The separate online contract
remains mandatory, with the engine-only failures documented above.

## Topology command verification

A successful CLI exit does not prove migration completed. The fixture verifies that the added primary owns exactly 4096 slots after expansion, zero before removal, and that no open slot markers remain. It waits for replica synchronization and complete cluster views on all eight participants before moving slots. These checks also prevent starting RESTORE-ASKING while the newly joined primary still reports CLUSTERDOWN.

## Outstanding acceptance

Low-memory scale-out with concurrent queries and final acceptance of the latest
complete tree remain outstanding. Low-memory scale-out and HNSW reclamation
passed with queries drained as documented above; that result must also be
repeated after the later implementation changes. Earlier experiments with a
local engine patch do not satisfy acceptance and are not supported.

## Interrupted operations and online topology

On 2026-11-05, the ordinary interrupted-topology contract passed in 92.53 seconds
on the official pin. It deliberately moved one source parent while leaving the
rest of its slot on the original owner, read through ASK, resumed expansion and
contraction, and repeated both completed commands. All 64 stable sources retained
their content, metadata and original versions after recovery and automatic
failover. A conditional write using the original version succeeded; reusing that
version was rejected. Evidence is in `tests/reports/interrupted-topology.json`
and `interrupted-topology-*.log`.

The first failover run exposed a Vecdex client discovery issue: a restarted
primary returned as a replica but was absent from the client's cached topology.
The supported rueidis periodic shard refresh now rediscovers it without waiting
for another MOVED reply. The before-fix evidence remains in
`interrupted-topology-before-client-refresh.json`.

With schema bootstrap before payload transfer, the ordinary online contract
also passed once in 107.47 seconds: expansion, contraction and failover completed
with 61, 39 and 25 SDK traffic cycles respectively. Each cycle writes, reads,
deletes and checks all stable IDs in keyword, semantic and hybrid search.
Temporary request errors are allowed; a successful incomplete reply fails the
contract. Evidence is in `tests/reports/online-topology-warmed.log` and
`online-topology-*.log`. This small-corpus result does not prove the memory-pressure
contract, nor eliminate the independent native crash under raw CLI migration.

The latest memory-pressure run did not pass: after 17,089 acknowledged bulk
writes and a real OOM, the incoming replica 17396 aborted during schema bootstrap.
Its official-module stack contains signal 6, `PerformSlotConsistencyCheck` and
`SearchIndexPartition`, matching the independent native reproducer. The ordinary
application test failed in 240.95 seconds after 4,650 complete concurrent searches;
it is not marked xfail. Evidence is in `tests/reports/online-capacity-current.log`,
`online-capacity-container.json` and `online-capacity-*.log`. Schema bootstrap
therefore does not establish safe online memory-pressure scale-out on this pin.

## Fork-window acceptance

On 2026-11-05, `TestClusterForkWindowIntegration` passed in 20.95 seconds on
the official pin with the fixture configuration above. It added 512 sources
with distinct random 48 KiB metadata payloads, then ran BGSAVE separately on
every primary and replica. For each of the six participants, INFO reported an
active snapshot both before and after successful `FT.SEARCH ... LOCALONLY`
queries: 399 such queries in total. Each snapshot completed successfully and
the local stable index inventory remained unchanged.

A concurrent SDK worker completed 15 write/read/delete and distributed search
cycles. All 64 stable sources and their versions were verified after the
snapshots, including through a fresh client. This is an ordinary passing
application contract, not an xfail. Evidence is in `tests/reports/fork-window.json`,
`fork-window.log`, `fork-window-container.json` and `fork-window-*.log`.

After adding the configuration readiness guards, a clean SDK suite passed in
197.96 seconds. Its fork case passed in 20.75 seconds with 229 queries bracketed
by active snapshots on all six members. The latest `fork-window.json` describes
that rerun; `ops-sdk-isolated.log` records the complete suite. The original
`fork-window.log` retains the earlier 399-query run.
