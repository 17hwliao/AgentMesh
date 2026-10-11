package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type byteReader struct{ text string }

func (b *byteReader) Read(p []byte) (int, error) {
	if b.text == "" {
		return 0, io.EOF
	}
	p[0] = b.text[0]
	b.text = b.text[1:]
	return 1, nil
}

func TestEventFraming(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		r := NewReader(&byteReader{text: strings.ReplaceAll("\uFEFF: heartbeat\n\nevent: message\nid: ignored\ndata: {\ndata: \"text\":\"中文\"}\n\n: ping\n\ndata:[DONE]\n\n", "\n", newline)})
		first, err := r.Next()
		if err != nil || first.Data != "{\n\"text\":\"中文\"}" {
			t.Fatalf("%q event=%+v err=%v", newline, first, err)
		}
		last, err := r.Next()
		if err != nil || last.Data != "[DONE]" {
			t.Fatal(last, err)
		}
		if _, err = r.Next(); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	}
}

func TestTruncatedAndBoundedEvents(t *testing.T) {
	for _, text := range []string{"data:[DONE]", "data:[DONE]\n", "event:error\n", "data:{\ndata:\"answer\":1}\n"} {
		if _, err := NewReader(strings.NewReader(text)).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated event accepted: %v", err)
		}
	}
	for _, text := range []string{"data:" + strings.Repeat("x", MaxEventBytes) + "\n\n", strings.Repeat(":meta\n", MaxEventBytes/5) + "\n"} {
		if _, err := NewReader(strings.NewReader(text)).Next(); !errors.Is(err, ErrLimit) {
			t.Fatalf("unbounded event accepted: %v", err)
		}
	}
}

func FuzzEventReader(f *testing.F) {
	f.Add("data: ok\n\ndata:[DONE]\n\n")
	f.Add("data:{\n")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 2*MaxEventBytes {
			return
		}
		r := NewReader(strings.NewReader(text))
		for i := 0; i <= len(text)+1; i++ {
			event, err := r.Next()
			if len(event.Data) > MaxEventBytes {
				t.Fatal("event exceeded bound")
			}
			if err != nil {
				return
			}
		}
		t.Fatal("reader failed to make progress")
	})
}
