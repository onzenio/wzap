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

func TestList(t *testing.T) {
	got := List("Cardápio", "Escolha", "Ver", "Obrigado", []ListSection{
		{Title: "Lanches", Rows: []ListRow{
			{Title: "X-Burger", Description: "pão com carne"},
			{Title: "X-Salada"},
		}},
	})
	want := "Cardápio\nEscolha\nLanches:\n• X-Burger — pão com carne\n• X-Salada\n[Ver]\nObrigado"
	if got != want {
		t.Errorf("List(...) = %q, want %q", got, want)
	}
	if empty := List("", "", "", "", nil); empty != "" {
		t.Errorf("List(empty) = %q, want empty for the warn+skip path", empty)
	}
}

func TestListResponse(t *testing.T) {
	if got := ListResponse("Cardápio", "1"); got != "Cardápio\nResposta: 1" {
		t.Errorf("ListResponse(title, id) = %q", got)
	}
	if got := ListResponse("", "pix"); got != "Resposta: pix" {
		t.Errorf("ListResponse(id only) = %q", got)
	}
}

func TestReaction(t *testing.T) {
	if got := Reaction("❤️", "WA-1"); got != "Reagiu com ❤️ à mensagem WA-1" {
		t.Errorf("Reaction(emoji, key) = %q", got)
	}
	if got := Reaction("", "WA-1"); !containsStr(got, "WA-1") {
		t.Errorf("Reaction(removed, key) = %q, want the key linked", got)
	}
}

func TestInteractive(t *testing.T) {
	got := Interactive("", "Pague com PIX", "Loja", []string{"Copiar chave PIX", ""})
	want := "Pague com PIX\n• Copiar chave PIX\nLoja"
	if got != want {
		t.Errorf("Interactive(...) = %q, want %q", got, want)
	}
	if empty := Interactive("", "", "", nil); empty != "" {
		t.Errorf("Interactive(empty) = %q, want empty for the warn+skip path", empty)
	}
}

func TestOrderAndProduct(t *testing.T) {
	if got := Order("123", "Pedido da loja"); got != "Pedido da loja\nPedido 123" {
		t.Errorf("Order(...) = %q", got)
	}
	if got := Product("Camiseta", "algodão"); got != "Camiseta\nalgodão" {
		t.Errorf("Product(...) = %q", got)
	}
	if empty := Product("", ""); empty != "" {
		t.Errorf("Product(empty) = %q, want empty for the warn+skip path", empty)
	}
}

func TestAd(t *testing.T) {
	got := Ad("Metade do preço", "só hoje", "https://loja.example/oferta")
	want := "Metade do preço\nsó hoje\nhttps://loja.example/oferta"
	if got != want {
		t.Errorf("Ad(...) = %q, want %q", got, want)
	}
	if empty := Ad("", "", ""); empty != "" {
		t.Errorf("Ad(empty) = %q, want empty (thumbnail-only still mirrors)", empty)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
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
