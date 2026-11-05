// Package storage defines the v3 storage namespace independently of cluster topology.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	// PartitionCount is immutable for the v3 layout and independent of shard count.
	PartitionCount = 256
	// ControlPartitionCount distributes collection metadata and lifecycle work.
	ControlPartitionCount = 16
	prefix                = "vecdex:v3:"
)

// Collection identifies exactly one incarnation of a collection.
// Construct it with NewCollection so arbitrary braces cannot change hash slots.
type Collection struct {
	base string
	tag  string
}

// NewCollection binds keys to an immutable collection generation.
func NewCollection(name, generation string) (Collection, error) {
	if !validComponent(name, 64) || !validComponent(generation, 256) {
		return Collection{}, fmt.Errorf("invalid collection name or generation")
	}
	base := prefix + "c:" + name + ":g:" + generation
	digest := sha256.Sum256([]byte(base))
	return Collection{base: base, tag: hex.EncodeToString(digest[:16])}, nil
}

// Partition selects the virtual partition for a source ID.
func Partition(id string) int {
	digest := sha256.Sum256([]byte(id))
	return int(digest[0])
}

// Base is the immutable generation namespace, without a hash tag.
func (c Collection) Base() string { return c.base }

// Index is a logical Search index name, not a Valkey data key.
func (c Collection) Index() string { return c.base + ":index" }

// ChunkPrefix spans all partitions so a single distributed index sees every fragment.
func (c Collection) ChunkPrefix() string { return c.base + ":chunk:" }

// Document is the public source document's parent hash.
func (c Collection) Document(id string) string {
	return c.base + ":doc:" + c.partitionTag(Partition(id)) + ":" + id
}

// ExpiryMarker lets native WATCH fence a write if its source expires during preparation.
// Only expiring sources allocate this key; the parent manifest stays durable for cleanup.
func (c Collection) ExpiryMarker(id string) string {
	return c.partitionKey(Partition(id), "expiry-marker") + ":" + id
}

// Staging identifies one immutable prepared parent version.
func (c Collection) Staging(id, operation string) string {
	return c.base + ":pending:" + c.partitionTag(Partition(id)) + ":" + id + ":" + operation
}

// Fragment identifies one private search fragment of a source version.
func (c Collection) Fragment(id, operation string, ordinal int) string {
	return c.ChunkPrefix() + c.partitionTag(Partition(id)) + ":" + id + ":" + operation + ":" + strconv.Itoa(ordinal)
}

// Guard is the partition-local lifecycle fence checked by document transactions.
func (c Collection) Guard(partition int) string { return c.partitionKey(partition, "guard") }

// Catalog stores only source IDs in one partition.
func (c Collection) Catalog(partition int) string { return c.partitionKey(partition, "ids") }

// Deadlines contains every source ID, scored by its expiry or +inf when persistent.
// It provides immediate live counts and bounded due-work discovery without SCAN.
func (c Collection) Deadlines(partition int) string { return c.partitionKey(partition, "deadlines") }

// WorkQueue registers durable preparation, cleanup and expiry work.
func (c Collection) WorkQueue(partition int) string { return c.partitionKey(partition, "work") }

// Operation identifies a durable intent and its eventual commit receipt.
func (c Collection) Operation(id, operation string) string {
	return c.partitionKey(Partition(id), "operation") + ":" + operation
}

// Request binds an external key to one source in this collection generation.
// Hashing keeps caller-provided key text out of physical names and hash tags.
func (c Collection) Request(id, key string) string {
	digest := sha256.Sum256([]byte(id + "\x00" + key))
	return c.partitionKey(Partition(id), "request") + ":" + hex.EncodeToString(digest[:])
}

func (c Collection) partitionKey(partition int, kind string) string {
	return c.base + ":" + kind + ":" + c.partitionTag(partition)
}

func (c Collection) partitionTag(partition int) string {
	if partition < 0 || partition >= PartitionCount {
		panic("v3 partition outside layout")
	}
	return "{" + c.tag + ":" + strconv.Itoa(partition) + "}"
}

// ControlBase selects the control bucket for a validated collection name.
func ControlBase(name string) string {
	return ControlBucket(ControlPartition(name))
}

// ControlBucket addresses a stable metadata/lifecycle partition.
func ControlBucket(partition int) string {
	if partition < 0 || partition >= ControlPartitionCount {
		panic("v3 control partition outside layout")
	}
	return prefix + "control:{control:" + strconv.Itoa(partition) + "}"
}

// Metadata is co-located with its collection catalog entry and lifecycle job.
func Metadata(name string) string { return ControlBase(name) + ":collection:" + name }

// CollectionCatalog is the name catalog for one control partition.
func CollectionCatalog(partition int) string { return ControlBucket(partition) + ":collections" }

// LifecycleQueue contains recoverable collection operations in one control partition.
func LifecycleQueue(partition int) string { return ControlBucket(partition) + ":work" }

func validComponent(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return !validNameRune(r)
	}) == -1
}

func validNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

// ControlPartition selects the stable metadata/lifecycle bucket for a collection.
func ControlPartition(name string) int {
	digest := sha256.Sum256([]byte(name))
	return int(digest[0]) % ControlPartitionCount
}
