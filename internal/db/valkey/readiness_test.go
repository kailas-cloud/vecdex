package valkey

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"
)

func TestReadyRejectsIncompleteSlotCoverage(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().Mode().Return(rueidis.ClientModeCluster)
	client.EXPECT().Do(gomock.Any(), mock.Match("CLUSTER", "INFO")).Return(mock.Result(
		mock.RedisString("cluster_state:ok\r\ncluster_slots_assigned:16384\r\ncluster_slots_ok:12000\r\n")))
	if err := NewStoreForTest(client).Ready(context.Background()); err == nil {
		t.Fatal("accepted uncovered slots")
	}
}

func TestReadyRequiresEveryPrimaryAndCoordinator(t *testing.T) {
	for _, failure := range []string{"unreachable-primary", "missing-coordinator", "zero-coordinator", "healthy"} {
		t.Run(failure, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mock.NewClient(ctrl)
			first, second := mock.NewClient(ctrl), mock.NewClient(ctrl)
			client.EXPECT().Mode().Return(rueidis.ClientModeCluster).AnyTimes()
			client.EXPECT().Do(gomock.Any(), mock.Match("CLUSTER", "INFO")).Return(mock.Result(
				mock.RedisString("cluster_state:ok\r\ncluster_slots_assigned:16384\r\ncluster_slots_ok:16384\r\n")))
			client.EXPECT().Do(gomock.Any(), mock.Match("CLUSTER", "SLOTS")).Return(mock.Result(mock.RedisArray(
				mock.RedisArray(
					mock.RedisInt64(0),
					mock.RedisInt64(8191),
					mock.RedisArray(mock.RedisString("127.0.0.1"), mock.RedisInt64(7000)),
				),
				mock.RedisArray(
					mock.RedisInt64(8192),
					mock.RedisInt64(16383),
					mock.RedisArray(mock.RedisString("127.0.0.1"), mock.RedisInt64(7001)),
				),
			)))
			client.EXPECT().Nodes().Return(map[string]rueidis.Client{"127.0.0.1:7000": first, "127.0.0.1:7001": second})
			modules := "module:name=search,ver=66049\r\nsearch_coordinator_server_listening_port:17000\r\n" +
				"search_total_active_write_threads:1\r\n"
			first.EXPECT().Do(gomock.Any(), mock.Match("PING")).Return(mock.Result(mock.RedisString("PONG"))).AnyTimes()
			first.EXPECT().
				Do(gomock.Any(), mock.Match("INFO", "modules")).
				Return(mock.Result(mock.RedisString(modules))).
				AnyTimes()
			forkConfig := mock.Result(mock.RedisArray(
				mock.RedisString("search.max-worker-suspension-secs"), mock.RedisString("0")))
			first.EXPECT().Do(gomock.Any(), mock.Match("CONFIG", "GET", "search.max-worker-suspension-secs")).
				Return(forkConfig).AnyTimes()
			ping := mock.Result(mock.RedisString("PONG"))
			if failure == "unreachable-primary" {
				ping = mock.Result(mock.RedisError("LOADING restart in progress"))
			}
			second.EXPECT().Do(gomock.Any(), mock.Match("PING")).Return(ping)
			if failure != "unreachable-primary" {
				if failure == "missing-coordinator" {
					modules = "module:name=search,ver=66049\r\n"
				}
				if failure == "zero-coordinator" {
					modules = "module:name=search,ver=66049\r\nsearch_coordinator_server_listening_port:0\r\n"
				}
				second.EXPECT().
					Do(gomock.Any(), mock.Match("INFO", "modules")).
					Return(mock.Result(mock.RedisString(modules)))
				if failure == "healthy" {
					second.EXPECT().Do(gomock.Any(), mock.Match("CONFIG", "GET", "search.max-worker-suspension-secs")).
						Return(forkConfig)
				}
			}
			err := NewStoreForTest(client).Ready(context.Background())
			if (err == nil) != (failure == "healthy") {
				t.Fatalf("readiness err=%v", err)
			}
		})
	}
}

func TestSearch121ReadinessRejectsForkSuspensionOnStandalone(t *testing.T) {
	for _, suspension := range []string{"0", "60", "1", ""} {
		t.Run("suspension="+suspension, func(t *testing.T) {
			client := mock.NewClient(gomock.NewController(t))
			client.EXPECT().Mode().Return(rueidis.ClientModeStandalone).AnyTimes()
			client.EXPECT().Do(gomock.Any(), mock.Match("PING")).Return(mock.Result(mock.RedisString("PONG")))
			client.EXPECT().Do(gomock.Any(), mock.Match("INFO", "modules")).
				Return(mock.Result(mock.RedisString("module:name=search,ver=66049,api=1\r\n" +
					"search_total_active_write_threads:1\r\n")))
			client.EXPECT().Do(gomock.Any(), mock.Match("CONFIG", "GET", "search.max-worker-suspension-secs")).
				Return(mock.Result(mock.RedisArray(
					mock.RedisString("search.max-worker-suspension-secs"), mock.RedisString(suspension))))
			err := NewStoreForTest(client).Ready(context.Background())
			if suspension == "0" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "upstream #1260") {
				t.Fatalf("fork-unsafe configuration accepted or reason lost: %v", err)
			}
		})
	}
}

