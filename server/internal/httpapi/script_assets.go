package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Pictures that ship with a script.
//
// A committed script is a directory — scripts/<name>/script.json plus whatever
// pictures it needs — so the body can point at them with a relative URL and the
// whole thing stays in one place, reviewable in git, independent of the web
// build. The studio keeps uploading runtime images to data/media instead; those
// are authored after publishing and belong to the data directory, not the repo.
//
//	GET /script-assets/<script dir>/<anything>/<file>.<image ext>
const scriptAssetsPrefix = "/script-assets/"

// ScriptAssetURL is the URL a script body should store for a file committed
// under scripts/<dir>/<rel>.
func ScriptAssetURL(dir, rel string) string {
	return scriptAssetsPrefix + strings.Trim(dir, "/") + "/" + strings.TrimLeft(filepath.ToSlash(rel), "/")
}

// handleScriptAsset serves one committed picture.
//
// It is read-only and unauthenticated: a clue card has to render for a signed-in
// player, and these files are public game material anyway. Only image
// extensions are reachable, so script.json (and any other text file that ends
// up in the directory) cannot be read through it.
func (s *Server) handleScriptAsset(w http.ResponseWriter, r *http.Request) {
	dir, rel := r.PathValue("script"), r.PathValue("file")
	if dir == "" || rel == "" || dir == "." || dir == ".." || strings.ContainsAny(dir, `/\`) {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(rel)
	mime, ok := imageExts[strings.ToLower(filepath.Ext(name))]
	if !ok {
		http.NotFound(w, r)
		return
	}
	root := filepath.Clean(s.cfg.ScriptsDir)
	if root == "" || root == "." {
		http.NotFound(w, r)
		return
	}
	scriptDir := filepath.Join(root, dir)
	path := filepath.Join(scriptDir, filepath.Clean("/"+rel))
	if !within(scriptDir, path) {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The name is stable by design: an author who re-crops a map replaces the
	// file and keeps the URL. A stored copy therefore keeps showing the old
	// picture — which is what a player reported after a re-crop — so every hit
	// revalidates instead. ServeContent answers the conditional request with a
	// 304 when nothing changed, so the price is one empty round trip.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
