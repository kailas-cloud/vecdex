package memorycontext

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/rueidis"
)

// Only nil EXEC is retried. A lost reply or a command error has an uncertain
// outcome; the caller must retry with the SAME idempotency key to inspect receipt.
func execute(ctx context.Context, client rueidis.DedicatedClient, commands []rueidis.Completed) (bool, error) {
	batch := make([]rueidis.Completed, 0, len(commands)+2)
	batch = append(batch, client.B().Multi().Build())
	batch = append(batch, commands...)
	batch = append(batch, client.B().Exec().Build())
	replies := client.DoMulti(ctx, batch...)
	if len(replies) != len(batch) {
		return false, fmt.Errorf("incomplete context transaction response")
	}
	values, err := replies[len(replies)-1].ToArray()
	if rueidis.IsRedisNil(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("execute context transaction: %w", err)
	}
	for _, reply := range replies[:len(replies)-1] {
		if err := reply.Error(); err != nil {
			return false, fmt.Errorf("queue context transaction: %w", err)
		}
	}
	if len(values) != len(commands) {
		return false, fmt.Errorf("incomplete context transaction results")
	}
	for _, value := range values {
		if err := value.Error(); err != nil {
			return false, fmt.Errorf("context transaction command: %w", err)
		}
	}
	return true, nil
}

func releaseWatch(client rueidis.DedicatedClient) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := client.Do(ctx, client.B().Unwatch().Build()).ToString()
	if err != nil {
		client.Close()
		return
	}
	if status == "QUEUED" {
		if err := client.Do(ctx, client.B().Discard().Build()).Error(); err != nil {
			client.Close()
		}
	}
}

func backoff(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(min(attempt+1, 10)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("context transaction cancelled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
