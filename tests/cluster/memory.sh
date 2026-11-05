#!/bin/sh
# Destructive memory and index-rebuild operations for this disposable fixture.
set -eu

wait_role() {
  for attempt in $(seq 1 300); do
    actual=$(timeout 3 valkey-cli --raw -p "$1" role 2>/dev/null | head -1 || true)
    if [ "$actual" = "$2" ]; then return; fi
    sleep 0.1
  done
  echo "node $1 did not become $2" >&2
  exit 1
}

wait_replica() {
  for attempt in $(seq 1 1200); do
    info=$(timeout 3 valkey-cli --raw -p "$1" info replication 2>/dev/null | tr -d '\r' || true)
    if echo "$info" | grep -q '^master_link_status:up$' &&
       echo "$info" | grep -q '^master_sync_in_progress:0$'; then return; fi
    sleep 0.1
  done
  echo "replica $1 did not synchronize" >&2
  exit 1
}

wait_index() {
  for attempt in $(seq 1 1200); do
    info=$(timeout 3 valkey-cli --raw -p "$1" FT.INFO "$2" LOCAL 2>/dev/null | tr -d '\r' || true)
    if echo "$info" | awk '
      $0 == "state" {getline; ready = ($0 == "ready")}
      $0 == "backfill_in_progress" {getline; backfill = ($0 == "0")}
      $0 == "mutation_queue_size" {getline; empty = ($0 == "0")}
      $0 == "hash_indexing_failures" {getline; valid = ($0 == "0")}
      END {exit !(ready && backfill && empty && valid)}'; then return; fi
    sleep 0.1
  done
  echo "index $2 did not become ready on node $1" >&2
  exit 1
}

restart_replica() (
  replica_port=$1
  valkey-cli -e -p "$replica_port" shutdown nosave
  # Removing this fixture's stale snapshot forces a full sync of current hashes.
  rm -f "/tmp/search-$replica_port/dump.rdb"
  sed '/^loadmodule /s/$/ --skip-rdb-load true/' \
    "/tmp/search-$replica_port/valkey.conf" > "/tmp/search-$replica_port/rebuild.conf"
  valkey-server "/tmp/search-$replica_port/rebuild.conf"
  wait_role "$replica_port" slave
  wait_replica "$replica_port"
  wait_index "$replica_port" "$2"
)

case ${1:-} in
  limit)
    case ${2:-} in ''|*[!0-9]*) echo 'limit must be an integer byte count' >&2; exit 2;; esac
    for port in 17389 17390 17391 17392 17393 17394 17395 17396; do
      sed '/^maxmemory /d; /^maxmemory-policy /d' "/tmp/search-$port/valkey.conf" > "/tmp/search-$port/memory.conf"
      printf 'maxmemory %s\nmaxmemory-policy noeviction\n' "$2" >> "/tmp/search-$port/memory.conf"
      mv "/tmp/search-$port/memory.conf" "/tmp/search-$port/valkey.conf"
      valkey-cli -e -p "$port" config set maxmemory "$2" maxmemory-policy noeviction
    done
    ;;
  rebuild)
    primary_port=$2 index=$3
    primary_id=$(valkey-cli --raw -p "$primary_port" cluster myid)
    replica_port=$(valkey-cli --raw -p "$primary_port" cluster nodes | \
      awk -v id="$primary_id" '$4 == id && $3 ~ /slave/ {split($2,a,":"); split(a[2],b,"@"); print b[1]; exit}')
    test -n "$replica_port"
    restart_replica "$replica_port" "$index"
    # A coordinated failover waits for replication; the old primary is rebuilt
    # only after the new primary has a complete index and has taken ownership.
    valkey-cli -e -p "$replica_port" cluster failover
    wait_role "$replica_port" master
    wait_role "$primary_port" slave
    wait_replica "$primary_port"
    restart_replica "$primary_port" "$index"
    printf 'NEW_PRIMARY=%s\n' "$replica_port"
    ;;
  *) echo 'usage: memory.sh limit bytes | rebuild primary-port index' >&2; exit 2;;
esac
