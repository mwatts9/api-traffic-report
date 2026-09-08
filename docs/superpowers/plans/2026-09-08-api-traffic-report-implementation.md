# API Traffic Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go CLI, `api-traffic-report`, that reads a JSONL API-request log and prints the JSON traffic report described in `docs/superpowers/specs/2026-09-08-api-traffic-report-design.md`.

**Architecture:** Flat `main`-package Go binary. A bounded line reader feeds raw lines to a per-line parser/validator, which produces `Record` values or a malformed-line reason. A `Builder` accumulates records (de-duplicating by `request_id`, counting per endpoint) and hands off to a rate-limit sliding-window detector. `main.go` wires flags, I/O, and a top-level panic recovery net around all of it.

**Tech Stack:** Go 1.22+ (developed/tested on go1.26.5), standard library only (`encoding/json`, `bufio`, `time`, `sort`, `flag`, `testing`, native `go test -fuzz`). No third-party dependencies.

## Global Constraints

- Language/runtime: Go, module `go.mod` declares `go 1.22`.
- Zero third-party dependencies (stdlib only) — supply-chain minimalism per the spec's Security section.
- Single flat `main` package — no `internal/`/multi-package split (see spec's Project Layout rationale).
- Every exported behavior must have a test; TDD order (failing test → implementation → passing test) for every step below.
- Rate-limit window: 10 seconds, `(timestamp - 10s, timestamp]`, left edge exclusive, right edge inclusive, threshold `> 5` requests, report only the earliest violation per client, output sorted by `client_id` ascending.
- Output JSON key order: `malformed_lines`, `rate_limit_violations`, `requests_per_endpoint`, `total_requests`; empty collections render as `{}`/`[]`, never `null`.
- Malformed-line content is never echoed verbatim to stdout/stderr, even in verbose mode (structural reason only).
- Default `--max-line-bytes` is 1 MiB (`1 << 20`); memory used while scanning a single line must stay bounded by this cap regardless of actual line length.
- `status_code` valid range is 100–599 inclusive; a JSON string that parses to a whole number in that range is accepted (coercion), everything else is malformed.
- Timestamps are parsed as RFC 3339 (both `Z` and `±HH:MM` offsets, with or without fractional seconds), normalized to UTC **only for internal comparison/sorting** — the original raw timestamp string from the input line is what gets echoed back in `rate_limit_violations[].timestamp`, never a reformatted value.

---

## Task 1: Module scaffolding + bounded line reader

**Files:**
- Create: `go.mod`
- Create: `linereader.go`
- Test: `linereader_test.go`

**Interfaces:**
- Produces: `newLineReader(r io.Reader, maxLineBytes int) *lineReader`, `(*lineReader).next() (line []byte, oversized bool, ok bool)`, `(*lineReader).err() error`. Later tasks (main.go) consume these: `ok == false` means end of input; `err()` returns a non-nil error only if the underlying reader failed for a reason other than EOF.

- [ ] **Step 1: Initialize the Go module**

```bash
cd ~/code/api-traffic-report
go mod init api-traffic-report
```

- [ ] **Step 2: Write the failing tests for the line reader**

Create `linereader_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestLineReader_Basic(t *testing.T) {
	input := "line one\nline two\nline three"
	lr := newLineReader(strings.NewReader(input), 1<<20)

	var got []string
	for {
		line, oversized, ok := lr.next()
		if !ok {
			break
		}
		if oversized {
			t.Fatalf("unexpected oversized line: %q", line)
		}
		got = append(got, string(line))
	}

	want := []string{"line one", "line two", "line three"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if err := lr.err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLineReader_EmptyInput(t *testing.T) {
	lr := newLineReader(strings.NewReader(""), 1<<20)
	_, _, ok := lr.next()
	if ok {
		t.Fatal("expected ok=false on empty input")
	}
}

func TestLineReader_OversizedLineIsBoundedAndFlagged(t *testing.T) {
	maxBytes := 16
	huge := strings.Repeat("x", maxBytes*1000) // far larger than maxBytes
	input := huge + "\n" + "short line"
	lr := newLineReader(strings.NewReader(input), maxBytes)

	line, oversized, ok := lr.next()
	if !ok {
		t.Fatal("expected a first line")
	}
	if !oversized {
		t.Fatal("expected first line to be flagged oversized")
	}
	if len(line) > maxBytes {
		t.Fatalf("oversized line retained %d bytes, want <= %d (memory must stay bounded)", len(line), maxBytes)
	}

	line2, oversized2, ok2 := lr.next()
	if !ok2 {
		t.Fatal("expected a second line after the oversized one")
	}
	if oversized2 {
		t.Fatal("second line should not be flagged oversized")
	}
	if string(line2) != "short line" {
		t.Fatalf("got %q, want %q", line2, "short line")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/code/api-traffic-report && go test ./... -run TestLineReader -v`
Expected: build failure — `newLineReader` undefined.

- [ ] **Step 4: Implement the bounded line reader**

Create `linereader.go`:

```go
package main

import (
	"bufio"
	"io"
)

// lineReader reads newline-delimited lines from r, guaranteeing that memory
// used per line never exceeds maxLineBytes even if the underlying line is
// arbitrarily large — this is the concrete defense against a pathological or
// adversarial line exhausting memory (see design spec's Security section).
type lineReader struct {
	br      *bufio.Reader
	max     int
	lastErr error
}

// newLineReader wraps r for bounded line-at-a-time reading. maxLineBytes
// caps how many bytes of any single line are retained; excess bytes are
// discarded, not buffered.
func newLineReader(r io.Reader, maxLineBytes int) *lineReader {
	return &lineReader{
		br:  bufio.NewReaderSize(r, 64*1024),
		max: maxLineBytes,
	}
}

// next returns the next line (without its trailing newline). oversized is
// true if the line exceeded maxLineBytes, in which case line holds only the
// first maxLineBytes bytes and the rest was discarded. ok is false once the
// input is exhausted; check err() afterward to distinguish clean EOF from a
// real read failure.
func (lr *lineReader) next() (line []byte, oversized bool, ok bool) {
	var buf []byte
	for {
		chunk, isPrefix, err := lr.br.ReadLine()
		if len(chunk) > 0 {
			if len(buf)+len(chunk) > lr.max {
				oversized = true
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err != nil {
			if err != io.EOF {
				lr.lastErr = err
			}
			if len(buf) == 0 && !oversized {
				return nil, false, false
			}
			return buf, oversized, true
		}
		if !isPrefix {
			return buf, oversized, true
		}
	}
}

// err returns the first non-EOF read error encountered, if any.
func (lr *lineReader) err() error {
	return lr.lastErr
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ~/code/api-traffic-report && go test ./... -run TestLineReader -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Commit**

```bash
cd ~/code/api-traffic-report
git add go.mod linereader.go linereader_test.go
git commit -m "feat: add bounded line reader

Reads lines without ever buffering more than max-line-bytes per line,
so a pathological or adversarial oversized line can't exhaust memory.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 2: Record parsing & validation

**Files:**
- Create: `record.go`
- Test: `record_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 (independent of `lineReader`).
- Produces: `type Record struct { RequestID string; ClientID string; Endpoint string; StatusCode int; Timestamp time.Time; RawTimestamp string }` and `func ParseRecord(line []byte) (Record, error)`. Task 3 (`Builder`) and Task 4 (`ratelimit`) consume `Record` by these exact field names; Task 5 (`main.go`) calls `ParseRecord`.

- [ ] **Step 1: Write the failing tests**

Create `record_test.go`:

```go
package main

import (
	"errors"
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
	_ = errors.New // keep errors imported for future assertions in this file
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd ~/code/api-traffic-report && go test ./... -run TestParseRecord -v`
Expected: build failure — `ParseRecord`/`Record` undefined.

- [ ] **Step 3: Implement record parsing & validation**

Create `record.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd ~/code/api-traffic-report && go test ./... -run TestParseRecord -v`
Expected: PASS (all cases)

- [ ] **Step 5: Commit**

```bash
cd ~/code/api-traffic-report
git add record.go record_test.go
git commit -m "feat: add per-line record parsing and validation

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 3: Report struct + Builder (dedup, per-endpoint counts)

**Files:**
- Create: `report.go`
- Test: `report_test.go`

**Interfaces:**
- Consumes: `Record` from Task 2 (fields `RequestID`, `ClientID`, `Endpoint`).
- Produces: `type RateLimitViolation struct { ClientID string; Timestamp string; RequestCount int }` (JSON tags `client_id`/`timestamp`/`request_count`), `type Report struct { MalformedLines int; RateLimitViolations []RateLimitViolation; RequestsPerEndpoint map[string]int; TotalRequests int }` (JSON tags `malformed_lines`/`rate_limit_violations`/`requests_per_endpoint`/`total_requests`, declared in that field order), `func NewBuilder() *Builder`, `(*Builder).AddMalformed()`, `(*Builder).AddRecord(r Record)`, `(*Builder).Build() Report`. Task 4 consumes the `byClient` data via a method `(*Builder).recordsByClient() map[string][]Record` used inside `Build`. Task 5 (`main.go`) calls `NewBuilder`, `AddMalformed`, `AddRecord`, `Build`.

- [ ] **Step 1: Write the failing tests**

Create `report_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd ~/code/api-traffic-report && go test ./... -run "TestBuilder|TestReport" -v`
Expected: build failure — `NewBuilder`/`Report` undefined.

- [ ] **Step 3: Implement Report and Builder**

Create `report.go`:

```go
package main

// RateLimitViolation is one client's earliest rate-limit violation.
type RateLimitViolation struct {
	ClientID     string `json:"client_id"`
	Timestamp    string `json:"timestamp"`
	RequestCount int    `json:"request_count"`
}

// Report is the final JSON structure printed to stdout. Field order here is
// the exact output key order required by the spec (alphabetical, matching
// the assessment's example): malformed_lines, rate_limit_violations,
// requests_per_endpoint, total_requests.
type Report struct {
	MalformedLines      int                  `json:"malformed_lines"`
	RateLimitViolations []RateLimitViolation `json:"rate_limit_violations"`
	RequestsPerEndpoint map[string]int       `json:"requests_per_endpoint"`
	TotalRequests       int                  `json:"total_requests"`
}

// Builder accumulates parsed records into a Report, de-duplicating by
// request_id as records are added.
type Builder struct {
	seen        map[string]bool
	byClient    map[string][]Record
	perEndpoint map[string]int
	total       int
	malformed   int
}

// NewBuilder returns an empty Builder ready to accept records.
func NewBuilder() *Builder {
	return &Builder{
		seen:        make(map[string]bool),
		byClient:    make(map[string][]Record),
		perEndpoint: make(map[string]int),
	}
}

// AddMalformed records one more malformed (failed-validation) line.
func (b *Builder) AddMalformed() {
	b.malformed++
}

// AddRecord adds a valid record, ignoring it if its request_id has already
// been seen (de-duplication, first occurrence wins).
func (b *Builder) AddRecord(r Record) {
	if b.seen[r.RequestID] {
		return
	}
	b.seen[r.RequestID] = true
	b.total++
	b.perEndpoint[r.Endpoint]++
	b.byClient[r.ClientID] = append(b.byClient[r.ClientID], r)
}

// recordsByClient exposes the deduplicated records grouped by client, for
// rate-limit detection.
func (b *Builder) recordsByClient() map[string][]Record {
	return b.byClient
}

// Build finalizes the Report, including rate-limit violation detection.
func (b *Builder) Build() Report {
	return Report{
		MalformedLines:      b.malformed,
		RateLimitViolations: DetectRateLimitViolations(b.recordsByClient()),
		RequestsPerEndpoint: b.perEndpoint,
		TotalRequests:       b.total,
	}
}
```

Note: this task references `DetectRateLimitViolations`, implemented in Task 4. To keep this task's tests passing independently, stub it now — Task 4 will replace the stub with the real implementation.

Create a temporary stub in `report.go` right below the `Builder` type (Task 4 deletes this and replaces it with the real file):

```go
// TODO(task 4): replaced by ratelimit.go's real implementation.
func DetectRateLimitViolations(byClient map[string][]Record) []RateLimitViolation {
	return []RateLimitViolation{}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd ~/code/api-traffic-report && go test ./... -run "TestBuilder|TestReport" -v`
Expected: PASS (all cases)

- [ ] **Step 5: Commit**

```bash
cd ~/code/api-traffic-report
git add report.go report_test.go
git commit -m "feat: add Report/Builder with request de-dup and endpoint counts

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 4: Rate-limit sliding-window detection

**Files:**
- Create: `ratelimit.go`
- Modify: `report.go` (delete the Task 3 stub of `DetectRateLimitViolations`)
- Test: `ratelimit_test.go`

**Interfaces:**
- Consumes: `Record` (fields `ClientID`, `Timestamp`, `RawTimestamp`) from Task 2; `RateLimitViolation` from Task 3.
- Produces: `func DetectRateLimitViolations(byClient map[string][]Record) []RateLimitViolation` — replaces the Task 3 stub with the real sliding-window algorithm. `Builder.Build` (Task 3) already calls this exact signature, so no other file needs to change.

- [ ] **Step 1: Write the failing tests**

Create `ratelimit_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd ~/code/api-traffic-report && go test ./... -run TestDetectRateLimitViolations -v`
Expected: `TestDetectRateLimitViolations_AppendixASample` and most others FAIL against the Task 3 stub (which always returns empty).

- [ ] **Step 3: Remove the Task 3 stub and implement the real algorithm**

In `report.go`, delete this block (added as a placeholder in Task 3):

```go
// TODO(task 4): replaced by ratelimit.go's real implementation.
func DetectRateLimitViolations(byClient map[string][]Record) []RateLimitViolation {
	return []RateLimitViolation{}
}
```

Create `ratelimit.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd ~/code/api-traffic-report && go test ./... -v`
Expected: PASS — every test in the module so far, including Tasks 1-3's tests (regression check).

- [ ] **Step 5: Commit**

```bash
cd ~/code/api-traffic-report
git add ratelimit.go ratelimit_test.go report.go
git commit -m "feat: implement rate-limit sliding-window violation detection

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 5: CLI wiring (main.go) + end-to-end sample test

**Files:**
- Create: `main.go`
- Create: `sample_input/requests.jsonl`
- Create: `sample_input/expected_output.json`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `newLineReader`/`(*lineReader).next`/`.err` (Task 1), `ParseRecord` (Task 2), `NewBuilder`/`AddMalformed`/`AddRecord`/`Build` and `Report` (Task 3).
- Produces: `func GenerateReport(r io.Reader, maxLineBytes int, verbose bool, diag io.Writer) (Report, error)` and `func main()`. Nothing downstream consumes these (this is the top of the call graph).

- [ ] **Step 1: Add the sample fixtures from the assessment's Appendix A/B**

Create `sample_input/requests.jsonl`:

```
{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}
{"request_id":"a1_2","timestamp":"2024-01-15T10:00:02Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}
{"request_id":"a1_3","timestamp":"2024-01-15T10:00:04Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}
{"request_id":"a1_4","timestamp":"2024-01-15T10:00:05Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}
{"request_id":"a1_5","timestamp":"2024-01-15T10:00:06Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}
{"request_id":"a1_6","timestamp":"2024-01-15T10:00:08Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}
{"request_id":"a2_1","timestamp":"2024-01-15T10:05:00Z","client_id":"acct_2","endpoint":"/v1/reports","status_code":200}
{"request_id":"a2_2","timestamp":"2024-01-15T10:05:05Z","client_id":"acct_2","endpoint":"/v1/reports","status_code":200}
```

Create `sample_input/expected_output.json`:

```json
{
  "malformed_lines": 0,
  "rate_limit_violations": [
    {
      "client_id": "acct_1",
      "request_count": 6,
      "timestamp": "2024-01-15T10:00:08Z"
    }
  ],
  "requests_per_endpoint": {
    "/v1/reports": 2,
    "/v1/widgets": 6
  },
  "total_requests": 8
}
```

- [ ] **Step 2: Write the failing tests**

Create `main_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestGenerateReport_SampleInputMatchesExpectedOutput(t *testing.T) {
	f, err := os.Open("sample_input/requests.jsonl")
	if err != nil {
		t.Fatalf("failed to open sample input: %v", err)
	}
	defer f.Close()

	report, err := GenerateReport(f, 1<<20, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	gotBytes, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatalf("re-unmarshal of got failed: %v", err)
	}

	wantBytes, err := os.ReadFile("sample_input/expected_output.json")
	if err != nil {
		t.Fatalf("failed to read expected output: %v", err)
	}
	var want map[string]interface{}
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("unmarshal of expected output failed: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report mismatch.\ngot:  %s\nwant: %s", gotBytes, wantBytes)
	}
}

func TestGenerateReport_MalformedLinesCountedAndNotEchoed(t *testing.T) {
	input := strings.Join([]string{
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200}`,
		`not json`,
		``,
		`{"request_id":"","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200}`,
		`{"request_id":"b","timestamp":"bad-timestamp","client_id":"c","endpoint":"/e","status_code":200}`,
	}, "\n")

	var diag bytes.Buffer
	report, err := GenerateReport(strings.NewReader(input), 1<<20, true, &diag)
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	if report.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want 1", report.TotalRequests)
	}
	if report.MalformedLines != 3 {
		t.Fatalf("MalformedLines = %d, want 3 (blank line should not count)", report.MalformedLines)
	}
	if strings.Contains(diag.String(), "not json") {
		t.Fatalf("verbose diagnostics must not echo raw line content, got: %s", diag.String())
	}
}

