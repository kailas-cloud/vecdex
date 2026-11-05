package valkey

import (
	"errors"
	"math"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

func grantCommand() *db.CreditGrantCommand {
	return &db.CreditGrantCommand{Action: "grant", Grant: db.CreditGrant{ID: "grant", At: 100, ExpiresAt: 200},
		Partitions: []int{1, 32, 63, 94, 125, 156, 187, 218}, Wanted: 99, Minimum: 10, DailyLimit: 100, MonthlyLimit: 90}
}

func TestCreditGrantAllocationUsesBothPeriodsAndPreservesHistory(t *testing.T) {
	command := grantCommand()
	command.BaselineDay, command.BaselineMonth = 20, 25
	var state creditGrantTransition
	if err := applyCreditGrant(command, &state, 100); err != nil {
		t.Fatal(err)
	}
	if state.result.Grant.Amount != 65 || state.day.Booked != 85 || state.month.Booked != 90 {
		t.Fatalf("monthly history was ignored: %+v", state)
	}
	if len(state.result.Grant.Slices) != 6 {
		t.Fatalf("fragmented credit below invocation bound: %+v", state.result)
	}
	if err := validateStoredCreditGrant(&state.result.Grant, "grant"); err != nil {
		t.Fatal(err)
	}
	for _, slice := range state.result.Grant.Slices {
		if slice.Amount < command.Minimum {
			t.Fatalf("unusable credit slice: %+v", slice)
		}
	}
}

func TestCreditGrantUnknownHistoryAndOverflowRejectAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*db.CreditGrantCommand)
		quota  bool
	}{
		{"unknown daily history", func(c *db.CreditGrantCommand) { c.UnknownDay = 100 }, true},
		{"unknown monthly history", func(c *db.CreditGrantCommand) { c.UnknownMonth = 90 }, true},
		{"baseline overflow", func(c *db.CreditGrantCommand) { c.BaselineDay = math.MaxInt64; c.UnknownDay = 1 }, false},
		{"expired grant", func(c *db.CreditGrantCommand) { c.Grant.ExpiresAt = 100 }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := grantCommand()
			test.modify(command)
			var state creditGrantTransition
			err := applyCreditGrant(command, &state, 100)
			if err == nil || (test.quota && !errors.Is(err, domain.ErrEmbeddingQuotaExceeded)) {
				t.Fatalf("unsafe admission: %+v %v", state, err)
			}
			if state.found {
				t.Fatal("rejected admission published a grant")
			}
		})
	}
}

func TestCreditGrantReplayCannotReopenClosedGrant(t *testing.T) {
	command := grantCommand()
	var state creditGrantTransition
	if err := applyCreditGrant(command, &state, 100); err != nil {
		t.Fatal(err)
	}
	command.Action, command.Refund = "refund", 80
	if err := applyCreditGrant(command, &state, 100); err != nil {
		t.Fatal(err)
	}
	command.Action, command.BaselineDay, command.BaselineMonth = "grant", 99, 99
	if err := applyCreditGrant(command, &state, 100); err != nil {
		t.Fatal(err)
	}
	if !state.result.Closed || state.result.Released != 80 || state.day.Booked != 10 || state.month.Booked != 10 {
		t.Fatalf("replayed grant reopened or forgot its allocation: %+v", state)
	}
	state.month.present = false
	if err := applyCreditGrant(command, &state, 100); err == nil {
		t.Fatal("missing period was initialized below existing grant")
	}
}
