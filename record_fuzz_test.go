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
