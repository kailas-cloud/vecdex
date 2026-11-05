#!/bin/sh
# Destructive operations for this disposable fixture only.
set -eu
seed=127.0.0.1:17389
node_id() { valkey-cli --raw -p "$1" cluster myid; }
wait_role() {
  port=$1 expected=$2
  for attempt in $(seq 1 150); do
    role=$(valkey-cli --raw -p "$port" role 2>/dev/null | head -1 || true)
    if [ "$role" = "$expected" ]; then return; fi
    sleep 0.1
  done
  echo "port $port did not become $expected" >&2
  exit 1
}
wait_replication() {
  for attempt in $(seq 1 150); do
    info=$(valkey-cli --raw -p "$1" info replication | tr -d '\r')
    if echo "$info" | grep -q '^master_link_status:up$' && echo "$info" | grep -q '^master_sync_in_progress:0$'; then
      sleep 0.5
      return
    fi
    sleep 0.1
  done
  echo "replica $1 did not finish synchronization" >&2
  exit 1
}
wait_cluster_ready() {
  expected_members=${2:-8}
  for attempt in $(seq 1 150); do
    info=$(valkey-cli --raw -p "$1" cluster info | tr -d '\r')
    if echo "$info" | grep -q '^cluster_state:ok$' &&
       echo "$info" | grep -q '^cluster_slots_assigned:16384$' &&
       echo "$info" | grep -q '^cluster_slots_ok:16384$' &&
       echo "$info" | grep -q "^cluster_known_nodes:$expected_members\$"; then
      return
    fi
    sleep 0.1
  done
  echo "node $1 has not joined the complete cluster view" >&2
  exit 1
}
member_id() {
  members=$(valkey-cli -e --raw -p 17389 cluster nodes) || exit 1
  echo "$members" | awk -v port="$1" '{split($2,a,":"); split(a[2],b,"@"); if (b[1] == port) {print $1; exit}}'
}
owned_slots() {
  valkey-cli -e --raw -p "$1" cluster nodes | awk '$3 ~ /myself/ {
    for (i=9;i<=NF;i++) if ($i ~ /^[0-9]+(-[0-9]+)?$/) {
      split($i,a,"-"); n+=a[2] ? a[2]-a[1]+1 : 1
    } print n+0}'
}
has_open_slots() {
  for participant in 17389 17390 17391 17392 17393 17394 17395 17396; do
    if [ -z "$(member_id "$participant")" ]; then continue; fi
    view=$(timeout 3 valkey-cli -e --raw -p "$participant" cluster nodes) || exit 1
    if echo "$view" | grep -q '\['; then return 0; fi
  done
  return 1
}
assert_no_open_slots() {
  if has_open_slots; then echo 'migration left open slots' >&2; exit 1; fi
}
repair_open_slots() {
  if has_open_slots; then
    # Native repair completes a matching IMPORTING/MIGRATING pair. Never force
    # replacement of conflicting values or forget an unavailable data owner.
    valkey-cli --cluster fix "$seed" --cluster-timeout 1000 --cluster-pipeline 1 --cluster-yes
    assert_no_open_slots
  fi
}
join_new_shard() {
  if [ -z "$(member_id 17395)" ]; then valkey-cli --cluster add-node 127.0.0.1:17395 "$seed"; fi
  new_id=$(node_id 17395)
  if [ -z "$(member_id 17396)" ]; then valkey-cli --cluster add-node 127.0.0.1:17396 "$seed"; fi
  for attempt in $(seq 1 100); do
    replica_parent=$(valkey-cli -e --raw -p 17396 cluster nodes | awk '$3 ~ /myself/ {print $4}')
    if [ "$replica_parent" = "$new_id" ]; then break; fi
    if valkey-cli -e -p 17396 cluster replicate "$new_id"; then break; fi
    sleep 0.1
  done
  replica_parent=$(valkey-cli -e --raw -p 17396 cluster nodes | awk '$3 ~ /myself/ {print $4}')
  if [ "$replica_parent" != "$new_id" ]; then echo 'new replica has the wrong primary' >&2; exit 1; fi
  wait_role 17395 master
  wait_role 17396 slave
  wait_replication 17396
  for participant in 17389 17390 17391 17392 17393 17394 17395 17396; do
    wait_cluster_ready "$participant"
  done
}
assert_new_slots() {
  count=$(owned_slots 17395)
  if [ "$count" != "$1" ]; then echo "new primary owns $count slots, want $1" >&2; exit 1; fi
  assert_no_open_slots
}

