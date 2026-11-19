package privacy

import (
	"errors"
	"strings"
	"testing"
)

func TestPlaceholdersAreRequestScoped(t *testing.T) {
	request, err := NewPlaceholders()
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewPlaceholders()
	if err != nil {
		t.Fatal(err)
	}
	text := "example example"
	spans := []Span{{Start: 0, End: 7}, {Start: 8, End: 15}}
	masked, err := request.Mask(text, spans)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(masked)
	if len(parts) != 2 || parts[0] != parts[1] || strings.Contains(masked, "example") {
		t.Fatalf("mask output = %q", masked)
	}
	got, err := request.RestoreDisplayText(masked, true)
	if err != nil || got != text {
		t.Fatalf("restore = %q, %v", got, err)
	}
	got, err = other.RestoreDisplayText(masked, false)
	if err != nil || got != masked {
		t.Fatalf("unknown placeholder changed: %q, %v", got, err)
	}
	if _, err := other.RestoreDisplayText(masked, true); !errors.Is(err, ErrMarker) {
		t.Fatalf("foreign placeholder error = %v", err)
	}
	if _, err := request.Mask(masked, nil); !errors.Is(err, ErrInput) {
		t.Fatalf("reserved marker input error = %v", err)
	}
	request.Clear()
	if _, err := request.RestoreDisplayText(masked, true); !errors.Is(err, ErrMarker) {
		t.Fatalf("cleared placeholder error = %v", err)
	}
}

func TestPlaceholdersRejectInvalidInput(t *testing.T) {
	request, err := NewPlaceholders()
	if err != nil {
		t.Fatal(err)
	}
	for _, spans := range [][]Span{
		{{Start: 0, End: 2}},
		{{Start: 1, End: 6}},
		{{Start: 0, End: 3}, {Start: 0, End: 3}},
		{{Start: 0, End: 99}},
	} {
		if _, err := request.Mask("数据", spans); !errors.Is(err, ErrInput) {
			t.Fatalf("spans %v produced %v", spans, err)
		}
	}
	if _, err := request.Mask(strings.Repeat("a", maxTextBytes+1), nil); !errors.Is(err, ErrInput) {
		t.Fatalf("oversize input error = %v", err)
	}
	if _, err := request.RestoreDisplayText(markerPrefix+"truncated", true); !errors.Is(err, ErrMarker) {
		t.Fatalf("malformed placeholder error = %v", err)
	}
}
