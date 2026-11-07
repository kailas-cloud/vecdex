# Operating Vecdex with Valkey Search

This runbook covers the official Valkey Bundle 9.1.2 image pinned in Compose.
Its runtime reports Valkey 9.1.1 and Search 1.2.1 (`66049`). No engine patch is
required or supported. Check the runtime versions as well as the image tag.

The supported scaling policy for this release is **planned expansion with a
maintenance window; no routine shard removal**. Small-corpus online expansion
and contraction have passed, but larger online runs have also crashed Search.
Free memory alone does not prevent that separate topology bug. Evidence and
native reproducers are in [KNOWN_FAILURES.md](../tests/cluster/KNOWN_FAILURES.md).

## Node configuration

Apply the same Search settings to every primary and replica, including nodes
that are about to join. A shard consists of a primary and at least one replica;
adding only an empty process does not add usable capacity until slots move.

| Setting | Recommendation | Reason |
| --- | --- | --- |
| `search.writer-threads` | **1** on Search 1.2.1 | Small transactional mutations can otherwise remain unindexed indefinitely: [#1651](https://github.com/valkey-io/valkey-search/issues/1651) |
| `search.max-worker-suspension-secs` | **0** on Search 1.2.1 | Resume writers at fork birth; avoid the circular wait described in [#1260](https://github.com/valkey-io/valkey-search/issues/1260) |
| `search.reader-threads` | Start with **2**, then measure CPU and query latency | This is the tested cluster configuration, not a universal throughput optimum |
| `--use-coordinator` module argument | Required on every cluster participant | Enables native distributed Search; set at module startup |
| `maxmemory-policy` | **noeviction** | Eviction can independently remove sources, catalogs, fragments or commit receipts |
| `maxmemory` | Explicit byte limit below the physical memory limit | Leave room for replication, RSS/fragmentation, persistence and copy-on-write |
| `appendonly`, `appendfsync` | `yes`, `everysec` as a starting persistence policy | Select a stronger policy if the required RPO demands it; this is not zero-loss synchronous replication |
| Primary/replica placement | Separate failure domains, comparable RAM and CPU | A replica must have enough capacity to become a primary and rebuild its index |

For a new 16 GiB dedicated container, `maxmemory 8gb` is a conservative starting
point for qualification, not a measured production limit. Raise it only after
measuring peak container memory during snapshots, full sync, migration and index
backfill under the intended write rate. A write-heavy fork can use much more
memory than steady state. See [Valkey administration](https://valkey.io/topics/admin/)
and [replication memory](https://valkey.io/topics/replication/).

Budget the Vecdex/embedding processes separately from Valkey nodes. In particular,
`maxmemory` does not limit ONNX native allocations, model sessions or inference
buffers, and Go heap metrics do not describe their full resident memory. Qualify
the model, concurrent requests and accepted batch sizes against a separate process
memory limit; do not treat spare shard capacity as embedding-service headroom.

Example additions to the node configuration:

```conf
cluster-enabled yes
cluster-node-timeout 15000
maxmemory 8gb
maxmemory-policy noeviction
appendonly yes
appendfsync everysec
search.writer-threads 1
search.reader-threads 2
search.max-worker-suspension-secs 0
```

The timeout and memory values above are examples to qualify against the deployment.
Amend the existing Search module load line to include `--use-coordinator`; do not
load Search a second time. The disposable fixture shows the module-argument form:

```conf
loadmodule /usr/lib/valkey/libsearch.so --use-coordinator --reader-threads 2 --writer-threads 1 --max-worker-suspension-secs 0
```

Use either the installation's module arguments or its supported module config
mechanism. Both root Compose and test Compose supply the two required workaround
settings explicitly. Production deployments must also provide their own memory
limits; development Compose does not choose a production `maxmemory` for you.

Valkey data ports, cluster bus ports and the Search coordinator listening ports
must be reachable between the appropriate participants. Read the actual
coordinator port from `INFO MODULES`; do not assume it is the data port.

Before starting Vecdex, verify every node:

```sh
valkey-cli INFO MODULES
valkey-cli CONFIG GET search.writer-threads search.max-worker-suspension-secs maxmemory maxmemory-policy
valkey-cli INFO replication
valkey-cli INFO memory
```

Vecdex startup and `/health` reject the known Search 1.2.1 configuration unless
there is one active writer and `search.max-worker-suspension-secs=0`. The writer
check also applies to standalone nodes. Checking the suspension setting requires
`CONFIG GET`; a denied or malformed response fails readiness. Vecdex reads these
settings and does not change the server configuration. Persist operator changes
through the deployment's normal configuration mechanism, not just an ephemeral
`CONFIG SET`.

For a healthy node during maintenance, the equivalent live configuration change
is the following; persist the same settings in its deployment configuration:

```sh
valkey-cli CONFIG SET search.max-worker-suspension-secs 0 search.writer-threads 1
```

This prevents the known conditions; it does not recover a main thread that has
already deadlocked and can no longer process commands.

## Writers, transactions and search consistency

The temporary workaround for [#1651](https://github.com/valkey-io/valkey-search/issues/1651)
is one writer per node, enforced by readiness. It removes the demonstrated
indefinite small-transaction queue stall. It does not make asynchronous indexing
linearizable, nor repair the separate topology consistency-check crash.
This is one writer per node, not a global write lock: different shards still
index concurrently. Increase write capacity through qualified shard expansion
or a tested official fixed release, rather than raising this setting on 1.2.1.

Keep `WATCH`/`MULTI`/`EXEC`. The source version, source catalog, expiry state,
durable commit outcome and idempotency receipt must publish together in the same
partition. Sending those commands separately would permit partially committed
documents and break CAS and restart recovery. Replacing them with Lua also hits
the same Search mutation queue and violates the intended bounded native commit
protocol. Padding transactions to reach the writer count is not a supported fix.

A direct local `FT.SEARCH ... LOCALONLY` can drain a node's private mutation
queue, which is useful for diagnosis. Vecdex does not use periodic dummy searches
as a consistency protocol: every primary and replica would need coverage, new
writes can race the probes, and the query path itself can deadlock with the old
fork setting. `FT.INFO` is not that barrier; the affected private queue can be
nonempty while `mutation_queue_size` reports zero.

Vecdex requests `ALLSHARDS CONSISTENT` and rejects malformed backend results.
These flags do not fence an unfinished legacy slot handoff on Search 1.2.1.
Do not switch to partial results to hide a failed shard. Use source `Get` and CAS
for a particular committed version; Search is not a transactional snapshot of all
concurrent source writes.

## Monitoring and alert activation

**Compose does not deploy Prometheus, Alertmanager, an exporter, notification
routes or an autoscaler. No operational notifications are enabled merely by
starting Vecdex.** The application exposes `/metrics`, and its health check
validates the current storage participants and known configuration constraints.

[vecdex.rules.yml](../deploy/monitoring/vecdex.rules.yml) provides ten alert rules
and a per-node memory recording rule. To activate them:

1. Scrape every Vecdex `/metrics` endpoint.
2. Scrape every Valkey primary and replica through an exporter using the
   `redis_` metric namespace. For example, `redis_exporter` supports Valkey;
   enable module metrics for the Search rules. Do not enable key scans, key-size
   scans or exporter Lua scripts for these rules.
3. Attach `service="vecdex"`, a stable `cluster` label and an `instance` label
   identifying the node to both application and exporter targets. The supplied
   rules select this service label so unrelated Valkey deployments are excluded.
   Use separate scrape jobs or targets for every member, including replacements.
4. Load the rule file through Prometheus `rule_files`, verify that all referenced
   metrics exist, and connect Alertmanager routes to the operating team.
5. Exercise notification delivery. A checked YAML file does not establish that
   a deployment is being monitored.

Example Prometheus wiring for one exporter per Valkey node; replace endpoints
and enumerate all primaries and replicas. This is an external deployment example,
not an extra Compose service:

```yaml
rule_files:
  - /etc/prometheus/rules/vecdex.rules.yml
scrape_configs:
  - job_name: vecdex
    static_configs:
      - targets: [vecdex-0:8080, vecdex-1:8080]
        labels: {service: vecdex, cluster: agent-memory}
  - job_name: vecdex-valkey
    static_configs:
      - targets: [primary-0-exporter:9121, replica-0-exporter:9121]
        labels: {service: vecdex, cluster: agent-memory}
```

| Supplied alert | Condition | Operator response |
| --- | --- | --- |
| `VecdexValkeyUnavailable` | `redis_up=0` for 1 minute | Check the node and its logs; stop topology changes |
| `VecdexValkeyMemoryLimitMissing` | `maxmemory=0` for 5 minutes | Set an explicit, qualified limit |
| `VecdexValkeyMemoryWarning` | Per-node memory ratio >=60% for 10 minutes | Check growth rate and prepare capacity |
| `VecdexValkeyScaleOutRequired` | Ratio >=65% for 5 minutes | Start the planned expansion; normally finish before 80% |
| `VecdexValkeyMemoryCritical` | Ratio >=80% for 1 minute | Limit ingestion if needed; preserve migration headroom |
| `VecdexValkeyEviction` | Any eviction in 5 minutes | Treat as a storage incident; verify source and index inventories |
| `VecdexSearchWriterConfiguration` | Active writers differ from 1 for 2 minutes | Check configuration or a suspended/stuck writer pool |
| `VecdexSearchSnapshotFailure` | Search RDB save failures increase | Check persistence and engine logs before another topology change |
| `VecdexHTTPServerErrors` | HTTP 5xx >5% for 5 minutes at >0.2 requests/second | Check storage and embedder errors |
| `VecdexEmbeddingBudgetExhausted` | Remaining budget is zero for 5 minutes | Review the provider's budget; rejection can be intentional |

The supplied ratio is `used_memory / maxmemory`, including buffers. It is
deliberately conservative. It is not physical RAM utilization or the precise
eviction accounting value. For the latter, inspect `mem_not_counted_for_evict`.
The rules flag replicas too. For a replica-only capacity alert, inspect replication
buffers and backfill before deciding to add a data shard; this file does not
automatically provision capacity.
Do not add `search_used_memory_bytes` to `used_memory` without checking allocator
accounting; that can double-count memory. Read [Valkey memory accounting](https://valkey.io/topics/lru-cache/).

The operator must additionally wire platform-specific alerts for container/host
memory, disk space, OOM kills, process restarts, lost replicas, replication lag,
uncovered slots and missing scrape targets. These are not supplied as generic
expressions because their metrics and labels depend on the deployment. An
exporter target removed from discovery no longer produces `redis_up=0`; monitor
the expected node inventory as well as target health.

Monitor Search memory, reclaimable graph bytes, indexing failures, backfill,
query failures and queue sizes using `INFO MODULES`/`INFO SEARCH` and per-index
`FT.INFO <index> LOCAL`. Exporter support varies; absence of a series is not a
healthy zero. Keep a search canary over a known set of source IDs in all three
modes. A zero mutation queue alone cannot detect #1651 or silent omissions.
The repository does not deploy that active canary automatically.

Validate the supplied rules with:

```sh
cd deploy/monitoring
promtool check rules vecdex.rules.yml
promtool test rules vecdex.rules.test.yml
```

## When to add a shard

Start at **65–70% of `maxmemory` on the fullest primary**, using the 65% alert as
the initial trigger. The 60/65/80% thresholds are starting policy, not proven safe
limits for every workload. Do not wait for a rejected write or an OOM kill.
Monitor the physical memory of replicas too: they normally ignore `maxmemory`.

Estimate the worst-case time `T` for provisioning, full sync, schema convergence,
payload transfer, index rebuild and verification. For each affected node, require:

```text
physical free memory > growth during T + temporary operation peak + safety reserve
```

Use the observed upper growth rate and operation duration, not their averages.
If the projected reserve will be exhausted before the procedure finishes, start
earlier than the percentage trigger or reduce ingestion. Valkey can reject writes
at `maxmemory` without a process crash; an OS/container OOM kill is a different
failure. Plan for both limits.

For three equal shards at 70%, ideal redistribution onto four leaves about
52.5% data utilization. Fixed index allocations, uneven source sizes, tombstones,
replication buffers and RSS mean actual usage will differ. Balance and verify
bytes and live vectors as well as slot counts. Moving keys does not guarantee
that the old HNSW graph or allocator immediately returns physical memory.

## How to add a shard

This is an external operational procedure. Vecdex does not provision machines,
move slots or provide a global maintenance-mode API. Run the procedure through
the deployment's operator/CLI and route traffic there. The scripts in
`tests/cluster/` operate fixed disposable ports and directories; they are evidence
and examples, not scripts to point at a production cluster.

1. **Preflight.** Record membership, role/replica mapping, slot ownership,
   configurations, source inventory and canary versions/results. Check that
   replicas are synchronized, indexes are ready, persistence works and the
   measured physical-memory reserve covers the operation. Confirm a restorable
   backup and sufficient free disk for snapshots and rebuilds.
2. **Drain traffic before changing membership.** Pause ingestion and search
   across all Vecdex instances and other Search clients, including background
   probes that issue distributed searches. Stop schema-changing maintenance and
   recovery workers as well. Let in-flight requests finish. Use
   an explicit maintenance response or stop the application processes; do not
   serve partial successful searches. The raw engine can crash on membership
   changes even with an empty index, so ample RAM is not a substitute for this
   step on Search 1.2.1.
3. **Provision a primary and replica.** Use the same official image, qualified
   limits and required Search settings. Join the primary and wait until every
   old member and the new primary reports the expanded membership and full slot
   coverage. Only then add the replica: `add-node` returns before gossip has
   converged, while the next `add-node` requires agreement across the cluster.
   Assign the replica with `CLUSTER REPLICATE`, and wait for every member to see
   the full topology again. Verify the replica's primary ID,
   `master_link_status:up` and `master_sync_in_progress:0`.
4. **Bootstrap schemas before moving payload.** Select a genuinely empty slot
   owned by an existing primary and verify it with `CLUSTER COUNTKEYSINSLOT`.
   Assign that slot to the new primary through IMPORTING/MIGRATING and SETSLOT
   NODE on all primaries. No application writes may populate it during this
   step. Wait for every existing collection index on the incoming primary and
   replica, and verify the old participants too. Require `state=ready`, no
   backfill, no mutation queue and no indexing failures. Use `FT.INFO ... LOCAL`;
   do not use distributed consistent INFO as a migration probe on this pin.
5. **Transfer slots with the native CLI.** Inspect the rebalance plan first.
   The tested payload pipeline moves one key at a time. Choose a migration
   timeout below the cluster failure timeout but above measured command latency.
   With larger pipelines, indexed RESTORE commands stalled and caused a source
   failover in earlier tests. Do not force replacement of conflicting values.
6. **Verify the transfer.** Check actual slot ownership and all 16,384 slots,
   not just the CLI exit code. There must be no remaining IMPORTING/MIGRATING
   markers, unreachable owners or conflicting owners. Verify schemas and index
   readiness on every primary and replica again.
7. **Reclaim retained index memory when needed.** Measure live vectors,
   `search_index_reclaimable_memory`, used memory, RSS and container memory.
   The tested procedure rebuilds through replicas using the official
   `search.skip-rdb-load` option, waits for full source sync and complete backfill,
   then performs coordinated failover before rebuilding the former primary.
   Process one shard at a time and keep a complete healthy copy throughout.
8. **Verify and resume.** Reconnect Vecdex, check health, compare the complete
   acknowledged source inventory, verify known versions and canary IDs in
   keyword/semantic/hybrid search, then enable a small write stream before full
   ingestion. Confirm memory stayed below both data and physical limits and
   that no participant restarted or reported indexing failures.

Native CLI command shapes, after supplying the deployment's endpoints:

```sh
set -eu
valkey-cli --cluster add-node "$NEW_PRIMARY" "$SEED"
# Wait for the expanded membership and full slot coverage on every member.
# Do not issue the next add-node while any old member still has the old view.
valkey-cli --cluster add-node "$NEW_REPLICA" "$SEED"
# Wait for complete gossip views before assigning the replica.
NEW_ID=$(valkey-cli -e -u "redis://$NEW_PRIMARY" CLUSTER MYID)
valkey-cli -e -u "redis://$NEW_REPLICA" CLUSTER REPLICATE "$NEW_ID"
# Wait for full synchronization, as described in step 3.
```

For the empty-slot bootstrap, supply `SOURCE_PRIMARY` (the current owner),
`EMPTY_SLOT` and `ALL_PRIMARIES` (all current primary endpoints, including the new
one). Keep traffic drained throughout; inspect each command's result. This
example uses plain endpoints; use the deployment's normal CLI connection options.

```sh
SOURCE_ID=$(valkey-cli -e -u "redis://$SOURCE_PRIMARY" CLUSTER MYID)
test "$(valkey-cli -e -u "redis://$SOURCE_PRIMARY" CLUSTER COUNTKEYSINSLOT "$EMPTY_SLOT")" = 0
valkey-cli -e -u "redis://$NEW_PRIMARY" CLUSTER SETSLOT "$EMPTY_SLOT" IMPORTING "$SOURCE_ID"
valkey-cli -e -u "redis://$SOURCE_PRIMARY" CLUSTER SETSLOT "$EMPTY_SLOT" MIGRATING "$NEW_ID"
for primary in $ALL_PRIMARIES; do
  valkey-cli -e -u "redis://$primary" CLUSTER SETSLOT "$EMPTY_SLOT" NODE "$NEW_ID"
done
```

Capture the index names with `FT._LIST` before maintenance. For each captured
index, wait for the step 4 readiness fields on **every** participant, including
the new replica. Do not proceed just because the incoming primary is ready.

```sh
valkey-cli -u "redis://$NEW_PRIMARY" FT.INFO "$INDEX" LOCAL
valkey-cli -u "redis://$NEW_REPLICA" FT.INFO "$INDEX" LOCAL

# After the empty-slot bootstrap and schema checks:
valkey-cli --cluster rebalance "$SEED" --cluster-use-empty-primaries --cluster-threshold 1 --cluster-pipeline 1 --cluster-timeout 1000 --cluster-simulate
valkey-cli --cluster rebalance "$SEED" --cluster-use-empty-primaries --cluster-threshold 1 --cluster-pipeline 1 --cluster-timeout 1000
valkey-cli --cluster check "$SEED"
```

The `1000` ms timeout is the tested local value, not a universal network setting.
For an interrupted move, keep traffic drained and inspect all members' open-slot
views. Native `--cluster fix` can complete a matching transfer, but do not use
`--cluster-replace` or forget an unreachable data owner to make a check green.
Recover that owner or resolve the conflicting data first.

For persistent production nodes, do not copy `memory.sh`'s deletion of a fixture
RDB file. That fixture has AOF disabled. A production rebuild must force a real
full sync and index backfill, for example on a replacement replica with an empty
data volume and `search.skip-rdb-load=yes`. Reusing an AOF or cached replication
history may defeat the intended clean rebuild. Qualify that persistence-specific
procedure before production use; retain the original data until verification.
Do not use `FT.DROPINDEX` against the live distributed index to reclaim one node.

The bundle's core has Atomic Slot Migration commands, but its Search module
rejects them explicitly. Use the supported legacy path; do not bypass the module
capability check or substitute a patched binary.

## Why routine shard removal is disabled

This is a temporary support restriction for the pinned Search release, not a
limitation of Vecdex's partition format or a claim that only contraction crashes.
There are two independent problems:

* A primary losing its final slots changes the coordinator's cached shard map.
  Search 1.2.1 has aborted in `PerformSlotConsistencyCheck` during contraction;
  incoming nodes have also aborted during expansion. Single passing runs do not
  establish that either direction is safe under arbitrary live traffic.
* Removal concentrates data on the remaining nodes. Four shards at 70% would
  become about 93% on three even with perfect balance, before migration peaks,
  new writes and retained graph memory. A node removed for cost can therefore
  create a memory incident elsewhere.

Do not enable automatic scale-in, remove a primary that still owns slots, or
delete its replica as a shortcut. A failed-node replacement that preserves the
shard's slot capacity is a separate recovery operation. The small interrupted
contraction test remains useful evidence, but is not production scale-in approval.

If a smaller cluster is required, plan a separate maintenance migration to a new
cluster: export sources through the catalog-backed API, recreate and reindex,
verify, then switch clients. This does not preserve old collection generations,
CAS tokens or idempotency receipts and is not a transparent online migration.
Keep the old cluster until the new one has passed verification.

## Validation scope

The official fixture has demonstrated interrupted-operation recovery, one
complete small-corpus online 3→4→3/failover run, and BGSAVE on all six original
members with the required settings. A clean drained-query memory-pressure repeat
retained 16,903 sources and resumed 256 writes after expansion and index rebuild.
The larger concurrent-query scale-out still reproduced the public coordinator
crash. The pre-OOM 65–70% online trigger has not yet been qualified by that test.
These distinctions are part of the support policy; native strict xfails do not
turn the failed application contract into supported behavior.
Required PR CI checks the supported recovery procedure and fork windows, followed
by isolated native engine regressions. Online qualification is a separate manual
workflow (`.github/workflows/online-topology.yml`); its application test remains
ordinary and can fail. A green supported-path workflow does not qualify online
scale-out or scale-in on this Search release.

Before the MCP transport migration, the required writer/fork settings passed
the old HTTP profiles: 344 tests with the mock embedder, 341 with ONNX, and
350 with the two-instance Bedrock mock. The nine rate-limiting cases remain
expected failures for an unimplemented feature. ONNX passed in an isolated run;
an earlier simultaneous run exhausted the shared 6 GiB test VM and killed the
Vecdex inference process, while Valkey remained running. That application OOM
is separate from the Search topology and fork defects above.


## MCP runtime and observability

The public agent endpoint is `/mcp`. Configure `auth.tokens` with a stable
`principal_id`, role (`read`, `write` or `admin`) and Bearer key. Invalid or empty
credential configuration prevents startup. Rolling key rotation should keep the
same identity and role on both old and new keys until callers have moved.

Contexts are durable, owner-scoped records in their own versioned Valkey
namespace. Related context records, catalog entries, expiry entries and receipts
share a hash slot. Receipts last 24 hours even after deletion; do not remove them
as a way to retry a failed request. Reads do not renew context or source expiry.

Source and context writes use native conditional transactions. Context expiry
index cleanup uses Valkey's clock so host/VM clock skew cannot strand expired
catalog entries. Source confirmation is a direct read of the committed ID and
version. Search remains asynchronously indexed.

MCP metrics use the `vecdex_mcp_` prefix: `requests_total`,
`request_duration_seconds`, `response_bytes`, `conflicts_total`, `replays_total`,
`truncated_results_total`, `active_subscriptions` (listener streams), and
`subscription_poll_errors_total`. Embedding and HTTP metrics remain available.
MCP result bytes are measured before SDK envelope enrichment; HTTP metrics and
access logs cover actual transport bytes. Requests log method, tool, stable
outcome, latency and request ID, without arguments, source content or credentials.

Ordinary MCP operations have a 120-second deadline. Subscription lifetimes are
managed separately, and their streams can outlive that deadline. The obsolete
`http.write_timeout_sec` setting has been removed; delete it from existing YAML
files before upgrading because strict configuration loading rejects unknown
fields.

Shut subscription streams down before draining HTTP so termination cannot wait
indefinitely for listeners. Changes are detected by coalesced polling, including
SDK writes and changes on other replicas. A reconnect requires rereading current resources; there is no durable
notification replay. Current MCP validation is recorded under `tests/`, separate
from the historical REST counts above.
