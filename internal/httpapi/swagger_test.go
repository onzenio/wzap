package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSwaggerIndexServesWithoutCredential(t *testing.T) {
	rec := serve(t, newTestServer(t), http.MethodGet, "/swagger/index.html", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestSwaggerDocJSONServesRoutesAndSecurityDefinition(t *testing.T) {
	rec := serve(t, newTestServer(t), http.MethodGet, "/swagger/doc.json", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var doc struct {
		Paths               map[string]json.RawMessage `json:"paths"`
		SecurityDefinitions map[string]struct {
			Type string `json:"type"`
			Name string `json:"name"`
			In   string `json:"in"`
		} `json:"securityDefinitions"`
	}
	decodeJSON(t, rec.Body.Bytes(), &doc)

	for _, path := range []string{
		"/instances",
		"/instances/{id}/messages/text",
		"/auth/login",
		"/healthz",
		"/media/{id}",
		"/users",
	} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("doc.json is missing path %q", path)
		}
	}
	def, ok := doc.SecurityDefinitions["apikey"]
	if !ok {
		t.Fatal("doc.json is missing securityDefinitions.apikey")
	}
	if def.Type != "apiKey" || def.Name != "apikey" || def.In != "header" {
		t.Errorf("securityDefinitions.apikey = %+v, want {apiKey apikey header}", def)
	}
}

func TestSwaggerMountKeepsGuardedRoutesAuthenticated(t *testing.T) {
	rec := serve(t, newTestServer(t), http.MethodGet, "/instances", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "unauthorized" {
		t.Errorf("error code = %q, want %q", code, "unauthorized")
	}
}

func TestSwaggerUnknownSubpathAsServed(t *testing.T) {
	rec := serve(t, newTestServer(t), http.MethodGet, "/swagger/does-not-exist-xyz", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want the file server's plain-text 404, not the API envelope", ct)
	}
}
