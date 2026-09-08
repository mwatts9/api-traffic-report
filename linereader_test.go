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
