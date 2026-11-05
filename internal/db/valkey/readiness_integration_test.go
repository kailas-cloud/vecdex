package valkey

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"
)

// The fixture opt-in permits short-lived CONFIG changes on its disposable nodes.
// No application or distributed search runs while the settings are changed.
func TestSearch121ParticipantConfigurationReadinessLive(t *testing.T) {
	seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS")
	if testing.Short() || seeds == "" || os.Getenv("VECDEX_E2E_CLUSTER_CONTAINER") == "" {
		t.Skip("explicit disposable Search cluster opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewStore(Config{Addrs: strings.Split(seeds, ",")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	participants, err := store.searchClients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	addresses := make([]string, 0, len(participants))
	for address := range participants {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			node := participants[address]
			for _, setting := range []struct{ name, value, issue string }{
				{"search.writer-threads", "2", "#1651"},
				{"search.max-worker-suspension-secs", "60", "#1260"},
			} {
				t.Run(setting.name, func(t *testing.T) {
					proveReadinessConfigurationRejection(t, ctx, store, node, address, setting.name, setting.value, setting.issue)
				})
			}
		})
	}
}

func proveReadinessConfigurationRejection(
	t *testing.T, ctx context.Context, store *Store, node rueidis.Client, address, name, value, issue string,
) {
	t.Helper()
	previous, err := node.Do(ctx, node.B().Arbitrary("CONFIG", "GET").Args(name).Build()).AsStrMap()
	if err != nil || previous[name] == "" {
		t.Fatalf("read original configuration: %v %v", previous, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		command := node.B().Arbitrary("CONFIG", "SET").Args(name, previous[name]).Build()
		if err := node.Do(cleanup, command).Error(); err != nil {
			t.Error(err)
		}
	}()
	if err := node.Do(ctx, node.B().Arbitrary("CONFIG", "SET").Args(name, value).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	err = store.Ready(ctx)
	if err == nil || !strings.Contains(err.Error(), address) || !strings.Contains(err.Error(), issue) {
		t.Fatalf("configuration on %s was not rejected with node and upstream issue: %v", address, err)
	}
	// Readiness must diagnose the fault without silently rewriting the server.
	actual, err := node.Do(ctx, node.B().Arbitrary("CONFIG", "GET").Args(name).Build()).AsStrMap()
	if err != nil || actual[name] != value {
		t.Fatalf("readiness changed server configuration: %v %v", actual, err)
	}
}
