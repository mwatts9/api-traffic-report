package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const defaultMaxLineBytes = 1 << 20 // 1 MiB

// utf8BOM is the UTF-8 byte order mark. Some producers (notably Windows
// tooling) prepend it to a text file; it is not part of the first record's
// JSON, so it is stripped from the very first line rather than being allowed
// to make that line parse as malformed.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run executes the CLI and returns the process exit code: 0 when a report
// was produced (regardless of how many input lines were malformed), 1 on a
// CLI/IO-level failure. It never panics — the top-level recover() below is
// the last-resort net that turns anything unforeseen into a clean stderr
// message and exit 1 instead of a raw panic trace.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) (exitCode int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "api-traffic-report: unexpected internal error: %v\n", r)
			exitCode = 1
		}
	}()

	fs := flag.NewFlagSet("api-traffic-report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("v", false, "print a short, structural reason per malformed line to stderr (never raw line content)")
	fs.BoolVar(verbose, "verbose", false, "alias for -v")
	maxLineBytes := fs.Int("max-line-bytes", defaultMaxLineBytes, "lines larger than this many bytes are treated as malformed")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// -h/--help: the flag package already printed usage to fs's
			// output. Asking for help is a success, not a failure.
			return 0
		}
		// fs uses ContinueOnError, so it already printed its own usage/error
		// message to its output — just exit, don't double-print.
		return 1
	}

	if *maxLineBytes < 1 {
		// Fail closed: a non-positive cap would make every line "oversized",
		// producing a confidently-wrong all-malformed report.
		fmt.Fprintf(stderr, "api-traffic-report: --max-line-bytes must be a positive integer\n")
		return 1
	}

	path := "-"
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}

	input := stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(stderr, "api-traffic-report: %v\n", err)
			return 1
		}
		defer f.Close()
		if info, statErr := f.Stat(); statErr == nil && info.IsDir() {
			fmt.Fprintf(stderr, "api-traffic-report: %s is a directory, not a file\n", path)
			return 1
		}
		input = f
	}

	report, err := GenerateReport(input, *maxLineBytes, *verbose, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "api-traffic-report: %v\n", err)
		return 1
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "api-traffic-report: failed to encode report: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(out))
	return 0
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

		if lineNo == 1 && !oversized {
			// Strip a UTF-8 BOM if the input starts with one. Only the very
			// first line of the whole input can carry it.
			line = bytes.TrimPrefix(line, utf8BOM)
		}

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
