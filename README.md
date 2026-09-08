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
