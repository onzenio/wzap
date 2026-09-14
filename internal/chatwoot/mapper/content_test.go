// Package mapper holds pure WhatsApp <-> Chatwoot content mappings.
//
// No I/O here: every function is a pure table/format conversion so the
// mirror worker (Task 6) can consume it without side effects.
package mapper

import "testing"

func TestMarkdownToChatwoot(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bold", "*hello*", "**hello**"},
		{"italic", "_hello_", "*hello*"},
		{"strike", "~hello~", "~~hello~~"},
		{"mixed", "*bold* and _italic_ and ~strike~", "**bold** and *italic* and ~~strike~~"},
		{"plain", "no formatting here", "no formatting here"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkdownToChatwoot(tc.in); got != tc.want {
				t.Errorf("MarkdownToChatwoot(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownToWhatsApp(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bold", "**hello**", "*hello*"},
		{"italic star", "*hello*", "_hello_"},
		{"italic underscore", "_hello_", "_hello_"},
		{"strike", "~~hello~~", "~hello~"},
		{"mixed", "**bold** and *italic* and ~~strike~~", "*bold* and _italic_ and ~strike~"},
		{"plain", "no formatting here", "no formatting here"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkdownToWhatsApp(tc.in); got != tc.want {
				t.Errorf("MarkdownToWhatsApp(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	inputs := []string{"*bold*", "_italic_", "~strike~", "*b* _i_ ~s~"}
	for _, in := range inputs {
		if got := MarkdownToWhatsApp(MarkdownToChatwoot(in)); got != in {
			t.Errorf("round trip(%q) = %q", in, got)
		}
	}
}

func TestText(t *testing.T) {
	cases := []struct {
		name string
		in   TextInput
		want string
	}{
		{"body wins", TextInput{Body: "hi", Caption: "cap"}, "hi"},
		{"caption fallback", TextInput{Caption: "cap"}, "cap"},
		{"empty", TextInput{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Errorf("Text(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLocation(t *testing.T) {
	got := Location(-23.55052, -46.633308, "Se", "Praca da Se, Sao Paulo")
	want := "Se\nPraca da Se, Sao Paulo\nhttps://maps.google.com/?q=-23.55052,-46.633308"
	if got != want {
		t.Errorf("Location(...) = %q, want %q", got, want)
	}

	bare := Location(-23.55052, -46.633308, "", "")
	wantBare := "https://maps.google.com/?q=-23.55052,-46.633308"
	if bare != wantBare {
		t.Errorf("Location(bare) = %q, want %q", bare, wantBare)
	}
}

func TestContact(t *testing.T) {
	got := Contact("Ada", []string{"+5511999999999", "+5511888888888"})
	want := "Ada\n+5511999999999\n+5511888888888"
	if got != want {
		t.Errorf("Contact(...) = %q, want %q", got, want)
	}

	nameless := Contact("", []string{"+5511999999999"})
	if nameless != "+5511999999999" {
		t.Errorf("Contact(nameless) = %q", nameless)
	}
}

func TestGroupPrefix(t *testing.T) {
	withBody := GroupPrefix("5511999999999", "Ada", "hello")
	wantWith := "5511999999999 \u2014 Ada\nhello"
	if withBody != wantWith {
		t.Errorf("GroupPrefix(with body) = %q, want %q", withBody, wantWith)
	}

	withoutBody := GroupPrefix("5511999999999", "Ada", "")
	wantWithout := "5511999999999 \u2014 Ada"
	if withoutBody != wantWithout {
		t.Errorf("GroupPrefix(without body) = %q, want %q", withoutBody, wantWithout)
	}
}

func TestAttachmentKind(t *testing.T) {
	cases := []struct {
		name      string
		mime      string
		ext       string
		wantKind  string
		wantAsDoc bool
	}{
		{"audio", "audio/ogg; codecs=opus", ".ogg", "audio", false},
		{"image", "image/jpeg", ".jpg", "image", false},
		{"video", "video/mp4", ".mp4", "video", false},
		{"svg mime", "image/svg+xml", ".svg", "file", true},
		{"svg ext", "image/png", ".svg", "file", true},
		{"pdf", "application/pdf", ".pdf", "file", true},
		{"unknown", "application/octet-stream", ".bin", "file", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, asDoc := AttachmentKind(tc.mime, tc.ext)
			if kind != tc.wantKind || asDoc != tc.wantAsDoc {
				t.Errorf("AttachmentKind(%q, %q) = (%q, %v), want (%q, %v)",
					tc.mime, tc.ext, kind, asDoc, tc.wantKind, tc.wantAsDoc)
			}
		})
	}
}
