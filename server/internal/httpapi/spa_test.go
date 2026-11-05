package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The built SPA must serve real files with their own content type and fall back
// to index.html only for client-side routes. Missing assets used to come back as
// index.html, which browsers reject as a module script.
func TestSPAHandlerServesAssetsAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	const html = "<!doctype html><div id=root></div>"
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not serve"), 0o644); err != nil {
		t.Fatal(err)
	}

	api := http.NewServeMux()
	api.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})
	h := spaHandler(http.FileServer(http.Dir(dir)), dir, api)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/assets/app.js"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Header().Get("Content-Type"), "javascript") ||
		rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	// Fingerprinted assets are immutable; the shell must never be cached, otherwise
	// it keeps pointing the browser at a stale bundle.
	if cc := get("/assets/app.js").Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("asset Cache-Control = %q", cc)
	}

	if rec := get("/assets/missing.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset should 404, got %d %q", rec.Code, rec.Body.String())
	}

	if rec := get("/lobby/ABC123"); rec.Code != http.StatusOK || rec.Body.String() != html {
		t.Fatalf("spa route = %d %q", rec.Code, rec.Body.String())
	}
	if cc := get("/lobby/ABC123").Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("shell Cache-Control = %q", cc)
	}

	if rec := get("/api/health"); rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("api route = %d %q", rec.Code, rec.Body.String())
	}

	// A traversal attempt must never read a file outside the web root.
	if rec := get("/../secret.txt"); strings.Contains(rec.Body.String(), "do not serve") {
		t.Fatalf("path traversal leaked a file: %q", rec.Body.String())
	}
}