func TestGenerateReport_OversizedLineCountedAsMalformed(t *testing.T) {
	huge := strings.Repeat("x", 100)
	input := `{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200}` + "\n" + huge
	report, err := GenerateReport(strings.NewReader(input), 10, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	if report.TotalRequests != 1 || report.MalformedLines != 1 {
		t.Fatalf("got total=%d malformed=%d, want total=1 malformed=1", report.TotalRequests, report.MalformedLines)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd ~/code/api-traffic-report && go test ./... -run TestGenerateReport -v`
Expected: build failure — `GenerateReport` undefined.

- [ ] **Step 4: Implement main.go**

Create `main.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

const defaultMaxLineBytes = 1 << 20 // 1 MiB

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "api-traffic-report: unexpected internal error: %v\n", r)
			os.Exit(1)
		}
	}()

	verbose := flag.Bool("v", false, "print a short, structural reason per malformed line to stderr (never raw line content)")
	flag.BoolVar(verbose, "verbose", false, "alias for -v")
	maxLineBytes := flag.Int("max-line-bytes", defaultMaxLineBytes, "lines larger than this many bytes are treated as malformed")
	flag.Parse()

	path := "-"
	if flag.NArg() > 0 {
		path = flag.Arg(0)
	}

	var input io.Reader = os.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "api-traffic-report: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
			fmt.Fprintf(os.Stderr, "api-traffic-report: %s is a directory, not a file\n", path)
			os.Exit(1)
		}
		input = f
	}

	report, err := GenerateReport(input, *maxLineBytes, *verbose, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-traffic-report: %v\n", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-traffic-report: failed to encode report: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// GenerateReport reads JSONL from r, validating and aggregating each line,
// and returns the resulting Report. If verbose is true, a short structural
// reason (never raw line content) is written to diag for each malformed
// line. GenerateReport itself never panics; malformed data is reflected in
// the returned Report, not returned as an error. A non-nil error means the
// underlying reader failed (not that the data was bad).
func GenerateReport(r io.Reader, maxLineBytes int, verbose bool, diag io.Writer) (Report, error) {
	lr := newLineReader(r, maxLineBytes)
	b := NewBuilder()
	lineNo := 0

	for {
		line, oversized, ok := lr.next()
		if !ok {
			break
		}
		lineNo++

		if oversized {
			b.AddMalformed()
			if verbose {
				fmt.Fprintf(diag, "line %d: exceeds max line size (%d bytes)\n", lineNo, maxLineBytes)
			}
			continue
		}

		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue // blank line: not malformed, not a request
		}

		record, err := ParseRecord(trimmed)
		if err != nil {
			b.AddMalformed()
			if verbose {
				fmt.Fprintf(diag, "line %d: %v\n", lineNo, err)
			}
			continue
		}
		b.AddRecord(record)
	}

	if err := lr.err(); err != nil {
		return Report{}, fmt.Errorf("reading input: %w", err)
	}
	return b.Build(), nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd ~/code/api-traffic-report && go test ./... -v`
Expected: PASS — every test in the module.

- [ ] **Step 6: Manually verify the CLI end-to-end**

```bash
cd ~/code/api-traffic-report
go run . sample_input/requests.jsonl
```

Expected stdout: the same JSON as `sample_input/expected_output.json` (key order and formatting should match exactly).

- [ ] **Step 7: Commit**

```bash
cd ~/code/api-traffic-report
git add main.go main_test.go sample_input/
git commit -m "feat: wire up CLI entrypoint and GenerateReport orchestration

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 6: Fuzz test for the record parser

**Files:**
- Create: `record_fuzz_test.go`

**Interfaces:**
- Consumes: `ParseRecord` (Task 2). Produces nothing new — this is a test-only addition.

- [ ] **Step 1: Write the fuzz target**

Create `record_fuzz_test.go`:

```go
package main

import "testing"

// FuzzParseRecord searches for inputs that make ParseRecord panic. It is
// seeded with the hand-written malformed-input cases from record_test.go
// so the fuzzer starts from known-interesting inputs, then mutates from
// there. This is the automated, adversarial complement to the hand-picked
// edge cases: ParseRecord must never crash, no matter what bytes a hostile
// or simply broken upstream sends.
func FuzzParseRecord(f *testing.F) {
	seeds := []string{
		`{"request_id":"a1_1","timestamp":"2024-01-15T10:00:00Z","client_id":"acct_1","endpoint":"/v1/widgets","status_code":200}`,
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00+05:00","client_id":"c","endpoint":"/e","status_code":"200"}`,
		`not json at all`,
		`{}`,
		`[]`,
		`null`,
		`{"request_id":"","timestamp":"bad","client_id":"c","endpoint":"/e","status_code":"nope"}`,
		`{"status_code":{}}`,
		`{"request_id":123,"timestamp":123,"client_id":123,"endpoint":123,"status_code":123}`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ParseRecord panicked on input %q: %v", data, r)
			}
		}()
		_, _ = ParseRecord(data)
	})
}
```

- [ ] **Step 2: Run it as a regular test first (fast sanity check)**

Run: `cd ~/code/api-traffic-report && go test ./... -run FuzzParseRecord -v`
Expected: PASS (runs only the seed corpus in normal `go test` mode)

- [ ] **Step 3: Run the actual fuzzer briefly to confirm it executes**

Run: `cd ~/code/api-traffic-report && go test -fuzz FuzzParseRecord -fuzztime 20s`
Expected: `PASS` after ~20s, with no `Fuzz failure`. Report execs/sec in the output as evidence it genuinely fuzzed.

- [ ] **Step 4: Commit**

```bash
cd ~/code/api-traffic-report
git add record_fuzz_test.go
git commit -m "test: add native Go fuzz target for the record parser

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 7: README.md

