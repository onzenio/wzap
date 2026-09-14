package webhook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCutRawForLimit(t *testing.T) {
	big := strings.Repeat("a", 300)
	small := "hi"

	tests := []struct {
		name      string
		raw       string
		useNil    bool
		limit     int64
		wantCut   bool
		wantExact string
		check     func(t *testing.T, out []byte)
	}{
		{
			name:    "nil passthrough",
			useNil:  true,
			limit:   16,
			wantCut: false,
			check: func(t *testing.T, out []byte) {
				t.Helper()
				if out != nil {
					t.Errorf("out = %q, want nil", out)
				}
			},
		},
		{
			name:      "empty passthrough",
			raw:       "",
			limit:     16,
			wantCut:   false,
			wantExact: "",
		},
		{
			name:      "non-JSON passthrough",
			raw:       "not json{{{",
			limit:     16,
			wantCut:   false,
			wantExact: "not json{{{",
		},
		{
			name:      "small strings kept",
			raw:       `{"text":"hi","n":42,"ok":true,"nothing":null}`,
			limit:     16,
			wantCut:   false,
			wantExact: `{"n":42,"nothing":null,"ok":true,"text":"hi"}`,
		},
		{
			name:    "nested blobs cut with marker and size",
			raw:     `{"text":"hi","data":{"blob":"` + big + `"},"list":["ok","` + big + `"],"n":7}`,
			limit:   16,
			wantCut: true,
			check: func(t *testing.T, out []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("Unmarshal out: %v", err)
				}
				if got["text"] != small {
					t.Errorf("text = %v, want %q (small strings kept)", got["text"], small)
				}
				if got["n"] != float64(7) {
					t.Errorf("n = %v, want 7 (non-strings preserved)", got["n"])
				}
				data, ok := got["data"].(map[string]any)
				if !ok {
					t.Fatalf("data = %v, want an object", got["data"])
				}
				assertMarker(t, data["blob"], len(big))
				list, ok := got["list"].([]any)
				if !ok || len(list) != 2 {
					t.Fatalf("list = %v, want 2 items", got["list"])
				}
				if list[0] != "ok" {
					t.Errorf("list[0] = %v, want %q", list[0], "ok")
				}
				assertMarker(t, list[1], len(big))
			},
		},
		{
			name:    "exact limit kept",
			raw:     `{"v":"` + strings.Repeat("b", 16) + `"}`,
			limit:   16,
			wantCut: false,
			check: func(t *testing.T, out []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("Unmarshal out: %v", err)
				}
				if got["v"] != strings.Repeat("b", 16) {
					t.Errorf("v was cut at exactly the limit, want it kept")
				}
			},
		},
		{
			name:    "one over limit cut",
			raw:     `{"v":"` + strings.Repeat("b", 17) + `"}`,
			limit:   16,
			wantCut: true,
			check: func(t *testing.T, out []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("Unmarshal out: %v", err)
				}
				assertMarker(t, got["v"], 17)
			},
		},
		{
			name:    "marker records byte length",
			raw:     `{"v":"é"}`,
			limit:   1,
			wantCut: true,
			check: func(t *testing.T, out []byte) {
				t.Helper()
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("Unmarshal out: %v", err)
				}
				// "é" is 2 bytes in UTF-8: the cut line is string length,
				// not rune count or base64-decoded size.
				assertMarker(t, got["v"], 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			if !tt.useNil {
				raw = []byte(tt.raw)
			}
			out, cut := CutRawForLimit(raw, tt.limit)
			if cut != tt.wantCut {
				t.Errorf("cut = %v, want %v", cut, tt.wantCut)
			}
			if tt.check == nil && string(out) != tt.wantExact {
				t.Errorf("out = %q, want %q", out, tt.wantExact)
			}
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
}

// assertMarker checks the omission marker object shape and its recorded size.
func assertMarker(t *testing.T, value any, wantSize int) {
	t.Helper()
	marker, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %v (%T), want the omission marker object", value, value)
	}
	if marker["omitted"] != true {
		t.Errorf("omitted = %v, want true", marker["omitted"])
	}
	size, ok := marker["size_bytes"].(float64)
	if !ok {
		t.Fatalf("size_bytes = %v (%T), want a number", marker["size_bytes"], marker["size_bytes"])
	}
	if int(size) != wantSize {
		t.Errorf("size_bytes = %v, want %d (original string length)", size, wantSize)
	}
	if len(marker) != 2 {
		t.Errorf("marker has %d keys, want exactly omitted+size_bytes: %v", len(marker), marker)
	}
}
