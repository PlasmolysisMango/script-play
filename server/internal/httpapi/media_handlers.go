package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"script-play/internal/store"
)

// Script images.
//
// A clue often *is* a picture — a photo of the body, a scan of a letter — so the
// studio uploads the file beside the script and stores a relative URL in the
// body. Relative on purpose: the same script JSON then works in a dev build,
// behind a reverse proxy and in an exported copy.
const (
	mediaRoot = "media"
	// maxImageBytes caps one picture. The body itself is capped at 8 MB, and no
	// clue card should ever carry a file near that.
	maxImageBytes = 8 << 20
)

// imageExts is also the allow list: an upload whose extension is missing here is
// refused, and serving always takes the type from the extension rather than from
// sniffing, so a picture can never be interpreted as a document.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

func (s *Server) mediaDir(scriptID string) string {
	return filepath.Join(s.cfg.DataDir, mediaRoot, scriptID)
}

// handleUploadScriptMedia stores one picture for a script and answers with the
// URL to paste into clue.imageUrl (or into a markdown image link).
func (s *Server) handleUploadScriptMedia(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, err := s.store.GetScript(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("剧本不存在"))
		return
	}
	if !ownsScript(u, rec) {
		fail(w, http.StatusForbidden, errors.New("只有作者可以上传图片"))
		return
	}
	if err := r.ParseMultipartForm(maxImageBytes << 1); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("解析上传失败：%w", err))
		return
	}
	fh := firstFile(r.MultipartForm, "file", "image", "files", "files[]")
	if fh == nil {
		fail(w, http.StatusBadRequest, errors.New("没有收到图片"))
		return
	}
	if fh.Size > maxImageBytes {
		fail(w, http.StatusRequestEntityTooLarge, fmt.Errorf("图片超过 %d MB", maxImageBytes>>20))
		return
	}
	original := filepath.Base(filepath.Clean(fh.Filename))
	ext := strings.ToLower(filepath.Ext(original))
	mime, ok := imageExts[ext]
	if !ok {
		fail(w, http.StatusUnsupportedMediaType, errors.New("只接受 png / jpg / webp / gif 图片"))
		return
	}
	src, err := fh.Open()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	defer src.Close()
	// The extension filters most mistakes; reading the header catches the rest,
	// e.g. a PDF renamed to .png.
	head := make([]byte, 512)
	n, readErr := io.ReadAtLeast(src, head, 1)
	if readErr != nil {
		fail(w, http.StatusBadRequest, errors.New("图片是空的"))
		return
	}
	if sniff := http.DetectContentType(head[:n]); !strings.HasPrefix(sniff, "image/") {
		fail(w, http.StatusUnsupportedMediaType, errors.New("文件内容不是图片"))
		return
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

	dir := s.mediaDir(rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, http.StatusInternalServerError, fmt.Errorf("创建图片目录失败：%w", err))
		return
	}
	stored := time.Now().UTC().Format("20060102T150405") + "-" + store.NewID("img") + ext
	dst, err := os.Create(filepath.Join(dir, stored))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	written, err := io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(filepath.Join(dir, stored))
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"url":  "/media/" + rec.ID + "/" + stored,
		"name": original,
		"size": written,
		"mime": mime,
	})
}

// handleMediaFile serves a stored picture. It is deliberately unauthenticated:
// a player's clue card must render on a phone that is already signed in, and the
// file name carries a random suffix, so the URL is not guessable from the script
// id alone.
func (s *Server) handleMediaFile(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	if id == "" || name == "" || id == "." || id == ".." || name == "." || name == ".." {
		http.NotFound(w, r)
		return
	}
	mime, ok := imageExts[strings.ToLower(filepath.Ext(name))]
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.mediaDir(id), name)
	if !within(filepath.Join(s.cfg.DataDir, mediaRoot), path) {
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
	// Uploads never change under their name, so a phone can keep them for a week.
	w.Header().Set("Cache-Control", "public, max-age=604800")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func firstFile(form *multipart.Form, keys ...string) *multipart.FileHeader {
	if form == nil {
		return nil
	}
	for _, key := range keys {
		if list := form.File[key]; len(list) > 0 {
			return list[0]
		}
	}
	return nil
}
