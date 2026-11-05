package vecdex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain/storage"
	"github.com/redis/rueidis"
)

type topologyRecoveryPhase struct {
	Name          string   `json:"name"`
	Command       []string `json:"command"`
	Output        string   `json:"output"`
	Error         string   `json:"error,omitempty"`
	ElapsedMillis int64    `json:"elapsed_millis"`
}

type topologyRecoveryEvidence struct {
	Container        string                  `json:"container"`
	Collection       string                  `json:"collection"`
	Versions         map[string]string       `json:"original_versions"`
	Phases           []topologyRecoveryPhase `json:"phases"`
	RecoveredSources int                     `json:"recovered_sources"`
	ConditionalWrite map[string]any          `json:"post_recovery_conditional_write"`
	Complete         bool                    `json:"complete"`
}

func TestClusterInterruptedTopologyRecoveryIntegration(t *testing.T) {
	container, seeds := os.Getenv("VECDEX_E2E_CLUSTER_CONTAINER"), os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS")
	if testing.Short() || container == "" || seeds == "" {
		t.Skip("fresh disposable three-primary Search fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	client, err := newIntegrationClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := fmt.Sprintf("sdk_topology_recovery_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, name, WithField("scope", FieldTag)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if err := client.Collections().Delete(cleanup, name); err != nil {
			t.Error(err)
		}
	}()
	col, err := client.collSvc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(name, col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	want := seedTopologyDocuments(t, ctx, client, name)
	evidence := topologyRecoveryEvidence{Container: container, Collection: name,
		Versions: make(map[string]string, len(want))}
	defer saveTopologyRecoveryEvidence(t, &evidence)
	verifyRecoveredSources(t, ctx, client, name, want, evidence.Versions)
	assertTopologySearch(t, ctx, client, name, want)
	id, key := recoverySourceOnPrimary(t, ctx, seeds, layout, want, 17389)
	stage := recoveryTopologyStep(t, ctx, &evidence, "interrupted-expand", "stage-expand", layout.Index(), key)
	assertStagedPartition(t, stage)
	assertMovedSource(t, ctx, client, name, id, evidence.Versions[id])
	recoveryTopologyStep(t, ctx, &evidence, "resume-expand", "expand", layout.Index())
	verifyReconnectedTopology(t, ctx, client, name, want, layout.Index(), evidence.Versions)
	recoveryTopologyStep(t, ctx, &evidence, "repeat-completed-expand", "expand", layout.Index())
	verifyReconnectedTopology(t, ctx, client, name, want, layout.Index(), evidence.Versions)
	id, key = recoverySourceOnPrimary(t, ctx, seeds, layout, want, 17395)
	stage = recoveryTopologyStep(t, ctx, &evidence, "interrupted-contract", "stage-contract", key)
	assertStagedPartition(t, stage)
	assertMovedSource(t, ctx, client, name, id, evidence.Versions[id])
	recoveryTopologyStep(t, ctx, &evidence, "resume-contract", "contract")
	verifyReconnectedTopology(t, ctx, client, name, want, layout.Index(), evidence.Versions)
	recoveryTopologyStep(t, ctx, &evidence, "repeat-completed-contract", "contract")
	verifyReconnectedTopology(t, ctx, client, name, want, layout.Index(), evidence.Versions)
	recoveryTopologyStep(t, ctx, &evidence, "automatic-failover-after-recovery", "failover")
	verifyReconnectedTopology(t, ctx, client, name, want, layout.Index(), evidence.Versions)
	evidence.ConditionalWrite = verifyRecoveryConditionalWrite(t, ctx, client, name, want[0], evidence.Versions[want[0]])
	evidence.RecoveredSources, evidence.Complete = len(want), true
}

func verifyRecoveryConditionalWrite(
	t *testing.T, ctx context.Context, client *Client, name, id, version string,
) map[string]any {
	t.Helper()
	doc := Document{ID: id, Content: "SciFact immutable source " + id,
		Metadata: map[string]any{"scope": "stable", "after_recovery": "checked"}}
	written, err := client.Documents(name).Upsert(ctx, doc, IfVersion(version))
	if err != nil || written.Created || written.Version == "" || written.Version == version {
		t.Fatalf("original CAS token after recovery: %+v %v", written, err)
	}
	if _, err := client.Documents(name).Upsert(ctx, doc, IfVersion(version)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("old CAS token remained reusable: %v", err)
	}
	stored, err := client.Documents(name).Get(ctx, id)
	if err != nil || stored.Version != written.Version || stored.Metadata["after_recovery"] != "checked" {
		t.Fatalf("conditional commit after recovery: %+v %v", stored, err)
	}
	return map[string]any{"id": id, "old_version": version, "new_version": written.Version, "stale_version_rejected": true}
}

func recoveryTopologyStep(
	t *testing.T, ctx context.Context, evidence *topologyRecoveryEvidence, name string, args ...string,
) string {
	t.Helper()
	command := append([]string{"exec", evidence.Container, "/bin/sh", "/tests/topology.sh"}, args...)
	started := time.Now()
	output, err := exec.CommandContext(ctx, "docker", command...).CombinedOutput()
	phase := topologyRecoveryPhase{Name: name, Command: command, Output: string(output),
		ElapsedMillis: time.Since(started).Milliseconds()}
	if err != nil {
		phase.Error = err.Error()
	}
	evidence.Phases = append(evidence.Phases, phase)
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, output)
	}
	t.Logf("%s: %s", name, time.Since(started).Round(time.Millisecond))
	return string(output)
}

