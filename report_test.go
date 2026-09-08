package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustRecord(t *testing.T, requestID, clientID, endpoint string, ts time.Time) Record {
	t.Helper()
	return Record{
		RequestID:    requestID,
		ClientID:     clientID,
		Endpoint:     endpoint,
		StatusCode:   200,
		Timestamp:    ts,
		RawTimestamp: ts.Format(time.RFC3339),
	}
}

func TestBuilder_DedupByRequestID(t *testing.T) {
	b := NewBuilder()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b.AddRecord(mustRecord(t, "dup", "acct_1", "/v1/widgets", now))
	b.AddRecord(mustRecord(t, "dup", "acct_1", "/v1/widgets", now.Add(time.Second)))
	b.AddRecord(mustRecord(t, "unique", "acct_1", "/v1/widgets", now.Add(2*time.Second)))

	report := b.Build()
	if report.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2 (duplicate request_id should count once)", report.TotalRequests)
	}
	if report.RequestsPerEndpoint["/v1/widgets"] != 2 {
		t.Fatalf("RequestsPerEndpoint[/v1/widgets] = %d, want 2", report.RequestsPerEndpoint["/v1/widgets"])
	}
}

func TestBuilder_PerEndpointCounts(t *testing.T) {
	b := NewBuilder()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b.AddRecord(mustRecord(t, "1", "acct_1", "/v1/widgets", now))
	b.AddRecord(mustRecord(t, "2", "acct_1", "/v1/reports", now))
	b.AddRecord(mustRecord(t, "3", "acct_2", "/v1/widgets", now))

	report := b.Build()
	if report.RequestsPerEndpoint["/v1/widgets"] != 2 {
		t.Errorf("/v1/widgets = %d, want 2", report.RequestsPerEndpoint["/v1/widgets"])
	}
	if report.RequestsPerEndpoint["/v1/reports"] != 1 {
		t.Errorf("/v1/reports = %d, want 1", report.RequestsPerEndpoint["/v1/reports"])
	}
}

func TestBuilder_MalformedLinesCounted(t *testing.T) {
	b := NewBuilder()
	b.AddMalformed()
	b.AddMalformed()
	report := b.Build()
	if report.MalformedLines != 2 {
		t.Fatalf("MalformedLines = %d, want 2", report.MalformedLines)
	}
}

func TestReport_EmptyCollectionsMarshalAsEmptyNotNull(t *testing.T) {
	b := NewBuilder()
	report := b.Build()

	out, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "null") {
		t.Fatalf("expected no null fields in empty report, got: %s", s)
	}
	if !strings.Contains(s, `"rate_limit_violations":[]`) {
		t.Errorf("expected empty rate_limit_violations as [], got: %s", s)
	}
	if !strings.Contains(s, `"requests_per_endpoint":{}`) {
		t.Errorf("expected empty requests_per_endpoint as {}, got: %s", s)
	}
}

func TestReport_KeyOrderMatchesSpec(t *testing.T) {
	b := NewBuilder()
	out, err := json.Marshal(b.Build())
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	s := string(out)
	order := []string{`"malformed_lines"`, `"rate_limit_violations"`, `"requests_per_endpoint"`, `"total_requests"`}
	last := -1
	for _, key := range order {
		idx := strings.Index(s, key)
		if idx == -1 {
			t.Fatalf("key %s not found in %s", key, s)
		}
		if idx < last {
			t.Fatalf("key %s out of order in %s", key, s)
		}
		last = idx
	}
}
