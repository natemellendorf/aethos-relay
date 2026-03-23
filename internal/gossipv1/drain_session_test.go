package gossipv1

import (
	"testing"
	"time"
)

func TestDrainSessionStopsOnNoProgressCap(t *testing.T) {
	startedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	session := NewDrainSession("ws-1", startedAt, DrainSessionLimits{
		NoProgressRoundCap: 2,
	})

	session.CompleteRound(false)
	session.CompleteRound(false)

	ok, reason := session.CanStartAnotherRound(startedAt.Add(time.Second))
	if ok {
		t.Fatal("expected no-progress cap to stop session")
	}
	if reason != DrainStopReasonRepeatedNoProgress {
		t.Fatalf("unexpected stop reason: got=%s want=%s", reason, DrainStopReasonRepeatedNoProgress)
	}
}

func TestDrainSessionStopsOnBudgets(t *testing.T) {
	startedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	t.Run("round budget", func(t *testing.T) {
		session := NewDrainSession("ws-round", startedAt, DrainSessionLimits{RoundBudget: 1})
		session.CompleteRound(true)
		ok, reason := session.CanStartAnotherRound(startedAt.Add(time.Second))
		if ok || reason != DrainStopReasonSessionRoundBudget {
			t.Fatalf("unexpected result ok=%v reason=%s", ok, reason)
		}
	})

	t.Run("byte budget", func(t *testing.T) {
		session := NewDrainSession("ws-byte", startedAt, DrainSessionLimits{ByteBudget: 16})
		session.RecordBytesReceived(9)
		reason := session.RecordBytesSent(8)
		if reason != DrainStopReasonSessionByteBudget {
			t.Fatalf("unexpected stop reason: got=%s want=%s", reason, DrainStopReasonSessionByteBudget)
		}
	})

	t.Run("time budget", func(t *testing.T) {
		session := NewDrainSession("ws-time", startedAt, DrainSessionLimits{WallClockBudget: 2 * time.Second})
		ok, reason := session.CanStartAnotherRound(startedAt.Add(3 * time.Second))
		if ok || reason != DrainStopReasonSessionTimeBudget {
			t.Fatalf("unexpected result ok=%v reason=%s", ok, reason)
		}
	})
}

func TestDrainSessionFairnessYield(t *testing.T) {
	startedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	session := NewDrainSession("ws-yield", startedAt, DrainSessionLimits{
		FairnessYieldEnable: true,
		YieldEveryNRounds:   2,
	})

	session.CompleteRound(true)
	session.CompleteRound(true)

	ok, reason := session.CanStartAnotherRound(startedAt.Add(time.Second))
	if ok {
		t.Fatal("expected fairness yield stop")
	}
	if reason != DrainStopReasonFairnessYield {
		t.Fatalf("unexpected stop reason: got=%s want=%s", reason, DrainStopReasonFairnessYield)
	}
}