**Files:**
- Create: `README.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Write the README**

Create `README.md`:

```markdown
# API Traffic Report

Reads a JSONL log of API requests and prints a single JSON traffic report to stdout.

## Run it

Requires Go 1.22+ (developed and tested on go1.26.5).

```bash
go run . path/to/requests.jsonl
```

Or build a binary:

```bash
go build -o api-traffic-report .
./api-traffic-report path/to/requests.jsonl
```

Reads from stdin if no path is given, or if the path is `-`:

```bash
cat path/to/requests.jsonl | ./api-traffic-report
```

Flags:
- `-v` / `--verbose`: print a short, structural reason (e.g. `line 42: invalid timestamp`) to stderr for each malformed line. Never prints the raw line content — third-party log data can carry sensitive fields, and the tool avoids duplicating that into a new output stream by default.
- `--max-line-bytes` (default `1048576`, i.e. 1 MiB): lines larger than this are counted as malformed rather than fully buffered, bounding memory per line regardless of how large an actual (or adversarial) line is.

Run the test suite:

```bash
go test ./...
```

Run the fuzz test for the line parser (native Go fuzzing, no extra tooling):

```bash
go test -fuzz FuzzParseRecord -fuzztime 30s
```

## Implementation decisions & assumptions

- **De-duplication key**: `request_id`. The spec says to de-duplicate but doesn't say by what — `request_id` is documented as the unique identifier, so a repeated `request_id` is treated as the same request logged twice (a flaky upstream retry), and the first occurrence (by file order) is kept.
- **Blank lines**: skipped silently — not counted as malformed, not counted as a request. The spec doesn't classify them either way.
- **`status_code` coercion**: a JSON string that parses to a whole number (e.g. `"200"`) is accepted, since some upstreams stringify numeric fields. The value must still fall in the valid HTTP range (100-599) after coercion; anything else (a float, an out-of-range number, a non-numeric string) is malformed.
- **Timestamp handling**: parsed as RFC 3339 (both `Z` and numeric offsets, with or without fractional seconds) and normalized to UTC for all internal comparisons (sorting, the rate-limit window). The *original* raw timestamp string from the input is what's echoed back in `rate_limit_violations[].timestamp` — never a reformatted value — so a request logged with a `-05:00` offset reports back in that same offset, not converted to `Z`.
- **Resource-exhaustion hardening**: a bounded line reader (`--max-line-bytes`, default 1 MiB) guarantees memory used scanning any single line stays capped, regardless of how large that line actually is on the wire. This is a deliberate security-minded choice, not just an accounting one — a naive implementation that reads a full line before checking its length would still be vulnerable to a single huge line exhausting memory.
- **Safe-by-default diagnostics**: `--verbose` reports *why* a line was malformed but never the line's raw content, since a malformed line from an untrusted upstream could contain PII or secrets.
- **Flat package layout**: everything lives in one `main` package across a handful of single-purpose files, rather than being split into `internal/`/multi-package layout. That Go feature exists to hide an API from external importers — this is a single binary with no external consumers, so it would add ceremony without benefit at this size.

