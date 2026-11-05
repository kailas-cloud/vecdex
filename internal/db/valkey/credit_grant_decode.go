package valkey

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/redis/rueidis"
)

func decodeCreditAllocationPeriod(fields map[string]string) (creditAllocationPeriod, error) {
	var period creditAllocationPeriod
	if len(fields) == 0 {
		return period, nil
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"booked", &period.Booked}, {"limit", &period.Limit},
		{"baseline", &period.Baseline}, {"unknown_baseline", &period.UnknownBaseline},
	} {
		value, err := strconv.ParseInt(fields[field.name], 10, 64)
		if err != nil || value < 0 {
			return period, fmt.Errorf("invalid credit allocation field %s", field.name)
		}
		*field.value = value
	}
	if period.UnknownBaseline > math.MaxInt64-period.Baseline ||
		period.Booked < period.Baseline+period.UnknownBaseline {
		return period, fmt.Errorf("credit allocation is below its history baseline")
	}
	period.present = true
	return period, nil
}

func decodeCreditGrant(fields map[string]string, id string) (db.CreditGrantResult, error) {
	var result db.CreditGrantResult
	if err := json.Unmarshal([]byte(fields["grant"]), &result.Grant); err != nil {
		return result, fmt.Errorf("decode credit grant: %w", err)
	}
	if err := validateStoredCreditGrant(&result.Grant, id); err != nil {
		return result, err
	}
	var err error
	result.Released, err = strconv.ParseInt(fields["released"], 10, 64)
	if err != nil || result.Released < 0 || result.Released > result.Grant.Amount {
		return result, fmt.Errorf("invalid cumulative credit refund")
	}
	result.Closed, err = strconv.ParseBool(fields["closed"])
	if err != nil || (!result.Closed && result.Released != 0) {
		return result, fmt.Errorf("invalid credit grant state")
	}
	if fields["active"] != "" {
		result.Active, err = strconv.ParseBool(fields["active"])
		if err != nil || (result.Closed && result.Active) {
			return result, fmt.Errorf("invalid credit activation state")
		}
	}
	return result, nil
}

func validateStoredCreditGrant(grant *db.CreditGrant, id string) error {
	if grant.ID != id || grant.At <= 0 || grant.ExpiresAt <= grant.At || grant.Amount <= 0 ||
		len(grant.Slices) == 0 || len(grant.Slices) > 8 {
		return fmt.Errorf("invalid stored credit grant")
	}
	seen, total := make(map[int]bool, len(grant.Slices)), int64(0)
	for _, slice := range grant.Slices {
		if slice.Partition < 0 || slice.Partition > 255 || slice.Amount <= 0 ||
			seen[slice.Partition] || slice.Amount > grant.Amount-total {
			return fmt.Errorf("invalid stored credit slice")
		}
		seen[slice.Partition] = true
		total += slice.Amount
	}
	if total != grant.Amount {
		return fmt.Errorf("credit slices differ from allocation")
	}
	return nil
}

func readCreditGrant(ctx context.Context, client rueidis.Client, key, id string) (db.CreditGrantResult, error) {
	fields, err := client.Do(ctx, client.B().Hgetall().Key(key).Build()).AsStrMap()
	if err != nil {
		return db.CreditGrantResult{}, fmt.Errorf("read credit grant manifest: %w", err)
	}
	if len(fields) == 0 {
		return db.CreditGrantResult{}, fmt.Errorf("credit grant manifest is missing")
	}
	return decodeCreditGrant(fields, id)
}
