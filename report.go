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

