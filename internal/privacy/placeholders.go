package privacy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	markerPrefix = "{{AGM_PRIV_"
	maxSpans     = 128
	maxTextBytes = 256 << 10
	maxSpanBytes = 4 << 10
)

var (
	ErrInput     = errors.New("privacy_invalid_input")
	ErrMarker    = errors.New("privacy_marker_unknown")
	markerSyntax = regexp.MustCompile(`\{\{AGM_PRIV_[0-9a-f]{32}_[0-9]{1,3}\}\}`)
)

type Span struct {
	Start int
	End   int
}

type Placeholders struct {
	prefix   string
	byValue  map[string]string
	byMarker map[string]string
}

func NewPlaceholders() (*Placeholders, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &Placeholders{
		prefix:  markerPrefix + hex.EncodeToString(nonce[:]) + "_",
		byValue: make(map[string]string), byMarker: make(map[string]string),
	}, nil
}

func (p *Placeholders) Mask(text string, spans []Span) (string, error) {
	if p.prefix == "" || len(text) > maxTextBytes || len(spans) > maxSpans || !utf8.ValidString(text) || strings.Contains(text, markerPrefix) {
		return "", ErrInput
	}
	pending := make(map[string]string)
	var out strings.Builder
	cursor := 0
	for _, span := range spans {
		if span.Start < cursor || span.End <= span.Start || span.End > len(text) || span.End-span.Start > maxSpanBytes {
			return "", ErrInput
		}
		if !utf8.RuneStart(text[span.Start]) || (span.End < len(text) && !utf8.RuneStart(text[span.End])) {
			return "", ErrInput
		}
		value := text[span.Start:span.End]
		marker := p.byValue[value]
		if marker == "" {
			marker = pending[value]
		}
		if marker == "" {
			index := len(p.byValue) + len(pending)
			if index >= maxSpans {
				return "", ErrInput
			}
			marker = p.prefix + strconv.Itoa(index) + "}}"
			pending[strings.Clone(value)] = marker
		}
		out.WriteString(text[cursor:span.Start])
		out.WriteString(marker)
		cursor = span.End
	}
	out.WriteString(text[cursor:])
	if out.Len() > maxTextBytes {
		return "", ErrInput
	}
	for value, marker := range pending {
		p.byValue[value], p.byMarker[marker] = marker, value
	}
	return out.String(), nil
}

func (p *Placeholders) RestoreDisplayText(text string, strict bool) (string, error) {
	if len(text) > maxTextBytes || !utf8.ValidString(text) {
		return "", ErrInput
	}
	var out strings.Builder
	cursor := 0
	for _, marker := range markerSyntax.FindAllStringIndex(text, -1) {
		before := text[cursor:marker[0]]
		raw := text[marker[0]:marker[1]]
		value, ok := p.byMarker[raw]
		if strict && (!ok || strings.Contains(before, markerPrefix)) {
			return "", ErrMarker
		}
		if !ok {
			value = raw
		}
		out.WriteString(before)
		out.WriteString(value)
		if out.Len() > maxTextBytes {
			return "", ErrInput
		}
		cursor = marker[1]
	}
	tail := text[cursor:]
	if strict && strings.Contains(tail, markerPrefix) {
		return "", ErrMarker
	}
	if out.Len()+len(tail) > maxTextBytes {
		return "", ErrInput
	}
	out.WriteString(tail)
	return out.String(), nil
}

func (p *Placeholders) Clear() {
	p.prefix = ""
	clear(p.byValue)
	clear(p.byMarker)
}
