package main

import (
	"sort"
	"time"
)

const (
	rateLimitWindow    = 10 * time.Second
	rateLimitThreshold = 5 // strictly more than this many requests in the window is a violation
)

// DetectRateLimitViolations returns the earliest rate-limit violation for
// each client that has one, sorted by client_id ascending. For a request at
// time t, the window is (t-10s, t] — left edge exclusive, right edge
// (the request itself) inclusive.
func DetectRateLimitViolations(byClient map[string][]Record) []RateLimitViolation {
	violations := make([]RateLimitViolation, 0, len(byClient))
	for clientID, records := range byClient {
		sorted := append([]Record(nil), records...)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Timestamp.Before(sorted[j].Timestamp)
		})
		if v, ok := earliestViolation(clientID, sorted); ok {
			violations = append(violations, v)
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].ClientID < violations[j].ClientID
	})
	return violations
}

// earliestViolation scans a client's timestamp-sorted records with a
// two-pointer sliding window and returns the first (earliest) point at
// which more than rateLimitThreshold requests fall in the trailing 10s
// window.
func earliestViolation(clientID string, sorted []Record) (RateLimitViolation, bool) {
	left := 0
	for right := 0; right < len(sorted); right++ {
		windowStart := sorted[right].Timestamp.Add(-rateLimitWindow)
		for !sorted[left].Timestamp.After(windowStart) {
			left++
		}
		count := right - left + 1
		if count > rateLimitThreshold {
			return RateLimitViolation{
				ClientID:     clientID,
				Timestamp:    sorted[right].RawTimestamp,
				RequestCount: count,
			}, true
		}
	}
	return RateLimitViolation{}, false
}
