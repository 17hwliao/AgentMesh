package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

func imageFixture(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 32))); e != nil {
		t.Fatal(e)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}
func TestMixedHistoryRoundTripAndInputBound(t *testing.T) {
	msgs := []Message{{Role: "system", Content: "keep system"}, {Role: "user", Parts: []ContentPart{{Type: "text", Text: "图？"}, {Type: "image_url", ImageURL: &ImageURL{URL: imageFixture(t), Detail: "low"}}}}, {Role: "assistant", Content: "old answer"}, {Role: "user", Content: "再看这张图"}}
	if ValidateMessages(msgs) != nil || !HasImages(msgs) {
		t.Fatal("valid image history rejected")
	}
	raw, e := json.Marshal(msgs)
	if e != nil {
		t.Fatal(e)
	}
	var got []Message
	if json.Unmarshal(raw, &got) != nil || len(got) != 4 || got[1].Parts[1].ImageURL.URL != msgs[1].Parts[1].ImageURL.URL || got[2].Content != "old answer" || got[3].Role != "user" {
		t.Fatal("history lost")
	}
	if InputBound(msgs) < ImageInputBound || InputBound(msgs) > ImageInputBound+1000 {
		t.Fatal("image bytes treated as tokens")
	}
}
func TestImageAndContextAdmissionRejectsUnsupportedInputs(t *testing.T) {
	for _, url := range []string{"file:///tmp/a.png", "http://example.com/a.png", "https://localhost/a.png", "https://127.0.0.1/a.png", "https://169.254.169.254/a", "https://10.1.2.3/a", "https://a.internal/a", "https://user:secret@example.com/a", "https://example.com:8443/a", "data:image/svg+xml;base64,PHN2Zy8+", "data:image/png;base64,aW52YWxpZA=="} {
		if ValidateMessages([]Message{{Role: "user", Parts: []ContentPart{{Type: "image_url", ImageURL: &ImageURL{URL: url}}}}}) == nil {
			t.Fatal("invalid image accepted", url)
		}
	}
	for _, raw := range []string{`{"role":"user","content":null}`, `{"role":"user","content":[]}`, `{"role":"user","content":[{"type":"text","text":"x","evil":true}]}`, `{"role":"user","content":"x","unimplemented_field":[]}`} {
		var m Message
		if json.Unmarshal([]byte(raw), &m) == nil {
			t.Fatal("unknown message accepted", raw)
		}
	}
	for _, m := range []Message{{Role: "tool", Content: "x"}, {Role: "user", Content: strings.Repeat("x", MaxTextBytes)}, {Role: "assistant", Parts: []ContentPart{{Type: "image_url", ImageURL: &ImageURL{URL: imageFixture(t)}}}}, {Role: "user", Parts: []ContentPart{{Type: "input_audio"}}}} {
		if ValidateMessages([]Message{m}) == nil {
			t.Fatal("invalid context accepted")
		}
	}
	parts := make([]ContentPart, 4)
	for i := range parts {
		parts[i] = ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: imageFixture(t)}}
	}
	if ValidateMessages([]Message{{Role: "user", Parts: parts}}) == nil {
		t.Fatal("image count not bounded")
	}
}
