# API Traffic Report — Design Spec

Date: 2026-09-08
Status: Approved

## Purpose

Implement the Circumvent take-home exercise ("API Traffic Report"): a CLI that reads a
JSONL log of API requests and prints a single JSON traffic report to stdout, per the
assessment PDF. The bar is a senior/staff-level solution: resilient to malformed/
unexpected upstream data, never crashes, and is written with the judgment of a security
company (defense-in-depth on untrusted input, no unnecessary attack surface).

## Language / runtime

Go 1.22+. Single compiled binary, no runtime dependency for whoever reviews it.

## Project layout

Flat `main` package — deliberately not split into `internal/`/multi-package layout.
`internal/` is a Go-compiler visibility boundary for protecting an API from external
importers; this is a single-binary CLI with no external consumers, so it buys nothing.
Multi-package layering would add enforced boundaries and independent testability, but
for ~500 lines across a handful of already-separate files, the "one clear purpose per
file" benefit is achieved without the extra ceremony. Revisit if this ever grows a
second entry point (e.g. a library other tools import, or an HTTP service wrapping the
same logic).

```
api-traffic-report/
  main.go                # CLI entry: flags, orchestration, top-level panic recovery
  record.go              # LogRecord type + per-line parse/validation
  report.go              # dedup, per-endpoint counts, JSON report struct + marshal
  ratelimit.go           # sliding-window violation detection
  main_test.go
  record_test.go
  record_fuzz_test.go    # FuzzParseRecord
  ratelimit_test.go
  report_test.go
  sample_input/requests.jsonl
  sample_input/expected_output.json
  README.md
  go.mod
```

## CLI contract

`api-traffic-report <path-to.jsonl>` reads the given file. If the argument is omitted or
is `-`, reads stdin instead (pipeable). Report JSON goes to stdout.

Flags:
- `-v`, `--verbose`: print a redaction-aware, one-line reason per malformed line to
  stderr (e.g. `line 42: invalid timestamp`) — never the raw line content, since
  third-party log data may carry sensitive fields (see Security).
- `--max-line-bytes` (default 1 MiB): lines larger than this are treated as malformed
  rather than fully buffered.

Exit codes: `0` on success (a report was produced, regardless of how many lines were
malformed). `1` on a CLI/IO-level failure — bad flags, file not found, unreadable file,
path is a directory, etc. These are distinct from "malformed data inside a valid file,"
which is always a `0`-exit report.

## Parsing & validation

A line is counted as **malformed** if any of the following hold; otherwise it's a valid
record:

- The line isn't valid JSON.
- `request_id`, `client_id`, or `endpoint` is missing, not a string, or empty.
- `timestamp` is missing or fails RFC 3339 parsing. Both `Z` and `+HH:MM`/`-HH:MM`
  offsets are accepted; all timestamps are normalized to UTC internally for comparison.
- `status_code` is missing, or isn't a whole-number integer, or falls outside the valid
  HTTP status range 100–599. A numeric value expressed as a JSON string (e.g.
  `"200"`) is coerced and accepted — an explicit resilience concession for upstreams
  that stringify numbers — but the 100–599 range check still applies after coercion.
- The line exceeds `--max-line-bytes`.

Unknown/extra JSON fields are ignored, not a failure. A blank/whitespace-only line is
skipped silently — not counted as malformed, not counted as a request (documented
assumption; the spec doesn't classify empty lines either way).

Lines are read with `bufio.Reader.ReadString('\n')`, not a fixed-capacity
`bufio.Scanner` buffer, so a normal-but-long line can't be spuriously rejected by a
buffer-size error — the explicit `--max-line-bytes` cap is the one deliberate limit,
enforced by us on purpose rather than hit by accident.

## De-duplication

Key = `request_id`. First occurrence by file order wins; later lines with the same
`request_id` are dropped from `total_requests`, `requests_per_endpoint`, and rate-limit
input. The spec doesn't define what to do if a duplicate's other fields disagree with
the first — we trust the first-seen record and ignore the rest (documented assumption).

## Rate-limit violations

Group valid, deduplicated records by `client_id`; sort each client's records by
timestamp (input is not guaranteed sorted). Two-pointer sliding window per client: for
a request at time *t*, count that client's requests with timestamp in `(t-10s, t]`. A
count `> 5` is a violation at that timestamp. Keep only the **earliest** violation (by
timestamp) per client. Final list is sorted by `client_id` ascending.

## Output

Pretty-printed JSON (2-space indent), keys ordered `malformed_lines`,
`rate_limit_violations`, `requests_per_endpoint`, `total_requests` — matching Appendix
B's example byte-for-byte ordering, even though a grader that parses JSON wouldn't care
about key order. Empty collections render as `{}` / `[]`, never `null`.

## Security considerations

- **Resource-exhaustion hardening**: `--max-line-bytes` (default 1 MiB) bounds memory
  used by any single line — protects against a pathological or adversarial line (huge
  or malformed on purpose) exhausting memory. This is the concrete mechanism behind
  "must not crash" for hostile input, not just well-formed-but-unusual input.
- **Defense-in-depth field validation**: `status_code` is restricted to 100–599 rather
  than "any integer," so an absurd value can't silently skew `requests_per_endpoint`/
  totals — it's classified as malformed instead.
- **Safe-by-default output**: malformed lines are counted, never echoed verbatim, to
  stdout or stderr. Third-party logs can carry PII or secrets in a field that happens
  to be malformed; the tool shouldn't duplicate that into a new output stream by
  default. `-v/--verbose` prints a short, structural reason only (field name / failure
  kind), never raw field values.
- **Fail-closed CLI/IO handling**: bad flags, missing/unreadable files, or a directory
  passed as the input path produce a clear stderr message and exit `1` — never a stack
  trace.
- **Supply-chain minimalism**: Go stdlib only, zero third-party dependencies.
  `gofmt`/`go vet` run clean (noted in README); nothing to run `govulncheck` against.
- **Fuzz testing**: `FuzzParseRecord` (Go's native `go test -fuzz`) targets the
  per-line parser, seeded from the hand-written malformed-input test cases, to search
  for panics on adversarial byte sequences — an automated, adversarial complement to
  "must not crash on unexpected data," not just hand-picked edge cases.
- **Last-resort net**: a single top-level `recover()` in `main` catches anything
  unforeseen that still slips through, printing a clean stderr message and exiting `1`
  instead of a raw panic trace. This should never actually trigger given the defensive
  parsing above — it's the staff-level guarantee, not the primary mechanism.

## Testing

Go `testing` package:
- `record_test.go`: parse/validation edge cases — bad timestamp formats/offsets,
  missing/empty fields, coerced string status codes, out-of-range status codes, blank
  lines, oversized lines.
- `record_fuzz_test.go`: `FuzzParseRecord` over the raw line parser.
- `ratelimit_test.go`: sliding-window correctness including the exact `t-10s` exclusive
  boundary, multiple clients, out-of-order input.
- `report_test.go`: dedup behavior, per-endpoint counting, output key order / empty
  collections.
- `main_test.go`: end-to-end run against `sample_input/requests.jsonl`, diffed against
  `sample_input/expected_output.json`.

## Known limitations / future work (for README "what I'd do with more time")

- Memory is O(valid unique records) — dedup and rate-limit checks require holding all
  records because input isn't guaranteed sorted. True multi-GB-scale input would need
  an external-sort or disk-backed approach.
- Flat single-package layout; would split into packages if this grew a second consumer
  (library import, HTTP service, etc.).
- No config file / structured logging — flags and stderr are sufficient at this scale.
