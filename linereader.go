package main

import (
	"bufio"
	"io"
)

// lineReader reads newline-delimited lines from r, guaranteeing that memory
// used per line never exceeds maxLineBytes even if the underlying line is
// arbitrarily large — this is the concrete defense against a pathological or
// adversarial line exhausting memory (see design spec's Security section).
type lineReader struct {
	br      *bufio.Reader
	max     int
	lastErr error
}

// newLineReader wraps r for bounded line-at-a-time reading. maxLineBytes
// caps how many bytes of any single line are retained; excess bytes are
// discarded, not buffered.
func newLineReader(r io.Reader, maxLineBytes int) *lineReader {
	return &lineReader{
		br:  bufio.NewReaderSize(r, 64*1024),
		max: maxLineBytes,
	}
}

// next returns the next line (without its trailing newline). oversized is
// true if the line exceeded maxLineBytes, in which case line holds at most
// maxLineBytes bytes and only a genuine prefix of the line — once the
// overflow is detected no further bytes are retained, so the returned bytes
// are never a splice of non-contiguous parts. ok is false once the input is
// exhausted; check err() afterward to distinguish clean EOF from a real read
// failure.
func (lr *lineReader) next() (line []byte, oversized bool, ok bool) {
	var buf []byte
	for {
		chunk, isPrefix, err := lr.br.ReadLine()
		if len(chunk) > 0 && !oversized {
			if len(buf)+len(chunk) > lr.max {
				oversized = true
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err != nil {
			if err != io.EOF {
				lr.lastErr = err
			}
			if len(buf) == 0 && !oversized {
				return nil, false, false
			}
			return buf, oversized, true
		}
		if !isPrefix {
			return buf, oversized, true
		}
	}
}

// err returns the first non-EOF read error encountered, if any.
func (lr *lineReader) err() error {
	return lr.lastErr
}
