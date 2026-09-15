package manager

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func fixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<html>wzap manager</html>")},
		"app.js":                 {Data: []byte("console.log(1)")},
		"assets/style.css":       {Data: []byte("body{}")},
		"assets/logo.svg":        {Data: []byte("<svg></svg>")},
		"favicon.ico":            {Data: []byte("ico")},
		".gitkeep":               {Data: []byte("")},
		"nested/.hidden":         {Data: []byte("x")},
		"nested/page/index.html": {Data: []byte("<html>nested</html>")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesIndexAtRoot(t *testing.T) {
	rec := get(t, handler(fixtureFS()), "/manager/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manager/ = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "wzap manager") {
		t.Errorf("body does not contain the embedded index: %q", rec.Body.String())
	}
}

func TestRedirectsBareManagerPath(t *testing.T) {
	rec := get(t, handler(fixtureFS()), "/manager")
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusFound {
		t.Fatalf("GET /manager = %d, want a redirect to /manager/", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/manager/" {
		t.Errorf("Location = %q, want /manager/", loc)
	}
}

func TestSPAFallbackForDeepLinks(t *testing.T) {
	h := handler(fixtureFS())
	for _, path := range []string{"/manager/instances", "/manager/login?next=/instances", "/manager/accounts/"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (SPA fallback)", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "wzap manager") {
			t.Errorf("GET %s did not fall back to the index: %q", path, rec.Body.String())
		}
	}
}

func TestServesAssetsWithContentType(t *testing.T) {
	h := handler(fixtureFS())
	tests := []struct{ path, wantCT string }{
		{"/manager/app.js", "text/javascript"},
		{"/manager/assets/style.css", "text/css"},
		{"/manager/assets/logo.svg", "image/svg+xml"},
	}
	for _, tt := range tests {
		rec := get(t, h, tt.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tt.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.wantCT) {
			t.Errorf("GET %s Content-Type = %q, want prefix %q", tt.path, ct, tt.wantCT)
		}
	}
}

func TestMissingAssetIs404NotFallback(t *testing.T) {
	rec := get(t, handler(fixtureFS()), "/manager/_nuxt/does-not-exist.abc123.js")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET missing hashed asset = %d, want 404 (must not fall back to HTML)", rec.Code)
	}
}

func TestDotfilesAreNotServed(t *testing.T) {
	h := handler(fixtureFS())
	for _, path := range []string{"/manager/.gitkeep", "/manager/nested/.hidden"} {
		rec := get(t, h, path)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want it hidden", path)
		}
	}
}

func TestPathTraversalIs404(t *testing.T) {
	rec := get(t, handler(fixtureFS()), "/manager/../manager/app.js")
	if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("traversal escaped the console root: %q", rec.Body.String())
	}
}

func TestUnbuiltDistIsServiceUnavailable(t *testing.T) {
	h := handler(fstest.MapFS{})
	rec := get(t, h, "/manager/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /manager/ without a build = %d, want 503", rec.Code)
	}
}

func TestEmbeddedDist(t *testing.T) {
	if !Built() {
		t.Skip("manager static build is not embedded; run pnpm --dir manager build first")
	}
	h := Handler()
	rec := get(t, h, "/manager/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manager/ on the embedded build = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// Regression guard: the Nuxt bundle lives under `_nuxt/`, which plain
	// `go:embed` silently drops (underscore prefix). Without it the console
	// loads index.html but renders a blank page (all JS/CSS 404).
	var bundle string
	_ = fs.WalkDir(mustDistFS(t), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || bundle != "" {
			return nil
		}
		if strings.HasPrefix(p, "_nuxt/") && strings.HasSuffix(p, ".js") {
			bundle = p
		}
		return nil
	})
	if bundle == "" {
		t.Fatal("embedded build holds no _nuxt/*.js bundle; the console would render blank (check the go:embed all: prefix)")
	}
	rec = get(t, h, "/manager/"+bundle)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manager/%s = %d, want 200", bundle, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("GET /manager/%s Content-Type = %q, want text/javascript", bundle, ct)
	}
}

func mustDistFS(t *testing.T) fs.FS {
	t.Helper()
	root := distFS()
	if root == nil {
		t.Fatal("embedded dist is missing")
	}
	return root
}
