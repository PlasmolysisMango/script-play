package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
	"time"
)

// upload posts one file as multipart/form-data. The apiClient helper always
// sends JSON, and a raw script upload cannot be JSON.
func (c *apiClient) upload(path, filename string, blob []byte) (int, []byte) {
	c.t.Helper()
	return c.uploadMany(path, []string{filename}, [][]byte{blob})
}

// uploadMany posts several files in one form, in order, so a test can prove
// what happens when the second file fails after the first one was stored.
func (c *apiClient) uploadMany(path string, names []string, blobs [][]byte) (int, []byte) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i, name := range names {
		if name == "" {
			continue
		}
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			c.t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(blobs[i]); err != nil {
			c.t.Fatalf("write form file: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		c.t.Fatalf("close form: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, &buf)
	if err != nil {
		c.t.Fatalf("request %s: %v", path, err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("do %s: %v", path, err)
	}
	defer res.Body.Close()
	payload, _ := io.ReadAll(res.Body)
	return res.StatusCode, payload
}

type materialView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}

// Uploading archives the original verbatim and reports where it went: nothing
// is parsed, so the bytes on disk must match the bytes that were sent.
func TestAdminUploadsRawMaterial(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	original := []byte("第一幕\n甲和乙在海边。\n乙是凶手。")
	code, body := admin.upload("/api/admin/materials", "雨夜灯塔.docx", original)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d %s", code, body)
	}
	var created struct {
		Materials []materialView `json:"materials"`
		Dir       string         `json:"dir"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode upload: %v body=%s", err, body)
	}
	if len(created.Materials) != 1 {
		t.Fatalf("materials = %d, want 1", len(created.Materials))
	}
	m := created.Materials[0]
	if m.Name != "雨夜灯塔.docx" {
		t.Fatalf("name = %q", m.Name)
	}
	if m.Size != int64(len(original)) {
		t.Fatalf("size = %d, want %d", m.Size, len(original))
	}

	stored, err := os.ReadFile(m.Path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(stored, original) {
		t.Fatal("stored file differs from the upload")
	}
	if fi, err := os.Stat(created.Dir); err != nil || !fi.IsDir() {
		t.Fatalf("inbox dir %q missing: %v", created.Dir, err)
	}

	var list struct {
		Materials []materialView `json:"materials"`
		Dir       string         `json:"dir"`
	}
	admin.mustJSON(http.MethodGet, "/api/admin/materials", nil, &list, http.StatusOK)
	if len(list.Materials) != 1 || list.Materials[0].ID != m.ID {
		t.Fatalf("list = %+v", list.Materials)
	}

	admin.mustJSON(http.MethodDelete, "/api/admin/materials/"+m.ID, nil, nil, http.StatusOK)
	if _, err := os.Stat(m.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file should be gone, stat err = %v", err)
	}
	admin.mustJSON(http.MethodGet, "/api/admin/materials", nil, &list, http.StatusOK)
	if len(list.Materials) != 0 {
		t.Fatalf("materials after delete = %d, want 0", len(list.Materials))
	}
}

// An oversize file is refused as 413, and a request that fails halfway leaves
// nothing behind: a half-uploaded game folder is worse than a clean failure.
func TestUploadMaterialTooLargeIsRefused(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	// newE2EServer caps one file at 4 MB; one byte over is enough. The body cap
	// is twice that, so this exercises the per-file check, not the reader.
	over := make([]byte, 4<<20+1)
	code, body := admin.uploadMany("/api/admin/materials",
		[]string{"第一幕.pdf", "大全集.zip"}, [][]byte{[]byte("%PDF-1.7 正常"), over})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload = %d %s", code, body)
	}

	var list struct {
		Materials   []materialView `json:"materials"`
		MaxUploadMB int64          `json:"maxUploadMB"`
	}
	admin.mustJSON(http.MethodGet, "/api/admin/materials", nil, &list, http.StatusOK)
	if len(list.Materials) != 0 {
		t.Fatalf("materials after 413 = %d, want 0", len(list.Materials))
	}
	if list.MaxUploadMB != 4 {
		t.Fatalf("maxUploadMB = %d, want 4", list.MaxUploadMB)
	}
}

// An empty form is a client bug, not an empty import: say so instead of
// returning 201 with nothing in it.
func TestUploadMaterialRequiresAFile(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	code, body := admin.upload("/api/admin/materials", "", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("empty upload = %d %s", code, body)
	}
}
