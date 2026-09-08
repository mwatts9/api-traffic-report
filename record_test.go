package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseRecord_Valid(t *testing.T) {
	line := []byte(`{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}`)
	rec, err := ParseRecord(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.RequestID != "a1_1" || rec.ClientID != "acct_1" || rec.Endpoint != "/v1/widgets" || rec.StatusCode != 200 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.RawTimestamp != "2024-01-15T10:00:00Z" {
		t.Fatalf("RawTimestamp = %q, want original string preserved", rec.RawTimestamp)
	}
	want := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	if !rec.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v", rec.Timestamp, want)
	}
}

func TestParseRecord_OffsetTimestampNormalizedForComparisonButRawPreserved(t *testing.T) {
	line := []byte(`{"request_id":"a1_1","timestamp":"2024-01-15T05:00:00-05:00","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}`)
	rec, err := ParseRecord(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.RawTimestamp != "2024-01-15T05:00:00-05:00" {
		t.Fatalf("RawTimestamp = %q, want original offset string preserved", rec.RawTimestamp)
	}
	want := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	if !rec.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v (normalized to UTC instant)", rec.Timestamp, want)
	}
}

func TestParseRecord_InvalidJSON(t *testing.T) {
	_, err := ParseRecord([]byte(`not json at all`))
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestParseRecord_MissingOrEmptyRequiredFields(t *testing.T) {
	cases := []string{
		`{"timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}`,
		`{"request_id":"","timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}`,
		`{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","endpoint":"/v1/widgets","status_code":200}`,
		`{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","client_id":"","endpoint":"/v1/widgets","status_code":200}`,
		`{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","status_code":200}`,
		`{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","endpoint":"","status_code":200}`,
	}
	for _, c := range cases {
		if _, err := ParseRecord([]byte(c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestParseRecord_InvalidTimestamp(t *testing.T) {
	cases := []string{
		`{"request_id":"a","timestamp":"not-a-date","client_id":"c","endpoint":"/e","status_code":200}`,
		`{"request_id":"a","timestamp":"2024-01-15","client_id":"c","endpoint":"/e","status_code":200}`,
		`{"request_id":"a","client_id":"c","endpoint":"/e","status_code":200}`,
	}
	for _, c := range cases {
		if _, err := ParseRecord([]byte(c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestParseRecord_ErrorMessagesNeverLeakRawValues(t *testing.T) {
	// Test that timestamp error messages don't include the raw timestamp value
	rawTimestamp := "not-a-date"
	line := []byte(`{"request_id":"a","timestamp":"not-a-date","client_id":"c","endpoint":"/e","status_code":200}`)
	_, err := ParseRecord(line)
	if err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
	if strings.Contains(err.Error(), rawTimestamp) {
		t.Errorf("timestamp error message leaked raw value: %q contains %q", err.Error(), rawTimestamp)
	}
	if strings.Contains(err.Error(), "parsing time") {
		t.Errorf("timestamp error message contains raw time.Parse error: %q", err.Error())
	}

	// Test that JSON error messages don't leak raw values
	badJSON := "not json at all"
	_, err = ParseRecord([]byte(badJSON))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if strings.Contains(err.Error(), badJSON) {
		t.Errorf("JSON error message leaked raw value: %q contains %q", err.Error(), badJSON)
	}
}

func TestParseRecord_StatusCodeCoercedFromString(t *testing.T) {
	line := []byte(`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":"200"}`)
	rec, err := ParseRecord(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", rec.StatusCode)
	}
}

func TestParseRecord_StatusCodeInvalid(t *testing.T) {
	cases := []string{
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200.5}`,
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":99}`,
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":600}`,
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":"not-a-number"}`,
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e"}`,
	}
	for _, c := range cases {
		if _, err := ParseRecord([]byte(c)); err == nil {
			t.Errorf("expected error for %s", c)
		}
	}
}

func TestParseRecord_NeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseRecord panicked: %v", r)
		}
	}()
	weird := [][]byte{
		nil, {}, []byte("{"), []byte("[]"), []byte("null"), []byte(`{"status_code":{}}`),
		[]byte(`{"request_id":123}`), []byte(`{"timestamp":123}`),
	}
	for _, w := range weird {
		_, _ = ParseRecord(w)
	}
}
