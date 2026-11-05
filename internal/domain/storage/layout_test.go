package storage

import (
	"fmt"
	"strings"
	"testing"
)

func TestCollectionTransactionsStayInOneSlot(t *testing.T) {
	keys, err := NewCollection("notes", "generation-one")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1000 {
		id := fmt.Sprintf("document-%d", i)
		p := Partition(id)
		group := []string{
			keys.Document(id), keys.Staging(id, "operation"), keys.Fragment(id, "operation", 0),
			keys.Guard(p), keys.Catalog(p), keys.WorkQueue(p), keys.Operation(id, "operation"),
			keys.Request(id, "caller-key"),
			keys.Deadlines(p),
			keys.ExpiryMarker(id),
		}
		for _, key := range group {
			if slot(key) != slot(group[0]) {
				t.Fatalf("cross-slot document transaction: %v", group)
			}
		}
	}
}

func TestSingleCollectionDistributesAcrossPrimaries(t *testing.T) {
	keys, err := NewCollection("large-collection", "generation-one")
	if err != nil {
		t.Fatal(err)
	}
	nodes := [3]int{}
	slots := map[uint16]bool{}
	for i := range 10000 {
		key := keys.Document(fmt.Sprintf("document-%d", i))
		s := slot(key)
		slots[s] = true
		nodes[int(s)*len(nodes)/16384]++
	}
	if len(slots) < 200 {
		t.Fatalf("collection occupies only %d slots", len(slots))
	}
	for node, count := range nodes {
		if count < 2000 || count > 4500 {
			t.Fatalf("node %d has %d/10000 documents: %v", node, count, nodes)
		}
	}
}

func TestGenerationAndSearchNamespace(t *testing.T) {
	old, _ := NewCollection("notes", "old")
	next, _ := NewCollection("notes", "next")
	if old.Document("a") == next.Document("a") || old.Guard(0) == next.Guard(0) ||
		old.Request("a", "key") == next.Request("a", "key") {
		t.Fatal("generation was reused")
	}
	if strings.Contains(old.Index(), "{") || strings.Contains(old.ChunkPrefix(), "{") {
		t.Fatal("logical search was pinned to a slot")
	}
	for p := range PartitionCount {
		// Find an ID in each partition to check the shared index prefix.
		for i := 0; ; i++ {
			id := fmt.Sprintf("id-%d", i)
			if Partition(id) == p {
				if !strings.HasPrefix(old.Fragment(id, "op", 0), old.ChunkPrefix()) {
					t.Fatal("fragment outside index")
				}
				break
			}
		}
	}
	for _, name := range []string{"", "notes{all}", "notes:other"} {
		if _, err := NewCollection(name, "generation"); err == nil {
			t.Fatalf("accepted unsafe name %q", name)
		}
	}
	if _, err := NewCollection("notes", ""); err == nil {
		t.Fatal("accepted absent generation")
	}
}

func TestControlCatalogCoLocation(t *testing.T) {
	for i := range 100 {
		name := fmt.Sprintf("collection-%d", i)
		base := ControlBase(name)
		matched := false
		for p := range ControlPartitionCount {
			if base == ControlBucket(p) {
				matched = true
				if slot(Metadata(name)) != slot(CollectionCatalog(p)) || slot(Metadata(name)) != slot(LifecycleQueue(p)) {
					t.Fatal("metadata and lifecycle/catalog are cross-slot")
				}
			}
		}
		if !matched {
			t.Fatal("unknown control bucket")
		}
	}
}

// slot implements the published CRC16/XMODEM cluster hash, independently of key helpers.
func slot(key string) uint16 {
	if start := strings.IndexByte(key, '{'); start >= 0 {
		if end := strings.IndexByte(key[start+1:], '}'); end > 0 {
			key = key[start+1 : start+1+end]
		}
	}
	var crc uint16
	for _, b := range []byte(key) {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc % 16384
}
