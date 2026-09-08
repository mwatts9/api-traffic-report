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

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	verbose := fs.Bool("v", false, "print a short, structural reason per malformed line to stderr (never raw line content)")
	fs.BoolVar(verbose, "verbose", false, "alias for -v")
	maxLineBytes := fs.Int("max-line-bytes", defaultMaxLineBytes, "lines larger than this many bytes are treated as malformed")
	if err := fs.Parse(os.Args[1:]); err != nil {
		// fs uses ContinueOnError, so it already printed its own usage/error
		// message to its output (stderr by default) — just exit, don't
		// double-print.
		os.Exit(1)
	}

	path := "-"
	if fs.NArg() > 0 {
		path = fs.Arg(0)
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
