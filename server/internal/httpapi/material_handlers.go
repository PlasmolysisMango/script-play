package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"script-play/internal/domain"
	"script-play/internal/store"
)

// Raw material is archived, never parsed.
//
// The server cannot turn a docx, a zip or a pile of photos into a standard
// script: that is a judgement call about chapters, roles and clues, and it
// belongs to a developer who can look at the result. So uploading simply drops
// the original into data/inbox and records where it went; the developer then
// writes the standard JSON and imports it through POST /api/scripts.

// handleListMaterials returns every uploaded original plus the inbox path, so
// the workbench can show an admin exactly where to pick the file up.
func (s *Server) handleListMaterials(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"materials": s.store.ListMaterials(),
		"dir":       s.imp.InboxDir(),
	})
}

// handleUploadMaterials accepts one or more originals as multipart/form-data.
func (s *Server) handleUploadMaterials(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := r.ParseMultipartForm(s.cfg.Limits.MaxUploadBytes); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("解析上传失败：%w", err))
		return
	}
	dir := s.imp.InboxDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, http.StatusInternalServerError, fmt.Errorf("创建收件目录失败：%w", err))
		return
	}
	var saved []*domain.RawMaterial
	for _, key := range []string{"files", "files[]", "file"} {
		for _, fh := range r.MultipartForm.File[key] {
			m, err := s.saveMaterial(dir, u.ID, fh)
			if err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
			saved = append(saved, m)
		}
	}
	if len(saved) == 0 {
		fail(w, http.StatusBadRequest, errors.New("没有收到文件"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"materials": saved, "dir": dir})
}

// saveMaterial copies one upload into the inbox and records it.
//
// The stored name keeps the original extension — a developer opening the folder
// needs to know what each blob is — and gets a timestamp plus a random suffix so
// two files with the same name never collide.
func (s *Server) saveMaterial(dir, uploader string, fh *multipart.FileHeader) (*domain.RawMaterial, error) {
	if fh.Size > s.cfg.Limits.MaxUploadBytes {
		return nil, fmt.Errorf("文件 %s 超过大小限制", fh.Filename)
	}
	original := filepath.Base(filepath.Clean(fh.Filename))
	if original == "." || original == "" {
		return nil, errors.New("文件名不合法")
	}
	stored := time.Now().UTC().Format("20060102T150405") + "-" + store.NewID("raw")
	if ext := strings.ToLower(filepath.Ext(original)); ext != "" {
		stored += ext
	}
	src, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	dst := filepath.Join(dir, stored)
	out, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	written, err := io.Copy(out, src)
	out.Close()
	if err != nil {
		os.Remove(dst)
		return nil, err
	}
	m := &domain.RawMaterial{
		ID:         store.NewID("material"),
		Name:       original,
		Path:       dst,
		MIME:       mimeOf(original, fh.Header.Get("Content-Type")),
		Size:       written,
		UploadedBy: uploader,
		CreatedAt:  time.Now().UTC(),
	}
	s.store.PutMaterial(m)
	return m, nil
}

// handleDeleteMaterial drops the record and the file behind it.
func (s *Server) handleDeleteMaterial(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.GetMaterial(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("素材不存在"))
		return
	}
	// Only ever remove something inside the inbox: the path came from our own
	// store, but a hand-edited state.json must not turn this into rm anywhere.
	if within(s.imp.InboxDir(), m.Path) {
		if err := os.Remove(m.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fail(w, http.StatusInternalServerError, fmt.Errorf("删除文件失败：%w", err))
			return
		}
	}
	if err := s.store.DeleteMaterial(m.ID); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func within(dir, path string) bool {
	root, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return abs != root && strings.HasPrefix(abs, root+string(os.PathSeparator))
}

// mimeOf prefers the extension: browsers happily report octet-stream for docx
// and x-pdf for a pdf.
func mimeOf(name, fallback string) string {
	if v := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); v != "" {
		return v
	}
	if fallback == "" {
		return "application/octet-stream"
	}
	return fallback
}
