# Local Search cluster tests

This disposable fixture runs three Valkey primaries with three replicas, using Bundle 9.1.2 and Search 1.2.1 with the native coordinator. Two extra processes are reserved for adding a fourth primary and its replica. Each server uses a separate data directory, but all processes share one container and its memory limit. Published loopback ports make the advertised addresses reachable from the host.

Start the fixture and run the baseline SDK contract:

```sh
docker compose -p vecdex-v3-cluster -f tests/cluster/docker-compose.yml up -d --wait
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 go test ./pkg/sdk -run 'TestDistributedCollectionSearchIntegration|TestClusterFreshTextSearchIntegration|TestTextIngestorDocumentLifecycleIntegration|TestSDKOpaqueVersionIntegration' -count=1 -v
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 go test ./internal/db/valkey -run 'TestClusterSmallTransactionSearchVisibilityLive|TestClusterSearchCoordinatorDistributionLive' -count=1 -v
```

Run the destructive topology contract against this fixture only:

```sh
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./pkg/sdk -run TestClusterTopologyIntegration -count=1 -v
```

The contract changes 3→4→3 primary topology, transfers slots with legacy SETSLOT/MIGRATE, kills a primary to test automatic failover, and reconnects the SDK. A concurrent worker writes, reads, deletes, and checks keyword/semantic/hybrid search against 64 immutable source IDs. Temporary request errors are allowed; successful incomplete search replies fail the test. The scripts verify actual slot ownership after migration, not only CLI exit codes. Use a fresh fixture for each topology run, including after a failure.

The join procedure waits for all seven members to report the new primary before
adding the eighth member. `CLUSTER MEET` returning does not prove gossip has
converged; the next CLI add-node checks cluster agreement. Repeated expansion
with all eight members already present skips this seven-member barrier.

This online application contract remains an ordinary failing/passing test. Run
it locally with the opt-in above, or through the manually dispatched
`Online topology qualification` workflow. Required PR CI covers the supported
maintenance/recovery procedure, fork windows and native engine regressions;
Search 1.2.1 online membership changes are outside the current support policy.

Cleanup:

```sh
docker compose -p vecdex-v3-cluster -f tests/cluster/docker-compose.yml down -v
```

If Compose is installed as a standalone binary, replace `docker compose` with `docker-compose`. The native storage tests can use the same seeds through `VECDEX_TEST_VALKEY_CLUSTER_ADDRS`.

The topology contract passed once with schema bootstrap before payload transfer;
raw native migration still exposes backend failures on the pinned version. See
[known failures and measured acceptance](KNOWN_FAILURES.md). Passing this
small-corpus contract does not prove low-memory scale-out. The separate capacity
contracts below use a larger corpus, per-node memory measurements, fixed limits
and a replica-first index rebuild procedure.

Interrupted-operation recovery has a separate ordinary contract. It splits a
real document slot, resumes and repeats expansion/contraction, then verifies
failover, original versions and conditional writes. Run on a fresh fixture:

```sh
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./pkg/sdk -run TestClusterInterruptedTopologyRecoveryIntegration -count=1 -v -timeout 7m
```

The fork-window contract runs BGSAVE on all six members under SDK traffic.
Successful native local searches must be bracketed by INFO showing an active
snapshot on each member, and all stable sources must survive unchanged:

```sh
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./pkg/sdk -run TestClusterForkWindowIntegration -count=1 -v -timeout 3m
```

The bundle's core supports atomic slot migration, but its Search 1.2.1 module
explicitly rejects it. A native capability check records that rejection and
verifies that both the stored hash and native search remain unchanged:

```sh
VECDEX_E2E_ENGINE_ASM_REPRO=1 \
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
python3 -m pytest -c tests/cluster/pytest.ini --confcutdir=tests/cluster tests/cluster/engine_regressions/test_atomic_migration_capability.py -v
```

Only official public images are supported. The fixture uses the pinned image above with `writer-threads=1`, `reader-threads=2` and `max-worker-suspension-secs=0`. The fork-window contract verifies these supported settings; see the known failures document for measured scope and remaining limits.

For Search 1.2.1, Vecdex readiness requires one active writer and
`search.max-worker-suspension-secs=0` on every primary and replica, including
standalone deployments. Configure these through the operator's normal server
configuration. Vecdex checks the configuration without changing it. The
[operations runbook](../../docs/operations.md) covers deployment settings and scaling.
The independent online-topology crash remains a known engine limitation.

Run isolated native engine regressions (Python and pytest, no Vecdex service required):

```sh
VECDEX_E2E_ENGINE_REPRO=1 \
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
python3 -m pytest -c tests/cluster/pytest.ini --confcutdir=tests/cluster tests/cluster/engine_regressions -v -rxX --junitxml=tests/reports/engine-regressions.xml
```

The repro temporarily changes writer counts and restores them. Run it separately from application and topology tests. The confirmed two-writer engine defect has strict xfail; the one-writer workaround is an ordinary test. Evidence JSON goes to `tests/reports/engine-multi-*.json`. Other errors remain failures.

