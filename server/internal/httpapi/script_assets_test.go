package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"script-play/internal/config"
)

// scriptAssetServer wires only the asset route: the handler reads nothing but
// the config, so the rest of the API is noise here.
func scriptAssetServer(t *testing.T, root string) *http.ServeMux {
	t.Helper()
	s := &Server{cfg: &config.Config{ScriptsDir: root}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /script-assets/{script}/{file...}", s.handleScriptAsset)
	return mux
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// A committed script carries its pictures with it, so the body can point at a
// relative URL that survives a rebuild of the web app.
func TestScriptAssetServesCommittedImage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "K7-星火号", "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	png := tinyPNG(t)
	if err := os.WriteFile(filepath.Join(dir, "starfire-map.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	h := scriptAssetServer(t, root)

	// Chinese directory names arrive percent-encoded from a browser; the mux
	// hands the handler the decoded segment.
	rec := get(t, h, ScriptAssetURL("K7-星火号", "images/starfire-map.png"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("content-type = %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
	if rec.Body.Len() != len(png) {
		t.Errorf("body = %d bytes, want %d", rec.Body.Len(), len(png))
	}
}

// The directory also holds script.json. Only image extensions are reachable, so
// the JSON can never be downloaded through the asset route.
func TestScriptAssetRefusesNonImageFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "K7-星火号")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.json"), []byte(`{"title":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := scriptAssetServer(t, root)

	for _, target := range []string{
		ScriptAssetURL("K7-星火号", "script.json"),
		ScriptAssetURL("K7-星火号", "images/missing.png"),
		ScriptAssetURL("K11-再见苏西", "images/nothing.jpg"),
	} {
		if rec := get(t, h, target); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, rec.Code)
		}
	}
}

// The handler is reachable with a decoded ".." segment if the mux ever lets one
// through, so it guards the path itself instead of trusting the URL.
func TestScriptAssetRefusesTraversal(t *testing.T) {
	root := t.TempDir()
	scriptDir := filepath.Join(root, "K7-星火号")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.png")
	if err := os.WriteFile(secret, tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: &config.Config{ScriptsDir: root}}
	req := httptest.NewRequest(http.MethodGet, "/script-assets/x/y.png", nil)
	req.SetPathValue("script", "K7-星火号")
	req.SetPathValue("file", "../secret.png")
	rec := httptest.NewRecorder()
	s.handleScriptAsset(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal = %d, want 404", rec.Code)
	}
}

// A missing picture inside a single-page app must be a 404, not the app shell:
// answering with index.html turns a broken image into a silent white screen.
func TestScriptAssetIsNotSwallowedByTheSPAFallback(t *testing.T) {
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html>shell"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := scriptAssetServer(t, t.TempDir())
	h := spaHandler(http.FileServer(http.Dir(web)), web, api)

	rec := get(t, h, ScriptAssetURL("K7-星火号", "images/missing.png"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "shell") {
		t.Fatal("the app shell was served for a missing image")
	}

	// A deep link still falls back to the shell.
	if rec := get(t, h, "/lobby/room/ABC123"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "shell") {
		t.Fatalf("deep link = %d", rec.Code)
	}
}
