package webhook

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RED: SSRF gate no webhook outbound — metadata/link-local sempre bloqueados,
// privado só loopback, userinfo rejeitado, redirect para bloqueado não seguido.

func TestValidateURLRejectsSSRFHosts(t *testing.T) {
	old := lookupIP
	t.Cleanup(func() { lookupIP = old })

	for _, raw := range []string{
		"https://169.254.169.254/hook",
		"https://100.100.100.200/hook",
		"https://192.168.1.10/hook",
		"https://10.0.0.5/hook",
		"https://[fe80::1]/hook",
		"https://user:pass@example.com/hook",
	} {
		if _, err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want SSRF rejection", raw)
		}
	}
}

func TestValidateURLRejectsDNSPrivate(t *testing.T) {
	old := lookupIP
	t.Cleanup(func() { lookupIP = old })
	lookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.1.2.3")}, nil
	}
	if _, err := ValidateURL("https://internal.example.com/hook"); err == nil {
		t.Error("ValidateURL(dns->10/8) = nil, want SSRF rejection")
	}
}

func TestDeliverBlocksSSRFWithoutRequest(t *testing.T) {
	r := newReceptor(t, http.StatusOK)
	srv := httptest.NewServer(r.handler())
	t.Cleanup(srv.Close)

	err := Deliver(context.Background(), "http://169.254.169.254/latest/meta-data/", "k", []byte(`{}`))
	if err == nil {
		t.Fatal("Deliver(metadata) = nil, want SSRF block")
	}
	if got := r.requests.Load(); got != 0 {
		t.Errorf("requests = %d, want 0 (SSRF nunca POSTa)", got)
	}
}

func TestDeliverBlocksRedirectToSSRF(t *testing.T) {
	var hits int
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	err := Deliver(context.Background(), redirector.URL, "k", []byte(`{}`))
	if err == nil {
		t.Fatal("Deliver(redirect->metadata) = nil, want block no redirect")
	}
	if hits != 1 {
		t.Errorf("redirector hits = %d, want 1 (redirect bloqueado, sem seguir)", hits)
	}
}