func assertStagedPartition(t *testing.T, output string) {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if !strings.HasPrefix(line, "STAGED ") {
			continue
		}
		fields := make(map[string]string)
		for _, part := range strings.Fields(line) {
			if key, value, ok := strings.Cut(part, "="); ok {
				fields[key] = value
			}
		}
		remaining, err := strconv.Atoi(fields["remaining"])
		if err != nil || remaining <= 0 || fields["imported"] != "1" {
			t.Fatalf("interruption did not split a real document partition: %s", line)
		}
		return
	}
	t.Fatal("script did not prove a partial slot transfer")
}

func assertMovedSource(t *testing.T, ctx context.Context, client *Client, name, id, version string) {
	t.Helper()
	doc, err := client.Documents(name).Get(ctx, id)
	if err != nil || doc.Content != "SciFact immutable source "+id || doc.Version != version {
		t.Fatalf("ASK read of moved source: %+v %v", doc, err)
	}
}

func verifyRecoveredSources(
	t *testing.T, ctx context.Context, client *Client, name string, ids []string, versions map[string]string,
) {
	t.Helper()
	for _, id := range ids {
		doc, err := client.Documents(name).Get(ctx, id)
		if err != nil || doc.Content != "SciFact immutable source "+id ||
			doc.Metadata["scope"] != "stable" || doc.Version == "" {
			t.Fatalf("recovered source %s: %+v %v", id, doc, err)
		}
		if previous, ok := versions[id]; ok && previous != doc.Version {
			t.Fatalf("source version changed during migration: %s", id)
		}
		versions[id] = doc.Version
	}
}

func verifyReconnectedTopology(
	t *testing.T, ctx context.Context, client *Client, name string, want []string, index string,
	versions map[string]string,
) {
	t.Helper()
	if err := client.store.WaitForIndex(ctx, index); err != nil {
		t.Fatal(err)
	}
	verifyRecoveredSources(t, ctx, client, name, want, versions)
	assertTopologySearch(t, ctx, client, name, want)
	reconnected, err := newIntegrationClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	verifyRecoveredSources(t, ctx, reconnected, name, want, versions)
	assertTopologySearch(t, ctx, reconnected, name, want)
}

func recoverySourceOnPrimary(
	t *testing.T, ctx context.Context, seeds string, layout storage.Collection, ids []string, port int64,
) (id, key string) {
	t.Helper()
	admin, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: strings.Split(seeds, ","),
		DisableCache: true, AlwaysRESP2: true})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	rows, err := admin.Do(ctx, admin.B().ClusterSlots().Build()).ToArray()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		parts, err := row.ToArray()
		if err != nil || len(parts) < 3 {
			t.Fatal("invalid slot range")
		}
		descriptor, err := parts[2].ToArray()
		if err != nil || len(descriptor) < 2 {
			t.Fatal("invalid primary descriptor")
		}
		owner, err := descriptor[1].AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		if owner != port {
			continue
		}
		first, err := parts[0].AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		last, err := parts[1].AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			key := layout.Document(id)
			command := admin.B().Get().Key(key).Build()
			slot := int64(command.Slot())
			if slot >= first && slot <= last {
				return id, key
			}
		}
	}
	t.Fatalf("no immutable source owned by fixture primary %d", port)
	return "", ""
}

func saveTopologyRecoveryEvidence(t *testing.T, evidence *topologyRecoveryEvidence) {
	t.Helper()
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Error(err)
		return
	}
	path := filepath.Join("..", "..", "tests", "reports")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(filepath.Join(path, "interrupted-topology.json"), append(encoded, '\n'), 0o600); err != nil {
		t.Error(err)
	}
}
