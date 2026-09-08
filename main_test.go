package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	// maxLineBytes must sit strictly between the normal line's length (103
	// bytes) and the oversized line's length (300 bytes) so that only the
	// latter trips lineReader's oversized flag.
	const maxLineBytes = 150
	huge := strings.Repeat("x", 300)
	input := `{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200}` + "\n" + huge
	report, err := GenerateReport(strings.NewReader(input), maxLineBytes, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	if report.TotalRequests != 1 || report.MalformedLines != 1 {
		t.Fatalf("got total=%d malformed=%d, want total=1 malformed=1", report.TotalRequests, report.MalformedLines)
	}
}

// TestGenerateReport_SampleOutputIsByteExact is deliberately stricter than the
// map-based comparison above: it asserts the exact bytes the CLI writes, so a
// struct field reorder (which changes JSON key order but not semantics) can
// never again silently diverge from the committed fixture.
func TestGenerateReport_SampleOutputIsByteExact(t *testing.T) {
	got := renderSampleReport(t)

	want, err := os.ReadFile("sample_input/expected_output.json")
	if err != nil {
		t.Fatalf("failed to read expected output: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("byte-exact mismatch with sample_input/expected_output.json.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// renderSampleReport produces the exact bytes the CLI writes to stdout for
// sample_input/requests.jsonl (MarshalIndent output plus the trailing newline
// from Fprintln).
func renderSampleReport(t *testing.T) []byte {
	t.Helper()

	f, err := os.Open("sample_input/requests.jsonl")
	if err != nil {
		t.Fatalf("failed to open sample input: %v", err)
	}
	defer f.Close()

	report, err := GenerateReport(f, defaultMaxLineBytes, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}
	return append(out, '\n')
}

func TestGenerateReport_LeadingUTF8BOMIsStripped(t *testing.T) {
	lines := strings.Join([]string{
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200}`,
		`{"request_id":"b","timestamp":"2024-01-15T10:00:01Z","client_id":"c","endpoint":"/e","status_code":200}`,
	}, "\n")

	plain, err := GenerateReport(strings.NewReader(lines), defaultMaxLineBytes, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport (no BOM) failed: %v", err)
	}

	withBOM, err := GenerateReport(strings.NewReader("\xEF\xBB\xBF"+lines), defaultMaxLineBytes, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport (BOM) failed: %v", err)
	}

	if withBOM.MalformedLines != 0 {
		t.Errorf("BOM-prefixed first line counted as malformed: MalformedLines = %d, want 0", withBOM.MalformedLines)
	}
	if withBOM.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2 (BOM must not drop the first request)", withBOM.TotalRequests)
	}
	if !reflect.DeepEqual(plain, withBOM) {
		t.Errorf("report with BOM differs from report without.\nwithBOM: %+v\nplain:   %+v", withBOM, plain)
	}
}

func TestGenerateReport_BOMOnlyStrippedFromFirstLine(t *testing.T) {
	// A BOM in the middle of the input is not a BOM — it's garbage inside a
	// line, and that line must still be counted as malformed.
	input := strings.Join([]string{
		`{"request_id":"a","timestamp":"2024-01-15T10:00:00Z","client_id":"c","endpoint":"/e","status_code":200}`,
		"\xEF\xBB\xBF" + `{"request_id":"b","timestamp":"2024-01-15T10:00:01Z","client_id":"c","endpoint":"/e","status_code":200}`,
	}, "\n")

	report, err := GenerateReport(strings.NewReader(input), defaultMaxLineBytes, false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	if report.TotalRequests != 1 || report.MalformedLines != 1 {
		t.Fatalf("got total=%d malformed=%d, want total=1 malformed=1", report.TotalRequests, report.MalformedLines)
	}
}

func TestRun_ValidFilePathWritesByteExactReportAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"sample_input/requests.jsonl"}, strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	want, err := os.ReadFile("sample_input/expected_output.json")
	if err != nil {
		t.Fatalf("failed to read expected output: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("stdout is not byte-exact with the fixture.\ngot:\n%s\nwant:\n%s", stdout.Bytes(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on success, got: %s", stderr.String())
	}
}

func TestRun_StdinInput(t *testing.T) {
	data, err := os.ReadFile("sample_input/requests.jsonl")
	if err != nil {
		t.Fatalf("failed to read sample input: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run(nil, strings.NewReader(string(data)), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	want, err := os.ReadFile("sample_input/expected_output.json")
	if err != nil {
		t.Fatalf("failed to read expected output: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("stdin report is not byte-exact with the fixture.\ngot:\n%s\nwant:\n%s", stdout.Bytes(), want)
	}

	// An explicit "-" path must behave identically to no argument at all.
	var dashOut, dashErr bytes.Buffer
	if code := run([]string{"-"}, strings.NewReader(string(data)), &dashOut, &dashErr); code != 0 {
		t.Fatalf(`run("-") exit code = %d, want 0 (stderr: %s)`, code, dashErr.String())
	}
	if !bytes.Equal(dashOut.Bytes(), stdout.Bytes()) {
		t.Errorf(`run("-") output differs from the no-argument stdin output`)
	}
}

func TestRun_MissingFileExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{filepath.Join(t.TempDir(), "does-not-exist.jsonl")}, strings.NewReader(""), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.Len() == 0 {
		t.Error("expected a message on stderr for a missing file")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on failure, got: %s", stdout.String())
	}
	if strings.Contains(stderr.String(), "panic") {
		t.Errorf("stderr should be a clean message, not a panic: %s", stderr.String())
	}
}

func TestRun_DirectoryPathExitsOne(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, strings.NewReader(""), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "is a directory") {
		t.Errorf("stderr should say the path is a directory, got: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on failure, got: %s", stdout.String())
	}
}

func TestRun_UnrecognizedFlagExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--bogus-flag"}, strings.NewReader(""), &stdout, &stderr)

	// Specifically 1, not the flag package's own default of 2 — every
	// CLI-level failure in this program exits 1.
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.Len() == 0 {
		t.Error("expected the flag package's error/usage output on stderr")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on failure, got: %s", stdout.String())
	}
}

func TestRun_HelpExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 {
			t.Errorf("run(%q) exit code = %d, want 0 (asking for help is not a failure)", arg, code)
		}
		if stderr.Len() == 0 {
			t.Errorf("run(%q): expected usage output on stderr", arg)
		}
	}
}

func TestRun_NonPositiveMaxLineBytesExitsOne(t *testing.T) {
	for _, arg := range []string{"--max-line-bytes=0", "--max-line-bytes=-5"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{arg, "sample_input/requests.jsonl"}, strings.NewReader(""), &stdout, &stderr)

		if code != 1 {
			t.Errorf("run(%q) exit code = %d, want 1 (must fail closed, not emit an all-malformed report)", arg, code)
		}
		if !strings.Contains(stderr.String(), "--max-line-bytes must be a positive integer") {
			t.Errorf("run(%q): stderr = %q, want a clear --max-line-bytes message", arg, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q): stdout should be empty, got: %s", arg, stdout.String())
		}
	}
}
