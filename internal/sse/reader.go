// Package sse decodes bounded SSE events, independently of the JSON protocol.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

const MaxEventBytes = 128 << 10

var ErrLimit = errors.New("sse_event_limit")

type Event struct{ Type, Data string }
type Reader struct {
	scanner *bufio.Scanner
	first   bool
}

func NewReader(input io.Reader) *Reader {
	s := bufio.NewScanner(input)
	s.Buffer(make([]byte, 4096), MaxEventBytes+2)
	s.Split(splitLine)
	return &Reader{scanner: s, first: true}
}

// Next dispatches only complete, blank-line-terminated data events. Partial
// events at EOF are never interpreted as successful completion. Comments and
// unknown fields are bounded too, so metadata cannot inflate one event.
func (r *Reader) Next() (Event, error) {
	var data []string
	kind := "message"
	size := 0
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if r.first {
			line = strings.TrimPrefix(line, "\uFEFF")
			r.first = false
		}
		if line == "" {
			if len(data) > 0 {
				return Event{Type: kind, Data: strings.Join(data, "\n")}, nil
			}
			size = 0
			kind = "message"
			continue
		}
		size += len(line) + 1
		if size > MaxEventBytes {
			return Event{}, ErrLimit
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			kind = value
		}
	}
	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return Event{}, ErrLimit
		}
		return Event{}, err
	}
	if size > 0 {
		return Event{}, io.ErrUnexpectedEOF
	}
	return Event{}, io.EOF
}

// SSE allows LF, CR and CRLF. A CR at the buffer boundary waits for the next
// byte, preventing an artificial empty event when CRLF crosses reads.
func splitLine(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		advance := i + 1
		if data[i] == '\r' {
			if advance == len(data) && !atEOF {
				return 0, nil, nil
			}
			if advance < len(data) && data[advance] == '\n' {
				advance++
			}
		}
		return advance, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
