package valkey

import (
	"errors"
	"math"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

func poolCommand(action string) *db.CreditPoolCommand {
	return &db.CreditPoolCommand{Action: action, GrantID: "grant", Capacity: 100, ExpiresAt: 200,
		Amount: 40, Tokens: 7}
}

func transitionPool(t *testing.T, command *db.CreditPoolCommand, state *creditPoolTransition, live bool, now int64) {
	t.Helper()
	state.changed, state.writeReceipt, state.writeCounters, state.refund = false, false, false, 0
	if err := applyCreditPool(command, state, live, now); err != nil {
		t.Fatal(err)
	}
}

func assertCreditUsage(t *testing.T, state *creditPoolTransition, want domusage.Counters) {
	t.Helper()
	if state.counters.Day != want || state.counters.Month != want || state.counters.Total != want {
		t.Fatalf("credit usage: %+v, want %+v", state.counters, want)
	}
	if err := validateCreditPoolState(&state.pool, "grant"); err != nil {
		t.Fatal(err)
	}
}

func TestCreditPoolCompletionAfterClose(t *testing.T) {
	var state creditPoolTransition
	transitionPool(t, poolCommand("install"), &state, false, 100)
	transitionPool(t, poolCommand("reserve"), &state, true, 100)
	assertCreditUsage(t, &state, domusage.Counters{Reserved: 40})
	transitionPool(t, poolCommand("close"), &state, true, 100)
	assertCreditUsage(t, &state, domusage.Counters{Reserved: 40, Requests: 1, Unknown: 1})
	if !state.pool.Closed || state.pool.Available != 60 || state.pool.Pending != 1 {
		t.Fatalf("close released an outstanding invocation: %+v", state.pool)
	}
	transitionPool(t, poolCommand("close"), &state, false, 100)
	if state.changed {
		t.Fatal("second close changed usage")
	}
	transitionPool(t, poolCommand("complete"), &state, false, 250)
	assertCreditUsage(t, &state, domusage.Counters{Tokens: 7, Requests: 1})
	if state.pool.Available != 93 || state.pool.Pending != 0 || state.refund != 33 {
		t.Fatalf("late completion did not return only unused credits: %+v", state)
	}
	transitionPool(t, poolCommand("complete"), &state, false, 250)
	if state.changed || state.refund != 0 {
		t.Fatal("duplicate completion returned credit twice")
	}
}

func TestCreditPoolUnknownCompletionKeepsUpperBound(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "expired"}[closeFirst], func(t *testing.T) {
			var state creditPoolTransition
			transitionPool(t, poolCommand("install"), &state, false, 100)
			transitionPool(t, poolCommand("reserve"), &state, true, 100)
			complete := poolCommand("complete")
			complete.Unknown = true
			transitionPool(t, complete, &state, !closeFirst, 100)
			assertCreditUsage(t, &state, domusage.Counters{Tokens: 7, Reserved: 33, Requests: 1, Unknown: 1})
			if state.pool.Available != 60 || state.pool.Pending != 0 || state.refund != 0 {
				t.Fatalf("unknown spend released its upper bound: %+v", state)
			}
			transitionPool(t, poolCommand("close"), &state, false, 250)
			assertCreditUsage(t, &state, domusage.Counters{Tokens: 7, Reserved: 33, Requests: 1, Unknown: 1})
		})
	}
}

func TestCreditPoolUninstalledCloseFencesLateInstall(t *testing.T) {
	var state creditPoolTransition
	transitionPool(t, poolCommand("close"), &state, false, 100)
	if state.pool.Available != 100 || !state.pool.Closed || state.writeCounters {
		t.Fatalf("uninstalled grant was not fully recoverable: %+v", state)
	}
	if err := applyCreditPool(poolCommand("install"), &state, false, 100); !errors.Is(err, db.ErrCreditPoolClosed) {
		t.Fatalf("closed tombstone reopened: %v", err)
	}
}

func TestCreditPoolRejectsInvalidTransitions(t *testing.T) {
	var active creditPoolTransition
	transitionPool(t, poolCommand("install"), &active, false, 100)
	transitionPool(t, poolCommand("reserve"), &active, true, 100)
	for _, test := range []struct {
		name    string
		command *db.CreditPoolCommand
		modify  func(*creditPoolTransition)
		live    bool
		now     int64
	}{
		{"expired", poolCommand("reserve"), nil, true, 200},
		{"missing guard", poolCommand("reserve"), nil, false, 100},
		{"excess usage", &db.CreditPoolCommand{Action: "complete", Tokens: 41}, nil, true, 100},
		{"wrong grant", poolCommand("complete"), func(s *creditPoolTransition) { s.receipt.GrantID = "other" }, true, 100},
		{"missing receipt", poolCommand("complete"),
			func(s *creditPoolTransition) { s.receipt = creditInvocation{} }, true, 100},
		{"lost day", poolCommand("complete"),
			func(s *creditPoolTransition) { s.counters.Day = domusage.Counters{} }, true, 100},
		{"overflow", poolCommand("complete"),
			func(s *creditPoolTransition) { s.counters.Total.Tokens = math.MaxInt64 }, true, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := active
			if test.modify != nil {
				test.modify(&state)
			}
			if err := applyCreditPool(test.command, &state, test.live, test.now); err == nil {
				t.Fatal("invalid transition was accepted")
			}
		})
	}
}

func TestCreditPoolReservationCannotReplayCompletedSpend(t *testing.T) {
	var state creditPoolTransition
	transitionPool(t, poolCommand("install"), &state, false, 100)
	transitionPool(t, poolCommand("reserve"), &state, true, 100)
	transitionPool(t, poolCommand("reserve"), &state, true, 100)
	if state.changed {
		t.Fatal("duplicate reservation charged twice")
	}
	transitionPool(t, poolCommand("complete"), &state, true, 100)
	if err := applyCreditPool(poolCommand("reserve"), &state, true, 100); err == nil {
		t.Fatal("completed receipt authorized another spend")
	}
}

func TestCreditPoolCancellationBeforeUpstreamDoesNotCountUsage(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "closed"}[closed], func(t *testing.T) {
			var state creditPoolTransition
			transitionPool(t, poolCommand("install"), &state, false, 100)
			transitionPool(t, poolCommand("reserve"), &state, true, 100)
			if closed {
				transitionPool(t, poolCommand("close"), &state, true, 100)
			}
			c := poolCommand("cancel")
			c.Tokens = 0
			transitionPool(t, c, &state, !closed, 100)
			assertCreditUsage(t, &state, domusage.Counters{})
			if state.pool.Available != 100 || state.pool.Pending != 0 || state.refund != 40 {
				t.Fatalf("unstarted invocation retained a charge: %+v", state)
			}
			transitionPool(t, c, &state, !closed, 100)
			if state.changed || state.refund != 0 {
				t.Fatal("cancellation returned credit twice")
			}
		})
	}
}
