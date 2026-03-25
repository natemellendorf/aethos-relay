package gossipv1

import (
	"sync"
	"time"
)

type DrainStopReason string

const (
	DrainStopReasonNone                    DrainStopReason = ""
	DrainStopReasonConvergedNoWant         DrainStopReason = "converged_no_want"
	DrainStopReasonNoEligibleItems         DrainStopReason = "no_eligible_items"
	DrainStopReasonRepeatedNoProgress      DrainStopReason = "repeated_no_progress_rounds"
	DrainStopReasonSessionTimeBudget       DrainStopReason = "session_time_budget_exceeded"
	DrainStopReasonSessionByteBudget       DrainStopReason = "session_byte_budget_exceeded"
	DrainStopReasonSessionRoundBudget      DrainStopReason = "session_round_budget_exceeded"
	DrainStopReasonClientDisconnect        DrainStopReason = "client_disconnect"
	DrainStopReasonClientSilence           DrainStopReason = "client_silence"
	DrainStopReasonFairnessYield           DrainStopReason = "fairness_yield"
	DrainStopReasonProtocolFatal           DrainStopReason = "protocol_fatal"
	DrainStopReasonStorageError            DrainStopReason = "storage_error"
	DrainStopReasonSessionTransportBackoff DrainStopReason = "transport_backpressure"
)

type DrainSessionLimits struct {
	RoundBudget         int
	ByteBudget          int64
	WallClockBudget     time.Duration
	NoProgressRoundCap  int
	YieldEveryNRounds   int
	SilenceTimeout      time.Duration
	FairnessYieldEnable bool
}

func DefaultDrainSessionLimits() DrainSessionLimits {
	return DrainSessionLimits{
		RoundBudget:         8,
		ByteBudget:          8 << 20,
		WallClockBudget:     45 * time.Second,
		NoProgressRoundCap:  2,
		YieldEveryNRounds:   4,
		SilenceTimeout:      45 * time.Second,
		FairnessYieldEnable: true,
	}
}

func (l DrainSessionLimits) Normalize() DrainSessionLimits {
	out := l
	if out.RoundBudget < 0 {
		out.RoundBudget = 0
	}
	if out.ByteBudget < 0 {
		out.ByteBudget = 0
	}
	if out.WallClockBudget < 0 {
		out.WallClockBudget = 0
	}
	if out.NoProgressRoundCap < 0 {
		out.NoProgressRoundCap = 0
	}
	if out.YieldEveryNRounds < 0 {
		out.YieldEveryNRounds = 0
	}
	if out.SilenceTimeout < 0 {
		out.SilenceTimeout = 0
	}
	return out
}

type DrainSessionSnapshot struct {
	SessionID               string
	AuthenticatedWayfarerID string
	ConnectionStartAt       time.Time
	LastProgressAt          time.Time
	RoundsCompleted         int
	ItemsRequested          int
	ItemsTransferred        int
	ItemsAcknowledged       int
	BytesSent               int64
	BytesReceived           int64
	NoProgressStreak        int
	StopReason              DrainStopReason
}

type DrainSession struct {
	mu     sync.Mutex
	state  DrainSessionSnapshot
	limits DrainSessionLimits
}

func NewDrainSession(sessionID string, startedAt time.Time, limits DrainSessionLimits) *DrainSession {
	normalizedLimits := limits.Normalize()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &DrainSession{
		state: DrainSessionSnapshot{
			SessionID:         sessionID,
			ConnectionStartAt: startedAt,
		},
		limits: normalizedLimits,
	}
}

func (s *DrainSession) Limits() DrainSessionLimits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limits
}

func (s *DrainSession) BindAuthenticatedWayfarerID(wayfarerID string) {
	if wayfarerID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.AuthenticatedWayfarerID = wayfarerID
}

