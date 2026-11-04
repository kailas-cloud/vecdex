package valkey

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/rueidis"
)

const maxTransactionAttempts = 128

// execTransaction queues native commands on a connection reserved by WATCH.
// A nil EXEC is a known conflict; network errors have an ambiguous outcome and
// must never cause an automatic replay of the write.
func execTransaction(
	ctx context.Context,
	client rueidis.DedicatedClient,
	commands []rueidis.Completed,
) ([]rueidis.RedisMessage, bool, error) {
	batch := make([]rueidis.Completed, 0, len(commands)+2)
	batch = append(batch, client.B().Multi().Build())
	batch = append(batch, commands...)
	batch = append(batch, client.B().Exec().Build())
	replies := client.DoMulti(ctx, batch...)
	if len(replies) != len(batch) {
		return nil, false, fmt.Errorf("incomplete transaction response")
	}
	for _, reply := range replies[:len(replies)-1] {
		if err := reply.Error(); err != nil {
			return nil, false, fmt.Errorf("queue transaction: %w", err)
		}
	}
	result, err := replies[len(replies)-1].ToArray()
	if rueidis.IsRedisNil(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("execute transaction: %w", err)
	}
	for _, reply := range result {
		if err := reply.Error(); err != nil {
			return nil, false, fmt.Errorf("transaction command: %w", err)
		}
	}
	return result, true, nil
}

func watchKeys(ctx context.Context, client rueidis.DedicatedClient, keys ...string) error {
	if err := client.Do(ctx, client.B().Watch().Key(keys...).Build()).Error(); err != nil {
		return fmt.Errorf("watch keys: %w", err)
	}
	return nil
}

// unwatch is also a response barrier: a successful reply proves that earlier
// commands on this connection have been processed. A broken connection cannot
// prove the outcome of EXEC, so prepared data must be retained in that case.
func unwatch(client rueidis.DedicatedClient) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := client.Do(ctx, client.B().Unwatch().Build()).ToString()
	if err != nil {
		client.Close()
		return false
	}
	if status == "QUEUED" {
		if err := client.Do(ctx, client.B().Discard().Build()).Error(); err != nil {
			client.Close()
			return false
		}
	}
	return true
}

func transactionRetry(ctx context.Context, attempt int) error {
	// Contenders back off in Go; the database executes no spin loop.
	timer := time.NewTimer(time.Duration(min(attempt+1, 10)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("transaction canceled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
