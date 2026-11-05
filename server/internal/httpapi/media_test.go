package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A 1x1 png: big enough for the content sniff, small enough to inline.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	blob, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode fixture png: %v", err)
	}
	return blob
}

type mediaUpload struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}

// A clue is often a photograph, so the studio uploads images next to the script
// and stores a relative URL. The file must come back byte for byte, without a
// token: a clue card has to render on a phone that is already signed in.
func TestUploadScriptImageIsServedBack(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	png := tinyPNG(t)
	code, body := admin.upload("/api/scripts/"+seeded.Script.ID+"/media", "尸体照片.png", png)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d %s", code, body)
	}
	var up mediaUpload
	if err := json.Unmarshal(body, &up); err != nil {
		t.Fatalf("decode upload: %v body=%s", err, body)
	}
	if !strings.HasPrefix(up.URL, "/media/"+seeded.Script.ID+"/") {
		t.Fatalf("url = %q", up.URL)
	}
	if up.MIME != "image/png" || up.Size != int64(len(png)) {
		t.Fatalf("upload = %+v", up)
	}

	res, err := http.Get(ts.URL + up.URL)
	if err != nil {
		t.Fatalf("get media: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get media = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type = %q", ct)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header = %q", got)
	}
	served, _ := io.ReadAll(res.Body)
	if string(served) != string(png) {
		t.Fatal("served bytes differ from the upload")
	}
}

// The allow list is about where the file lands, not about politeness: a pdf or a
// script renamed to .png is refused before anything is written.
func TestUploadScriptImageRejectsNonImages(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)
	path := "/api/scripts/" + seeded.Script.ID + "/media"

	if code, body := admin.upload(path, "线索.pdf", []byte("%PDF-1.7")); code != http.StatusUnsupportedMediaType {
		t.Fatalf("pdf upload = %d %s", code, body)
	}
	// A .png that is really HTML would be a stored XSS if it were served as html.
	if code, body := admin.upload(path, "假图片.png", []byte("<html><script>alert(1)</script>")); code != http.StatusUnsupportedMediaType {
		t.Fatalf("html-as-png upload = %d %s", code, body)
	}
	if code, body := admin.upload(path, "", nil); code != http.StatusBadRequest {
		t.Fatalf("empty upload = %d %s", code, body)
	}
}

func TestUploadScriptImageNeedsOwnership(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	player := &apiClient{t: t, base: ts.URL}
	var session struct {
		Token string `json:"token"`
	}
	player.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "player01", "password": "pass12345", "displayName": "玩家一"}, &session, http.StatusCreated)
	player.token = session.Token

	if code, body := player.upload("/api/scripts/"+seeded.Script.ID+"/media", "x.png", tinyPNG(t)); code != http.StatusForbidden {
		t.Fatalf("player upload = %d %s", code, body)
	}
}

// Anything that is not a stored picture under data/media answers 404 — including
// a path that tries to climb out of the media root.
func TestMediaFileRefusesUnknownPaths(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	for _, path := range []string{
		"/media/script_x/missing.png",
		"/media/script_x/state.json",
		"/media/..%2f..%2fsecret.key",
		"/media/script_x/",
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("get %s = %d, want 404", path, res.StatusCode)
		}
	}
}