# A new zero-slot primary does not receive Search schemas yet. Give it one
# empty slot, then wait for its index and replica before transferring payloads.
# Omit the index argument only for the isolated raw-engine crash reproducer.
warm_importing_index() {
  index=$1
  if [ "$(owned_slots 17395)" = 0 ]; then
    source_id=$(node_id 17389)
    target_id=$(node_id 17395)
    bootstrap_slot=
    for candidate in $(seq 0 5460); do
      count=$(valkey-cli --raw -p 17389 cluster countkeysinslot "$candidate")
      if [ "$count" = 0 ]; then bootstrap_slot=$candidate; break; fi
    done
    test -n "$bootstrap_slot"
    valkey-cli -e -p 17395 cluster setslot "$bootstrap_slot" importing "$source_id"
    valkey-cli -e -p 17389 cluster setslot "$bootstrap_slot" migrating "$target_id"
    # Core rejects reassignment if a concurrent writer populated the empty slot.
    for participant in 17389 17390 17391 17395; do
      valkey-cli -e -p "$participant" cluster setslot "$bootstrap_slot" node "$target_id"
    done
  fi
  for participant in 17389 17390 17391 17392 17393 17394 17395 17396; do
    ready=no
    for attempt in $(seq 1 1200); do
      info=$(timeout 3 valkey-cli --raw -p "$participant" FT.INFO "$index" LOCAL 2>/dev/null | tr -d '\r' || true)
      if echo "$info" | awk '
        $0 == "state" {getline; ready = ($0 == "ready")}
        $0 == "backfill_in_progress" {getline; backfill = ($0 == "0")}
        $0 == "mutation_queue_size" {getline; empty = ($0 == "0")}
        $0 == "hash_indexing_failures" {getline; valid = ($0 == "0")}
        END {exit !(ready && backfill && empty && valid)}'; then
        ready=yes
        break
      fi
      sleep 0.1
    done
    if [ "$ready" != yes ]; then echo "index $index not ready on $participant before migration" >&2; exit 1; fi
  done
}
stage_transfer() {
  key=$1 target_port=$2
  slot=$(valkey-cli -e --raw -p 17389 cluster keyslot "$key")
  source_port=$(valkey-cli -e --raw -p 17389 cluster nodes | awk -v slot="$slot" '
    $3 ~ /master/ {for (i=9;i<=NF;i++) if ($i ~ /^[0-9]+(-[0-9]+)?$/) {
      split($i,s,"-"); last=s[2] ? s[2] : s[1]
      if (slot >= s[1] && slot <= last) {split($2,a,":"); split(a[2],b,"@"); print b[1]; exit}
    }}')
  test -n "$source_port"
  test "$source_port" != "$target_port"
  test "$(valkey-cli -e --raw -p "$source_port" exists "$key")" = 1
  source_id=$(node_id "$source_port") target_id=$(node_id "$target_port")
  valkey-cli -e -p "$target_port" cluster setslot "$slot" importing "$source_id"
  valkey-cli -e -p "$source_port" cluster setslot "$slot" migrating "$target_id"
  valkey-cli -e -p "$source_port" migrate 127.0.0.1 "$target_port" "$key" 0 1000
  remaining=$(valkey-cli -e --raw -p "$source_port" cluster countkeysinslot "$slot")
  imported=$(valkey-cli -e --raw -p "$target_port" cluster countkeysinslot "$slot")
  # Deliberately stop after a real key has moved, leaving the rest of its
  # partition and slot ownership unfinished for the recovery contract.
  echo "STAGED slot=$slot source=$source_port target=$target_port remaining=$remaining imported=$imported key=$key"
}
case ${1:-} in
  expand)
    join_new_shard
    if [ -n "${2:-}" ]; then warm_importing_index "$2"; fi
    repair_open_slots
    valkey-cli --cluster rebalance "$seed" --cluster-use-empty-masters --cluster-threshold 1 --cluster-pipeline 1 --cluster-timeout 1000 --cluster-yes
    assert_new_slots 4096
    ;;
  contract)
    repair_open_slots
    new_id=$(member_id 17395)
    replica_id=$(member_id 17396)
    target_id=$(node_id 17390)
    if [ -n "$new_id" ]; then
      slots=$(owned_slots 17395)
      if [ "$slots" != 0 ]; then
        valkey-cli --cluster reshard "$seed" --cluster-from "$new_id" --cluster-to "$target_id" --cluster-slots "$slots" --cluster-pipeline 1 --cluster-timeout 1000 --cluster-yes
      fi
      assert_new_slots 0
    fi
    if [ -n "$replica_id" ]; then valkey-cli --cluster del-node "$seed" "$replica_id"; fi
    if [ -n "$new_id" ]; then valkey-cli --cluster del-node "$seed" "$new_id"; fi
    valkey-cli --cluster rebalance "$seed" --cluster-threshold 1 --cluster-pipeline 1 --cluster-timeout 1000 --cluster-yes
    assert_no_open_slots
    for participant in 17389 17390 17391 17392 17393 17394; do wait_cluster_ready "$participant" 6; done
    ;;
  stage-expand)
    test -n "${2:-}" && test -n "${3:-}"
    join_new_shard
    warm_importing_index "$2"
    stage_transfer "$3" 17395
    ;;
  stage-contract)
    test -n "${2:-}"
    stage_transfer "$2" 17390
    ;;
  recover)
    repair_open_slots
    assert_no_open_slots
    ;;
  failover)
    # The fixture starts 17392 as a replica; determine its primary dynamically.
    primary_id=$(valkey-cli --raw -p 17392 cluster nodes | awk '$3 ~ /myself/ {print $4}')
    primary_port=$(valkey-cli --raw -p 17392 cluster nodes | awk -v id="$primary_id" '$1 == id {split($2,a,":"); split(a[2],b,"@"); print b[1]}')
    test -n "$primary_port"
    valkey-cli -p "$primary_port" shutdown nosave
    wait_role 17392 master
    valkey-server "/tmp/search-$primary_port/valkey.conf"
    wait_role "$primary_port" slave
    wait_replication "$primary_port"
    ;;
  *) echo 'usage: topology.sh expand [index] | contract | failover | stage-expand index key | stage-contract key | recover' >&2; exit 2 ;;
esac
