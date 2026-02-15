package valkey

import "github.com/redis/rueidis"

// NewStoreForTest creates a Store with the provided rueidis client (test-only).
func NewStoreForTest(c rueidis.Client) *Store {
	return &Store{client: c}
}
