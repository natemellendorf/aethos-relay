package model

import "sort"

// RelayScoreSlice is a slice of RelayScore for sorting.
type RelayScoreSlice []*RelayScore

// Len returns the length of the slice.
func (p RelayScoreSlice) Len() int {
	return len(p)
}

// Less returns true if i's score is less than j's score.
func (p RelayScoreSlice) Less(i, j int) bool {
	return p[i].Score < p[j].Score
}

// Swap swaps elements i and j.
func (p RelayScoreSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// SortByScore sorts relays by score in descending order (highest first).
func SortByScore(relays []*RelayScore) {
	sort.Sort(sort.Reverse(RelayScoreSlice(relays)))
}

// RelaySelector handles adaptive relay selection for publishing.
type RelaySelector struct {
	currentWidth    int
	minWidth        int
	maxWidth        int
	recentFailures  int
	recentSuccesses int
	windowSize      int
}

// NewRelaySelector creates a new relay selector with adaptive width.
func NewRelaySelector() *RelaySelector {
	return &RelaySelector{
		currentWidth:    MIN_PUBLISH_WIDTH,
		minWidth:        MIN_PUBLISH_WIDTH,
		maxWidth:        MAX_PUBLISH_WIDTH,
		recentFailures:  0,
		recentSuccesses: 0,
		windowSize:      10, // Track last 10 publishes
	}
}

// RecordPublishAttempt records a publish attempt result.
func (s *RelaySelector) RecordPublishAttempt(success bool) {
	if success {
		s.recentSuccesses++
	} else {
		s.recentFailures++
	}

	// Keep window bounded
	total := s.recentFailures + s.recentSuccesses
	if total > s.windowSize {
		// Remove oldest (approximate by scaling down)
		ratio := float64(s.windowSize) / float64(total)
		s.recentFailures = int(float64(s.recentFailures) * ratio)
		s.recentSuccesses = int(float64(s.recentSuccesses) * ratio)
	}

	s.adjustWidth()
}

// adjustWidth adjusts the publish width based on recent success/failure rate.
func (s *RelaySelector) adjustWidth() {
	total := s.recentFailures + s.recentSuccesses
	if total < 3 {
		return // Not enough data to adjust
	}

	failureRate := float64(s.recentFailures) / float64(total)

	// Increase width on unhealthy signals (high failure rate)
	if failureRate > 0.5 {
		s.currentWidth++
	} else if failureRate > 0.3 {
		// Slight increase
		if s.currentWidth < s.maxWidth {
			s.currentWidth++
		}
	}

	// Decrease width when stable (low failure rate)
	if failureRate < 0.1 && s.currentWidth > s.minWidth {
		s.currentWidth--
	}

	// Clamp to bounds
	if s.currentWidth < s.minWidth {
		s.currentWidth = s.minWidth
	}
	if s.currentWidth > s.maxWidth {
		s.currentWidth = s.maxWidth
	}
}

// CurrentWidth returns the current publish width.
func (s *RelaySelector) CurrentWidth() int {
	return s.currentWidth
}

// SelectRelays selects the top N relays by score.
func SelectRelays(relays []*RelayScore, width int) []*RelayScore {
	// Sort by score descending
	SortByScore(relays)

	// Return top N (or all if fewer than N)
	if len(relays) < width {
		width = len(relays)
	}

	if width == 0 {
		return nil
	}

	return relays[:width]
}
