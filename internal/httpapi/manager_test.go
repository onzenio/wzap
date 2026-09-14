package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"wzap/manager"
)

// The manager console is a public surface: browsers load it without a
// credential and log in through it.
func TestManagerMountIsPublic(t *testing.T) {
	for _, path := range []string{"/manager/", "/manager"} {
		rec := serve(t, newTestServer(t), http.MethodGet, path, "")
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("GET %s without credential = 401, want the public console (or its redirect)", path)
		}
	}
}

func TestManagerServesIndexWhenBuilt(t *testing.T) {
	if !manager.Built() {
		t.Skip("manager static build is not embedded; run pnpm --dir manager build first")
	}
	srv := newTestServer(t)

	rec := serve(t, srv, http.MethodGet, "/manager/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manager/ = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	// Deep links resolve to the app (SPA fallback) so refresh works.
	rec = serve(t, srv, http.MethodGet, "/manager/instances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manager/instances = %d, want 200 (SPA fallback)", rec.Code)
	}
}
