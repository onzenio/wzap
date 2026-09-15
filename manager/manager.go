// Package manager serves the embedded wzap manager console under /manager/.
//
// The console is a static Nuxt build: `pnpm --dir manager build` (which runs
// `nuxt generate`) renders it into manager/.output/public, and the files are
// embedded into the Go binary below so the service stays a single artifact.
// A placeholder (manager/.output/public/.gitkeep, force-added despite the
// ignored .output tree) keeps the embed pattern valid on fresh clones without
// a Node build; without generated files the handler answers 503 and the
// Dockerfile multi-stage build guarantees the image always embeds the real
// console.
package manager

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// The `all:` prefix is load-bearing: plain `go:embed` silently skips
// files and directories starting with "_" or ".", which would drop the
// whole `_nuxt/` bundle (JS/CSS) and leave a blank page. Dotfiles stay
// blocked at serve time by isHidden.
//
//go:embed all:.output/public
var dist embed.FS

const (
	publicDir = ".output/public"
	indexFile = "index.html"
)

// distFS returns the embedded public directory rooted at its content, or nil
// when the embed holds nothing usable.
func distFS() fs.FS {
	sub, err := fs.Sub(dist, publicDir)
	if err != nil {
		return nil
	}
	return sub
}

// Built reports whether the embedded console holds a generated index. Fresh
// clones without a `pnpm --dir manager build` report false and serve 503.
func Built() bool {
	root := distFS()
	if root == nil {
		return false
	}
	info, err := fs.Stat(root, indexFile)
	return err == nil && !info.IsDir()
}

// Handler serves the embedded console with SPA fallback: exact files win,
// extensionless deep links resolve to index.html so refresh works, missing
// hashed assets answer 404, and dotfiles are never served. Without a
// generated build embedded it answers 503.
func Handler() http.Handler {
	root := distFS()
	if root == nil {
		return serviceUnavailable()
	}
	return handler(root)
}

// contentTypes maps generated-asset extensions to the exact Content-Type the
// browser needs. http.ServeContent sniffs .js as text/plain, which breaks
// module scripts, so the console sets types explicitly.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".ico":         "image/x-icon",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".webp":        "image/webp",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".txt":         "text/plain; charset=utf-8",
}

func serviceUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "manager console is not built into this binary", http.StatusServiceUnavailable)
	})
}

func handler(root fs.FS) http.Handler {
	if root == nil {
		return serviceUnavailable()
	}
	if info, err := fs.Stat(root, indexFile); err != nil || info.IsDir() {
		return serviceUnavailable()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manager" {
			http.Redirect(w, r, "/manager/", http.StatusMovedPermanently)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/manager/")
		rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
		if rel == "" {
			serveFile(w, r, root, indexFile)
			return
		}
		if !fs.ValidPath(rel) || isHidden(rel) {
			http.NotFound(w, r)
			return
		}
		if info, err := fs.Stat(root, rel); err == nil && !info.IsDir() {
			serveFile(w, r, root, rel)
			return
		}
		// Missing hashed assets (with an extension) are 404: falling back to
		// HTML would serve the app shell as a script or style.
		if path.Ext(rel) != "" {
			http.NotFound(w, r)
			return
		}
		serveFile(w, r, root, indexFile)
	})
}

// isHidden reports whether any segment of rel starts with a dot (placeholders
// such as .gitkeep, never generated console files).
func isHidden(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if strings.HasPrefix(segment, ".") && segment != "." && segment != ".." {
			return true
		}
	}
	return false
}

func serveFile(w http.ResponseWriter, r *http.Request, root fs.FS, name string) {
	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
}
