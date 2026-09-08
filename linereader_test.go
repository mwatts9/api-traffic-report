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

func TestLineReader_OversizedLineSpanningMultipleChunks(t *testing.T) {
	// Create a line larger than the 64KB bufio.Reader buffer to exercise
	// the multi-chunk continuation path (isPrefix == true loop in next()).
	// This verifies that when a single line spans multiple ReadLine() calls,
	// the bounded memory property is maintained and we correctly skip to the next line.
	const hugeLineSize = 500000 // 500 KB, well beyond 64 KB buffer
	const maxLineBytes = 1000

	// Build the huge line from a simple repeating pattern
	pattern := "x"
	hugeOversizedLine := strings.Repeat(pattern, hugeLineSize)

	// Construct input: oversized line, then a newline, then a normal line
	input := hugeOversizedLine + "\n" + "short line after huge"
	lr := newLineReader(strings.NewReader(input), maxLineBytes)

	// Read the oversized line
	line, oversized, ok := lr.next()
	if !ok {
		t.Fatal("expected a first line")
	}
	if !oversized {
		t.Fatal("expected first line to be flagged oversized")
	}
	if len(line) > maxLineBytes {
		t.Fatalf("oversized line retained %d bytes, want <= %d (memory must stay bounded)", len(line), maxLineBytes)
	}

	// Read the next line to verify the reader correctly recovered after the oversized line.
	// This is the key test: it confirms that the multi-chunk loop in next() correctly
	// kept discarding chunks until it found the newline, and then properly reset
	// to read the subsequent line.
	line2, oversized2, ok2 := lr.next()
	if !ok2 {
		t.Fatal("expected a second line after the oversized one")
	}
	if oversized2 {
		t.Fatal("second line should not be flagged oversized")
	}
	want := "short line after huge"
	if string(line2) != want {
		t.Fatalf("got %q, want %q", line2, want)
	}

	// Verify no read errors
	if err := lr.err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLineReader_OversizedLineRetainsOnlyAGenuinePrefix(t *testing.T) {
	// Sized so the line spans several 64 KB ReadLine chunks and the *final*
	// chunk is small enough that it would still "fit" under maxLineBytes on
	// its own. A reader that keeps appending after the overflow is detected
	// would splice that tail onto the head, returning bytes that are not a
	// contiguous prefix of the line.
	const chunk = 64 * 1024
	const maxLineBytes = 70000
	head := strings.Repeat("a", chunk*3)
	tail := strings.Repeat("b", 3392)

	lr := newLineReader(strings.NewReader(head+tail+"\n"+"short line"), maxLineBytes)

	line, oversized, ok := lr.next()
	if !ok {
		t.Fatal("expected a first line")
	}
	if !oversized {
		t.Fatal("expected the line to be flagged oversized")
	}
	if len(line) > maxLineBytes {
		t.Fatalf("retained %d bytes, want <= %d", len(line), maxLineBytes)
	}
	if strings.Contains(string(line), "b") {
		t.Fatalf("retained bytes include content from the tail of the line — not a genuine prefix (retained %d bytes)", len(line))
	}
	if !strings.HasPrefix(head+tail, string(line)) {
		t.Fatal("retained bytes are not a prefix of the original line")
	}

	line2, oversized2, ok2 := lr.next()
	if !ok2 || oversized2 || string(line2) != "short line" {
		t.Fatalf("next line: got %q oversized=%v ok=%v, want %q", line2, oversized2, ok2, "short line")
	}
}
