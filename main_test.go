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