## What I'd do differently with more time

- **Streaming for very large files**: memory is currently O(valid unique records), because de-duplication and the rate-limit window both require holding all of a client's records (the input isn't guaranteed sorted). For truly massive (multi-GB+) logs, I'd look at an external-sort or disk-backed approach so memory stays bounded independent of input size.
- **More precise malformed-line diagnostics**: right now a JSON-structure error (e.g. a field with the wrong type) collapses to one generic "invalid JSON" message rather than naming which field was wrong, because Go's `encoding/json` fails the whole line before field-level checks run. A hand-rolled tokenizer could report exactly which field and why.
- **Package split**: if this ever needed a second entry point (e.g. wrapped in an HTTP service, or imported as a library), I'd split `record`/`report`/`ratelimit` into real packages with defined exported surfaces.
- **Property-based tests for the sliding window**: beyond the fuzz test on the parser, a property-based test generating random timestamp sequences and checking the sliding-window result against a naive O(n²) reference implementation would add more confidence in the rate-limit algorithm specifically.

## AI tool usage

Built with Claude Code (Anthropic's CLI), used throughout: brainstorming the design and its tradeoffs, writing the implementation plan, and generating the implementation and tests via TDD. All output was reviewed before being committed.
```

- [ ] **Step 2: Commit**

```bash
cd ~/code/api-traffic-report
git add README.md
git commit -m "docs: add README with run instructions, assumptions, and AI usage disclosure

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

## Task 8: Final verification pass

**Files:** None created or modified — verification only.

**Interfaces:** None.

- [ ] **Step 1: Format and vet**

```bash
cd ~/code/api-traffic-report
gofmt -l .
```

Expected: no output (nothing unformatted). If any files are listed, run `gofmt -w .` and re-check.

```bash
go vet ./...
```

Expected: no output (clean).

- [ ] **Step 2: Full test suite**

```bash
cd ~/code/api-traffic-report
go test ./... -v
```

Expected: PASS, every test listed above.

- [ ] **Step 3: Fuzz for a longer stretch as a final resilience check**

```bash
cd ~/code/api-traffic-report
go test -fuzz FuzzParseRecord -fuzztime 60s
```

Expected: `PASS`, no crashes found.

- [ ] **Step 4: End-to-end CLI check against the sample fixture, byte-for-byte**

```bash
cd ~/code/api-traffic-report
go run . sample_input/requests.jsonl > /tmp/actual_output.json
diff <(jq -S . /tmp/actual_output.json) <(jq -S . sample_input/expected_output.json)
```

Expected: no diff output (if `jq` isn't installed, compare the two files by eye instead — key order was already pinned to match in Task 3/5).

- [ ] **Step 5: Confirm nothing is uncommitted**

```bash
cd ~/code/api-traffic-report
git status
```

Expected: `nothing to commit, working tree clean`.

- [ ] **Step 6: Report back to the user**

Summarize: all tests pass, `gofmt`/`go vet` clean, fuzz run clean, end-to-end sample output matches Appendix B exactly, working tree is clean. Ask the user whether they want the repo pushed to GitHub now (per the assessment's submission instructions) — creating a remote repo and pushing is a publishing action that needs their explicit go-ahead and a decision on visibility (public/private) and account/org.

---

## Self-Review Notes

- **Spec coverage**: every section of the design spec (CLI contract, parsing/validation rules including all five malformed-line conditions, de-dup, rate-limit algorithm with exact window semantics, output shape/key-order/empty-collection rules, all six security considerations, the full test plan, and the "known limitations" list for the README) maps to a task above.
- **Type consistency checked**: `Record` fields (`RequestID`, `ClientID`, `Endpoint`, `StatusCode`, `Timestamp`, `RawTimestamp`) are defined once in Task 2 and used with identical names in Tasks 3, 4, and 5. `Report`/`RateLimitViolation` are defined once in Task 3 and consumed as-is by Task 4 (return type) and Task 5 (marshaled directly). `DetectRateLimitViolations(byClient map[string][]Record) []RateLimitViolation` signature is identical between its Task 3 stub and Task 4's real implementation, so no other file needs to change when the stub is replaced.
- **No placeholders remain** other than the intentional, explicitly-flagged Task 3→4 stub, which exists so Task 3's tests can pass independently before Task 4 lands — this is a deliberate incremental-TDD seam, not an unfinished spec item.
