package main

import (
	"testing"
	"time"
)

func rec(clientID string, offsetSeconds int) Record {
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offsetSeconds) * time.Second)
	return Record{
		RequestID:    "",
		ClientID:     clientID,
		Endpoint:     "/e",
		StatusCode:   200,
		Timestamp:    ts,
		RawTimestamp: ts.Format(time.RFC3339),
	}
}

// TestDetectRateLimitViolations_AppendixASample reproduces the assessment's
// own worked example: acct_1 makes 6 requests at offsets 0,2,4,5,6,8 seconds
// (all within a 10s window of the 6th) and should violate at the 6th
// request (count=6); acct_2 makes 2 requests 5s apart and should not.
func TestDetectRateLimitViolations_AppendixASample(t *testing.T) {
	byClient := map[string][]Record{
		"acct_1": {rec("acct_1", 0), rec("acct_1", 2), rec("acct_1", 4), rec("acct_1", 5), rec("acct_1", 6), rec("acct_1", 8)},
		"acct_2": {rec("acct_2", 300), rec("acct_2", 305)},
	}
	got := DetectRateLimitViolations(byClient)
	if len(got) != 1 {
		t.Fatalf("got %d violations, want 1: %+v", len(got), got)
	}
	if got[0].ClientID != "acct_1" || got[0].RequestCount != 6 {
		t.Fatalf("got %+v, want client acct_1 count 6", got[0])
	}
	wantTS := time.Date(2024, 1, 1, 0, 0, 8, 0, time.UTC).Format(time.RFC3339)
	if got[0].Timestamp != wantTS {
		t.Fatalf("Timestamp = %q, want %q", got[0].Timestamp, wantTS)
	}
}

// TestDetectRateLimitViolations_LeftEdgeExclusive checks the window
// (t-10s, t]: a request exactly 10s before the current one must NOT count
// toward it. Requests at 0,1,2,3,4,10,10 for one client: at t=10, the
// window is (0,10], which excludes the request at t=0, leaving 6 requests
// (1,2,3,4,10,10) — a violation. Without the exclusive edge, t=0 would also
// be counted (7), which would still violate but for the wrong reason; this
// test's real point is verified by TestDetectRateLimitViolations_ExactlyAtThreshold.
func TestDetectRateLimitViolations_LeftEdgeExclusive(t *testing.T) {
	byClient := map[string][]Record{
		"acct_1": {rec("acct_1", 0), rec("acct_1", 1), rec("acct_1", 2), rec("acct_1", 3), rec("acct_1", 4), rec("acct_1", 10)},
	}
	got := DetectRateLimitViolations(byClient)
	// Window at t=10 is (0,10]: excludes t=0, includes 1,2,3,4,10 = 5 requests.
	// 5 is not > 5, so this must NOT be a violation.
	if len(got) != 0 {
		t.Fatalf("got %d violations, want 0 (count should be exactly 5, not a violation): %+v", len(got), got)
	}
}

func TestDetectRateLimitViolations_ExactlyAtThreshold(t *testing.T) {
	byClient := map[string][]Record{
		// Window at either t=10 request is (0,10]: excludes t=0, includes
		// 1,2,3,4,10,10 = 6 requests > 5 => violation.
		"acct_1": {rec("acct_1", 0), rec("acct_1", 1), rec("acct_1", 2), rec("acct_1", 3), rec("acct_1", 4), rec("acct_1", 10), rec("acct_1", 10)},
	}
	got := DetectRateLimitViolations(byClient)
	if len(got) != 1 {
		t.Fatalf("got %d violations, want 1: %+v", len(got), got)
	}
	if got[0].RequestCount != 6 {
		t.Fatalf("RequestCount = %d, want 6", got[0].RequestCount)
	}
}

func TestDetectRateLimitViolations_EarliestOnly(t *testing.T) {
	byClient := map[string][]Record{
		// Two separate bursts of 6; only the first (earlier) should be reported.
		"acct_1": {
			rec("acct_1", 0), rec("acct_1", 1), rec("acct_1", 2), rec("acct_1", 3), rec("acct_1", 4), rec("acct_1", 5),
			rec("acct_1", 100), rec("acct_1", 101), rec("acct_1", 102), rec("acct_1", 103), rec("acct_1", 104), rec("acct_1", 105),
		},
	}
	got := DetectRateLimitViolations(byClient)
	if len(got) != 1 {
		t.Fatalf("got %d violations, want 1", len(got))
	}
	wantTS := time.Date(2024, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339)
	if got[0].Timestamp != wantTS {
		t.Fatalf("Timestamp = %q, want earliest violation at %q", got[0].Timestamp, wantTS)
	}
}

func TestDetectRateLimitViolations_SortedByClientID(t *testing.T) {
	burst := func(client string) []Record {
		return []Record{rec(client, 0), rec(client, 1), rec(client, 2), rec(client, 3), rec(client, 4), rec(client, 5)}
	}
	byClient := map[string][]Record{
		"zebra": burst("zebra"),
		"alpha": burst("alpha"),
		"mango": burst("mango"),
	}
	got := DetectRateLimitViolations(byClient)
	if len(got) != 3 {
		t.Fatalf("got %d violations, want 3", len(got))
	}
	if got[0].ClientID != "alpha" || got[1].ClientID != "mango" || got[2].ClientID != "zebra" {
		t.Fatalf("not sorted by client_id: %+v", got)
	}
}

func TestDetectRateLimitViolations_UnsortedInputStillCorrect(t *testing.T) {
	byClient := map[string][]Record{
		"acct_1": {rec("acct_1", 8), rec("acct_1", 0), rec("acct_1", 5), rec("acct_1", 6), rec("acct_1", 2), rec("acct_1", 4)},
	}
	got := DetectRateLimitViolations(byClient)
	if len(got) != 1 || got[0].RequestCount != 6 {
		t.Fatalf("got %+v, want 1 violation with count 6 despite unsorted input", got)
	}
}

func TestDetectRateLimitViolations_NoViolations(t *testing.T) {
	got := DetectRateLimitViolations(map[string][]Record{
		"acct_1": {rec("acct_1", 0)},
	})
	if len(got) != 0 {
		t.Fatalf("got %d violations, want 0", len(got))
	}
}