func TestIndexReadinessRejectsBackfillAndPendingMutations(t *testing.T) {
	for _, state := range []string{"ready", "backfill", "pending", "failed-indexing"} {
		t.Run(state, func(t *testing.T) {
			client := mock.NewClient(gomock.NewController(t))
			client.EXPECT().Mode().Return(rueidis.ClientModeStandalone).AnyTimes()
			backfill, pending, failures := "0", "0", "0"
			if state == "backfill" {
				backfill = "1"
			}
			if state == "pending" {
				pending = "1"
			}
			if state == "failed-indexing" {
				failures = "1"
			}
			client.EXPECT().
				Do(gomock.Any(), mock.Match("FT.INFO", "index", "LOCAL")).
				Return(mock.Result(mock.RedisArray(
					mock.RedisString("state"), mock.RedisString("ready"),
					mock.RedisString("backfill_in_progress"), mock.RedisString(backfill),
					mock.RedisString("mutation_queue_size"), mock.RedisString(pending),
					mock.RedisString("hash_indexing_failures"), mock.RedisString(failures),
				)))
			err := NewStoreForTest(client).indexReady(context.Background(), "index")
			if (err == nil) != (state == "ready") {
				t.Fatalf("state=%s err=%v", state, err)
			}
		})
	}
}

func TestIndexReadinessIncludesReplicaParticipants(t *testing.T) {
	ctrl := gomock.NewController(t)
	client, primary, replica := mock.NewClient(ctrl), mock.NewClient(ctrl), mock.NewClient(ctrl)
	client.EXPECT().Mode().Return(rueidis.ClientModeCluster).AnyTimes()
	client.EXPECT().Do(gomock.Any(), mock.Match("CLUSTER", "INFO")).Return(mock.Result(
		mock.RedisString("cluster_state:ok\r\ncluster_slots_assigned:16384\r\ncluster_slots_ok:16384\r\n")))
	client.EXPECT().Do(gomock.Any(), mock.Match("CLUSTER", "SLOTS")).Return(mock.Result(mock.RedisArray(mock.RedisArray(
		mock.RedisInt64(0), mock.RedisInt64(16383),
		mock.RedisArray(mock.RedisString("127.0.0.1"), mock.RedisInt64(7000)),
		mock.RedisArray(mock.RedisString("127.0.0.1"), mock.RedisInt64(7001)),
	))))
	client.EXPECT().Nodes().Return(map[string]rueidis.Client{"127.0.0.1:7000": primary, "127.0.0.1:7001": replica})
	ready := mock.Result(mock.RedisArray(
		mock.RedisString("backfill_in_progress"), mock.RedisString("0"),
		mock.RedisString("mutation_queue_size"), mock.RedisString("0"),
		mock.RedisString("hash_indexing_failures"), mock.RedisString("0"),
		mock.RedisString("state"), mock.RedisString("ready"),
	))
	primary.EXPECT().Do(gomock.Any(), mock.Match("FT.INFO", "index", "LOCAL")).Return(ready).AnyTimes()
	replica.EXPECT().Do(gomock.Any(), mock.Match("FT.INFO", "index", "LOCAL")).Return(mock.Result(mock.RedisArray(
		mock.RedisString("backfill_in_progress"), mock.RedisString("1"),
		mock.RedisString("mutation_queue_size"), mock.RedisString("0"),
		mock.RedisString("hash_indexing_failures"), mock.RedisString("0"),
		mock.RedisString("state"), mock.RedisString("backfill_in_progress"),
	)))
	if err := NewStoreForTest(client).indexReady(context.Background(), "index"); err == nil {
		t.Fatal("accepted an unready replica that the coordinator can select")
	}
}

func TestSearch121RequiresOneWriter(t *testing.T) {
	for _, writers := range []string{"1", "2", "0", ""} {
		t.Run("writers="+writers, func(t *testing.T) {
			info := "module:name=search,ver=66049,api=1\r\n" +
				"search_coordinator_server_listening_port:17000\r\n" +
				"search_total_active_write_threads:" + writers + "\r\n"
			err := validateSearchWriterConfig(info)
			if (err == nil) != (writers == "1") {
				t.Fatalf("writers=%q err=%v", writers, err)
			}
		})
	}
}

func TestReadinessTimeoutPreservesConfigurationError(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().Mode().Return(rueidis.ClientModeStandalone).AnyTimes()
	client.EXPECT().Do(gomock.Any(), mock.Match("PING")).Return(mock.Result(mock.RedisString("PONG"))).AnyTimes()
	client.EXPECT().Do(gomock.Any(), mock.Match("INFO", "modules")).
		Return(mock.Result(mock.RedisString(""))).AnyTimes()
	err := NewStoreForTest(client).WaitForReady(context.Background(), 150*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "search module missing") {
		t.Fatalf("readiness discarded the actionable failure: %v", err)
	}
}