The coordinator crash reproducer has a separate destructive opt-in. Start from a fresh fixture, run it by itself, and recreate the fixture afterward, including after an expected failure:

```sh
VECDEX_E2E_ENGINE_TOPOLOGY_REPRO=1 \
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
python3 -m pytest -c tests/cluster/pytest.ini --confcutdir=tests/cluster tests/cluster/engine_regressions/test_topology_consistency.py -v -rxX
```

It uses native RESP queries and the external slot migration CLI, with no Vecdex server or SDK. A strict xfail requires the specific Search consistency-check abort in the captured official-module stack. Generated JSON and all node logs are saved under `tests/reports/`.

The memory-pressure application contract also requires a fresh fixture:

```sh
VECDEX_E2E_MEMORY_PRESSURE=1 \
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./pkg/sdk -run TestClusterMemoryCapacityIntegration -count=1 -v -timeout 25m
```

It uses 1536-dimensional vectors and unchanged production HNSW settings, sets a
128 MiB noeviction limit on every participant, fills until an actual OOM error,
adds a fourth primary/replica, and tries an external replica-first index rebuild.
It records used memory, RSS, Search allocations, reclaimable graph bytes and live
vector counts. Success requires retaining every acknowledged document, matching
the full index inventory, releasing deleted graph allocations, and resuming writes
under the same limits. A concurrent reader requires exact stable source IDs from
every successful keyword, semantic and hybrid query. This is an ordinary
application acceptance test, not an xfail, and it has not passed full acceptance.

Run the deterministic one-key slot handoff reproducer on a fresh fixture:

```sh
VECDEX_E2E_ENGINE_HANDOFF_REPRO=1 \
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
python3 -m pytest -c tests/cluster/pytest.ini --confcutdir=tests/cluster tests/cluster/engine_regressions/test_slot_handoff.py -v -rxX
```

It deliberately leaves an importing/migrating slot open as evidence. Recreate the
fixture afterward. Do not run it together with the crash reproducer on one fixture;
the CI job recreates the fixture between these cases.

To measure reclamation independently of the confirmed online-search engine gap,
run the separate memory contract on another fresh fixture:

```sh
VECDEX_E2E_MEMORY_PRESSURE=1 VECDEX_E2E_MEMORY_RECLAMATION=1 \
VECDEX_E2E_CLUSTER_CONTAINER=vecdex-v3-cluster-search-cluster-1 \
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./pkg/sdk -run TestClusterMemoryReclamationIntegration -count=1 -v -timeout 25m
```

This contract drains queries during migration and index rebuild, then checks all
acknowledged documents, the full index inventory and resumed writes under unchanged
limits. It does not prove safe searches during topology changes. The concurrent
`TestClusterMemoryCapacityIntegration` contract remains separate and mandatory.
Both contracts use distinct dense vectors for bulk sources so payload interning
cannot hide retained HNSW vector memory.

The capacity contracts pass the collection index to `topology.sh expand`. This
experimental procedure gives the incoming primary one empty slot and waits for
the index on every primary and replica before moving document payloads. Combined with the single-key migration pipeline, it passed the separate
reclamation contract and the small-corpus online topology contract on the official
image. Concurrent search during memory-pressure scale-out remains a separate,
unpassed acceptance requirement. Calling `topology.sh expand` without
an index preserves raw CLI migration for the isolated engine crash reproducer.

The migration fixture uses `--cluster-pipeline 1 --cluster-timeout 1000` for
payload transfer. The single-key pipeline avoids sending several indexed RESTORE
commands behind a client blocked by Search ingestion; a previous larger pipeline
stalled until its 60-second timeout and caused an automatic source failover. The
bounded migration timeout is shorter than this fixture's cluster-node timeout.
These are external CLI options on the official binary, not engine modifications.
A completed transfer is still distinct from proving RSS reclamation or complete
concurrent search during migration.

## Budget credit storage protocol

```sh
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./internal/db/valkey -run TestCredit -count=1 -v -timeout 2m
```

The storage contracts cover bounded shared allocation, independent same-slot
credit pools, lease expiry, concurrent close/completion, partial installation,
and cumulative refunds after reconnecting. All writes use fixed-size native
transactions. Each case removes its own known keys without scanning the cluster.
The same cases run on an isolated standalone fixture when
`VECDEX_TEST_VALKEY_ADDR` is set. Repository contracts also check physical usage
distribution, common limits between independent replicas, historical usage,
process-loss recovery, original UTC periods, and cancellation before an upstream
call after a lost activation reply:

```sh
VECDEX_E2E_VALKEY_CLUSTER_ADDRS=127.0.0.1:17389,127.0.0.1:17390,127.0.0.1:17391 \
go test ./internal/repository/budget -run 'TestPartitionedUsage|TestLeasedCredit' -count=1 -v -timeout 2m
```
