package valkey

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

// Ready checks every Search participant, including replicas: the coordinator
// can select either role to answer a shard query. Ping alone misses those failures.
func (s *Store) Ready(ctx context.Context) error {
	participants, err := s.searchClients(ctx)
	if err != nil {
		return err
	}
	for address, client := range participants {
		if err := client.Do(ctx, client.B().Ping().Build()).Error(); err != nil {
			return fmt.Errorf("search node %s: %w", address, err)
		}
		info, err := client.Do(ctx, client.B().Info().Section("modules").Build()).ToString()
		if err != nil {
			return fmt.Errorf("search readiness %s: %w", address, err)
		}
		if !strings.Contains(info, "module:name=search,") {
			return fmt.Errorf("search module missing on %s", address)
		}
		if err := validateSearchWriterConfig(info); err != nil {
			return fmt.Errorf("search node %s: %w", address, err)
		}
		if s.client.Mode() == rueidis.ClientModeCluster {
			if err := validateSearchClusterConfig(info); err != nil {
				return fmt.Errorf("search node %s: %w", address, err)
			}
		}
		if err := validateSearchForkConfig(ctx, client, info); err != nil {
			return fmt.Errorf("search node %s: %w", address, err)
		}
	}
	return nil
}

func validateSearchForkConfig(ctx context.Context, client rueidis.Client, info string) error {
	if searchModuleVersion(info) != "66049" {
		return nil
	}
	// Search 1.2.1 can block the main thread draining transactional mutations
	// while its writer pool waits for a fork-child callback on that same thread.
	// This also affects standalone nodes with automatic RDB/AOF persistence.
	// https://github.com/valkey-io/valkey-search/issues/1260
	const setting = "search.max-worker-suspension-secs"
	values, err := client.Do(ctx, client.B().Arbitrary("CONFIG", "GET").Args(setting).ReadOnly()).AsMap()
	if err != nil {
		return fmt.Errorf("read search 1.2.1 fork configuration: %w", err)
	}
	value := values[setting]
	suspension, err := value.ToString()
	if err != nil || len(values) != 1 || suspension != "0" {
		return fmt.Errorf("search 1.2.1 requires %s=0 to avoid a fork deadlock; "+
			"configure it on every primary and replica (upstream #1260)", setting)
	}
	return nil
}

func validateSearchClusterConfig(info string) error {
	fields := infoFields(info)
	port, err := strconv.Atoi(fields["search_coordinator_server_listening_port"])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("search coordinator missing")
	}
	return nil
}

func validateSearchWriterConfig(info string) error {
	fields := infoFields(info)
	// Search 1.2.1 strands transactional mutations below writer-threads. The
	// coordinator can read those shards without the local search queue barrier.
	// Reject that confirmed unsafe configuration without changing server settings.
	// https://github.com/valkey-io/valkey-search/issues/1651
	if searchModuleVersion(info) == "66049" && fields["search_total_active_write_threads"] != "1" {
		return fmt.Errorf("search 1.2.1 requires one active writer thread for transactional indexing; " +
			"configure search.writer-threads=1 on every primary and replica (upstream #1651)")
	}
	return nil
}

func searchModuleVersion(info string) string {
	for line := range strings.SplitSeq(info, "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if parts[0] != "module:name=search" {
			continue
		}
		for _, part := range parts[1:] {
			if version, ok := strings.CutPrefix(part, "ver="); ok {
				return version
			}
		}
	}
	return ""
}

func (s *Store) searchClients(ctx context.Context) (map[string]rueidis.Client, error) {
	if s.client.Mode() != rueidis.ClientModeCluster {
		return map[string]rueidis.Client{"standalone": s.client}, nil
	}
	raw, err := s.client.Do(ctx, s.b().ClusterInfo().Build()).ToString()
	if err != nil {
		return nil, fmt.Errorf("read cluster health: %w", err)
	}
	info := infoFields(raw)
	if info["cluster_state"] != "ok" || info["cluster_slots_assigned"] != "16384" ||
		info["cluster_slots_ok"] != "16384" {
		return nil, fmt.Errorf("cluster slot coverage is not ready")
	}
	slots, err := s.client.Do(ctx, s.b().ClusterSlots().Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("read cluster slots: %w", err)
	}
	nodes := s.client.Nodes()
	participants := make(map[string]rueidis.Client)
	for _, slot := range slots {
		row, err := slot.ToArray()
		if err != nil || len(row) < 3 {
			return nil, fmt.Errorf("invalid cluster slots reply")
		}
		for _, descriptor := range row[2:] {
			address, err := searchNodeAddress(descriptor)
			if err != nil {
				return nil, err
			}
			node, ok := nodes[address]
			if !ok {
				return nil, fmt.Errorf("search node %s is not yet in client topology", address)
			}
			participants[address] = node
		}
	}
	if len(participants) == 0 {
		return nil, fmt.Errorf("cluster has no search participants")
	}
	return participants, nil
}

func infoFields(raw string) map[string]string {
	fields := make(map[string]string)
	for line := range strings.SplitSeq(raw, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			fields[key] = value
		}
	}
	return fields
}

// WaitForIndex gates collection publication on schema convergence and completed
// backfill on every current Search participant. Failed attempts leave the lifecycle job.
func (s *Store) WaitForIndex(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if last = s.indexReady(ctx, name); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("index is not ready: %w: %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Store) indexReady(ctx context.Context, name string) error {
	participants, err := s.searchClients(ctx)
	if err != nil {
		return err
	}
	// Search 1.2.1 aborts in PerformSlotConsistencyCheck for FT.INFO
	// PRIMARY CONSISTENT. Probe every participating node locally instead; searches
	// independently enforce ALLSHARDS CONSISTENT at query time.
	for address, client := range participants {
		command := client.B().Arbitrary("FT.INFO").Args(name, "LOCAL").Build()
		fields, err := client.Do(ctx, command).AsMap()
		if err != nil {
			return fmt.Errorf("index on %s: %w", address, err)
		}
		for _, field := range []string{"backfill_in_progress", "mutation_queue_size", "hash_indexing_failures"} {
			message, ok := fields[field]
			if !ok {
				return fmt.Errorf("index on %s has no %s", address, field)
			}
			value, err := message.ToString()
			if err != nil || value != "0" {
				return fmt.Errorf("index on %s: %s is not zero", address, field)
			}
		}
		state, ok := fields["state"]
		if !ok {
			return fmt.Errorf("index on %s has no readiness state", address)
		}
		value, err := state.ToString()
		if err != nil || value != "ready" {
			return fmt.Errorf("index on %s is not ready", address)
		}
	}
	return nil
}

func searchNodeAddress(message rueidis.RedisMessage) (string, error) {
	primary, err := message.ToArray()
	if err != nil || len(primary) < 2 {
		return "", fmt.Errorf("invalid cluster primary reply")
	}
	host, err := primary[0].ToString()
	if err != nil || host == "" {
		return "", fmt.Errorf("cluster primary has no advertised address")
	}
	port, err := primary[1].AsInt64()
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("cluster primary has invalid port")
	}
	return net.JoinHostPort(host, strconv.FormatInt(port, 10)), nil
}
