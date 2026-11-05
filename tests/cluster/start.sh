#!/bin/sh
set -eu
for p in 17389 17390 17391 17392 17393 17394 17395 17396; do
  mkdir -p /tmp/search-$p
  cat > /tmp/search-$p/valkey.conf <<CONF
port $p
cluster-enabled yes
cluster-config-file nodes.conf
cluster-announce-ip 127.0.0.1
cluster-node-timeout 3000
repl-diskless-sync-delay 0
protected-mode no
dir /tmp/search-$p
daemonize yes
logfile /tmp/search-$p/server.log
save ""
appendonly no
loadmodule /usr/lib/valkey/libsearch.so --use-coordinator --reader-threads 2 --writer-threads 1 --max-worker-suspension-secs 0
CONF
  valkey-server /tmp/search-$p/valkey.conf
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    if valkey-cli -p $p ping >/dev/null 2>&1; then break; fi
    sleep 0.1
  done
  if ! valkey-cli -p $p ping; then cat /tmp/search-$p/server.log; exit 1; fi
done

valkey-cli --cluster create 127.0.0.1:17389 127.0.0.1:17390 127.0.0.1:17391 127.0.0.1:17392 127.0.0.1:17393 127.0.0.1:17394 --cluster-replicas 1 --cluster-yes
for p in 17392 17393 17394; do
  synced=no
  for attempt in $(seq 1 150); do
    info=$(valkey-cli --raw -p "$p" info replication | tr -d '\r')
    if echo "$info" | grep -q '^master_link_status:up$' && echo "$info" | grep -q '^master_sync_in_progress:0$'; then
      synced=yes
      break
    fi
    sleep 0.1
  done
  if [ "$synced" != yes ]; then echo "replica $p failed to synchronize" >&2; exit 1; fi
done
sleep 0.5
touch /tmp/cluster-ready
exec tail -f /dev/null
