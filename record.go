package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Record is a validated, normalized log line.
type Record struct {
	RequestID string
	ClientID  string
	Endpoint  string
	StatusCode int
	// Timestamp is normalized to UTC, for internal comparison/sorting only.
	Timestamp time.Time
	// RawTimestamp is the original timestamp string from the input line,
	// preserved verbatim for output — we never re-emit a reformatted value.
	RawTimestamp string
}

type rawRecord struct {
	RequestID  *string         `json:"request_id"`
	Timestamp  *string         `json:"timestamp"`
	ClientID   *string         `json:"client_id"`
	Endpoint   *string         `json:"endpoint"`
	StatusCode json.RawMessage `json:"status_code"`
}

const (
	minValidStatusCode = 100
	maxValidStatusCode = 599
)

var timestampLayouts = []string{time.RFC3339, time.RFC3339Nano}

// ParseRecord parses and validates a single JSONL line into a Record. It
// never panics regardless of input, and returns a descriptive error (safe
// to log — it never contains raw field values) when the line is malformed.
func ParseRecord(line []byte) (Record, error) {
	var raw rawRecord
	if err := json.Unmarshal(line, &raw); err != nil {
		return Record{}, fmt.Errorf("invalid JSON: %w", err)
	}

	requestID, err := requiredString(raw.RequestID, "request_id")
	if err != nil {
		return Record{}, err
	}
	clientID, err := requiredString(raw.ClientID, "client_id")
	if err != nil {
		return Record{}, err
	}
	endpoint, err := requiredString(raw.Endpoint, "endpoint")
	if err != nil {
		return Record{}, err
	}

	if raw.Timestamp == nil {
		return Record{}, fmt.Errorf("missing required field: timestamp")
	}
	ts, err := parseTimestamp(*raw.Timestamp)
	if err != nil {
		return Record{}, fmt.Errorf("invalid timestamp: %w", err)
	}

	statusCode, err := parseStatusCode(raw.StatusCode)
	if err != nil {
		return Record{}, fmt.Errorf("invalid status_code: %w", err)
	}

	return Record{
		RequestID:    requestID,
		ClientID:     clientID,
		Endpoint:     endpoint,
		StatusCode:   statusCode,
		Timestamp:    ts.UTC(),
		RawTimestamp: *raw.Timestamp,
	}, nil
}

func requiredString(field *string, name string) (string, error) {
	if field == nil || *field == "" {
		return "", fmt.Errorf("missing or empty required field: %s", name)
	}
	return *field, nil
}

func parseTimestamp(s string) (time.Time, error) {
	var lastErr error
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// parseStatusCode accepts either a JSON number or a JSON string that parses
// to a whole number in the valid HTTP status range (100-599). Anything else
// — missing, a float, an out-of-range value, a non-numeric string, an
// object/array — is rejected.
func parseStatusCode(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("missing required field: status_code")
	}

	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("not valid JSON")
	}

	var n int
	switch t := v.(type) {
	case float64:
		if t != math.Trunc(t) {
			return 0, fmt.Errorf("must be a whole number")
		}
		n = int(t)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("not a valid integer")
		}
		n = parsed
	default:
		return 0, fmt.Errorf("must be a number or numeric string")
	}

	if n < minValidStatusCode || n > maxValidStatusCode {
		return 0, fmt.Errorf("out of valid HTTP status range (%d-%d)", minValidStatusCode, maxValidStatusCode)
	}
	return n, nil
}