func (s *DrainSession) RecordBytesSent(bytes int) DrainStopReason {
	if bytes <= 0 {
		return DrainStopReasonNone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.BytesSent += int64(bytes)
	return s.stopReasonForBudgetExceeded(time.Now().UTC())
}

func (s *DrainSession) CanSendBytes(bytes int) (bool, DrainStopReason) {
	if bytes <= 0 {
		return true, DrainStopReasonNone
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if reason := s.stopReasonForBudgetExceeded(now); reason != DrainStopReasonNone {
		return false, reason
	}
	if s.limits.ByteBudget > 0 && (s.state.BytesSent+s.state.BytesReceived+int64(bytes)) >= s.limits.ByteBudget {
		s.state.StopReason = DrainStopReasonSessionByteBudget
		return false, s.state.StopReason
	}

	return true, DrainStopReasonNone
}

func (s *DrainSession) RecordBytesReceived(bytes int) DrainStopReason {
	if bytes <= 0 {
		return DrainStopReasonNone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.BytesReceived += int64(bytes)
	return s.stopReasonForBudgetExceeded(time.Now().UTC())
}

func (s *DrainSession) RecordRequested(count int) {
	if count <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ItemsRequested += count
	s.recordProgressLocked(time.Now().UTC())
}

func (s *DrainSession) RecordTransferred(count int) {
	if count <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ItemsTransferred += count
	s.recordProgressLocked(time.Now().UTC())
}

func (s *DrainSession) RecordAcknowledged(count int) {
	if count <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ItemsAcknowledged += count
	s.recordProgressLocked(time.Now().UTC())
}

func (s *DrainSession) CompleteRound(progressed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.RoundsCompleted++
	if progressed {
		s.state.NoProgressStreak = 0
		s.recordProgressLocked(time.Now().UTC())
		return
	}
	s.state.NoProgressStreak++
}

func (s *DrainSession) CanStartAnotherRound(now time.Time) (bool, DrainStopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if now.IsZero() {
		now = time.Now().UTC()
	}
	if s.state.StopReason != DrainStopReasonNone {
		return false, s.state.StopReason
	}

	if reason := s.stopReasonForBudgetExceeded(now); reason != DrainStopReasonNone {
		return false, reason
	}

	if s.limits.NoProgressRoundCap > 0 && s.state.NoProgressStreak >= s.limits.NoProgressRoundCap {
		s.state.StopReason = DrainStopReasonRepeatedNoProgress
		return false, s.state.StopReason
	}

	if s.limits.FairnessYieldEnable && s.limits.YieldEveryNRounds > 0 && s.state.RoundsCompleted > 0 && s.state.RoundsCompleted%s.limits.YieldEveryNRounds == 0 {
		s.state.StopReason = DrainStopReasonFairnessYield
		return false, s.state.StopReason
	}

	return true, DrainStopReasonNone
}

func (s *DrainSession) Stop(reason DrainStopReason) {
	if reason == DrainStopReasonNone {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.StopReason != DrainStopReasonNone {
		return
	}
	s.state.StopReason = reason
}

func (s *DrainSession) Snapshot() DrainSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *DrainSession) stopReasonForBudgetExceeded(now time.Time) DrainStopReason {
	if s.state.StopReason != DrainStopReasonNone {
		return s.state.StopReason
	}

	if s.limits.RoundBudget > 0 && s.state.RoundsCompleted >= s.limits.RoundBudget {
		s.state.StopReason = DrainStopReasonSessionRoundBudget
		return s.state.StopReason
	}
	if s.limits.WallClockBudget > 0 && now.Sub(s.state.ConnectionStartAt) >= s.limits.WallClockBudget {
		s.state.StopReason = DrainStopReasonSessionTimeBudget
		return s.state.StopReason
	}
	if s.limits.ByteBudget > 0 && (s.state.BytesSent+s.state.BytesReceived) >= s.limits.ByteBudget {
		s.state.StopReason = DrainStopReasonSessionByteBudget
		return s.state.StopReason
	}

	return DrainStopReasonNone
}

func (s *DrainSession) recordProgressLocked(now time.Time) {
	if now.IsZero() {
		return
	}
	s.state.LastProgressAt = now
}
