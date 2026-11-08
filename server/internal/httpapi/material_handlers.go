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

// materialRequestFactor caps a whole request at a multiple of the per-file
// limit: one upload is usually a whole game folder, i.e. several PDFs or a
// zip plus scans. nginx's client_max_body_size must be at least as large, or a
// big request dies with a bare nginx 413 before this handler ever runs.
const materialRequestFactor = 2

// errMaterialTooLarge marks an upload that would exceed the per-file cap. It is
// a 413, not a 400: the body was fine, it was simply too big.
type errMaterialTooLarge struct {
	name string
	mb   int64
}

func (e *errMaterialTooLarge) Error() string {
	return fmt.Sprintf("文件 %s 超过 %d MB 上限，请压缩或拆分后再传", e.name, e.mb)
}

// uploadErrorStatus maps a read failure to the status the client deserves.
func uploadErrorStatus(err error) int {
	var tooLarge *errMaterialTooLarge
	var bodyLimit *http.MaxBytesError
	if errors.As(err, &tooLarge) || errors.As(err, &bodyLimit) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// handleListMaterials returns every uploaded original plus the inbox path, so
// the workbench can show an admin exactly where to pick the file up. The size
// cap travels with the list so the UI can warn before a slow upload starts.
func (s *Server) handleListMaterials(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"materials":   s.store.ListMaterials(),
		"dir":         s.imp.InboxDir(),
		"maxUploadMB": s.cfg.Limits.MaxUploadBytes >> 20,
	})
}

// handleUploadMaterials accepts one or more originals as multipart/form-data.
//
// Parts are streamed straight into the inbox rather than going through
// ParseMultipartForm: that helper's argument is the *in-memory* budget, not a
// size limit, and every byte beyond it is spooled into os.TempDir — tmpfs on
// this box — so a large bundle would sit in RAM and then be copied a second
// time. Streaming also lets one part be cut off mid-copy at the cap, and the
// body itself is bounded by MaxBytesReader instead of trusting the proxy.
func (s *Server) handleUploadMaterials(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	limit := s.cfg.Limits.MaxUploadBytes
	r.Body = http.MaxBytesReader(w, r.Body, limit*materialRequestFactor)
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("解析上传失败：%w", err))
		return
	}
	dir := s.imp.InboxDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, http.StatusInternalServerError, fmt.Errorf("创建收件目录失败：%w", err))
		return
	}
	var saved []*domain.RawMaterial
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.rollbackMaterials(saved)
			fail(w, uploadErrorStatus(err), fmt.Errorf("解析上传失败：%w", err))
			return
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}
		m, err := s.saveMaterial(dir, u.ID, part, limit)
		part.Close()
		if err != nil {
			s.rollbackMaterials(saved)
			fail(w, uploadErrorStatus(err), err)
			return
		}
		saved = append(saved, m)
	}
	if len(saved) == 0 {
		fail(w, http.StatusBadRequest, errors.New("没有收到文件"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"materials":   saved,
		"dir":         dir,
		"maxUploadMB": limit >> 20,
	})
}

// rollbackMaterials undoes a request that failed halfway: a 413 in the second
// file must not leave the first one in the inbox, otherwise the workbench shows
// an upload that the client believes failed.
func (s *Server) rollbackMaterials(saved []*domain.RawMaterial) {
	for _, m := range saved {
		if within(s.imp.InboxDir(), m.Path) {
			_ = os.Remove(m.Path)
		}
		_ = s.store.DeleteMaterial(m.ID)
	}
}

// saveMaterial copies one upload into the inbox and records it.
//
// The stored name keeps the original extension — a developer opening the folder
// needs to know what each blob is — and gets a timestamp plus a random suffix so
// two files with the same name never collide.
func (s *Server) saveMaterial(dir, uploader string, part *multipart.Part, limit int64) (*domain.RawMaterial, error) {
	original := filepath.Base(filepath.Clean(part.FileName()))
	if original == "." || original == "" {
		return nil, errors.New("文件名不合法")
	}
	stored := time.Now().UTC().Format("20060102T150405") + "-" + store.NewID("raw")
	if ext := strings.ToLower(filepath.Ext(original)); ext != "" {
		stored += ext
	}
	dst := filepath.Join(dir, stored)
	out, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	// Read one byte past the cap: enough to know it overflowed, without letting
	// a single part fill the disk.
	written, err := io.Copy(out, io.LimitReader(part, limit+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(dst)
		return nil, err
	}
	if written > limit {
		os.Remove(dst)
		return nil, &errMaterialTooLarge{name: original, mb: limit >> 20}
	}
	m := &domain.RawMaterial{
		ID:         store.NewID("material"),
		Name:       original,
		Path:       dst,
		MIME:       mimeOf(original, part.Header.Get("Content-Type")),
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
