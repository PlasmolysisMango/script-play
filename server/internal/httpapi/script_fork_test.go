package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A published script is edited in place: the record keeps its id, the version
// goes up, and rooms started afterwards bind the new revision. Running games
// hold their own copy, so they are never rewritten under the table.
//
// An invalid revision is refused outright, and publishing twice must not keep
// bumping the version.
func TestPublishedScriptIsEditedInPlace(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 10*time.Second)

	author := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"script"`
	}
	author.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	var detail struct {
		Script map[string]any `json:"script"`
	}
	author.mustJSON(http.MethodGet, "/api/scripts/"+seeded.Script.ID, nil, &detail, http.StatusOK)

	body, _ := detail.Script["body"].(map[string]any)
	meta, _ := body["meta"].(map[string]any)
	meta["title"] = "雨夜灯塔（就地修订）"

	var saved struct {
		Script struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Version int    `json:"version"`
		} `json:"script"`
	}
	author.mustJSON(http.MethodPut, "/api/scripts/"+seeded.Script.ID, body, &saved, http.StatusOK)
	if saved.Script.ID != seeded.Script.ID {
		t.Fatalf("edit created a new record: %q", saved.Script.ID)
	}
	if saved.Script.Status != "published" {
		t.Fatalf("status after edit = %q", saved.Script.Status)
	}
	if saved.Script.Version != seeded.Script.Version+1 {
		t.Fatalf("version after edit = %d, want %d", saved.Script.Version, seeded.Script.Version+1)
	}

	// A broken revision must not replace a published one: a new room would bind
	// a script that cannot be played.
	blob, _ := json.Marshal(body)
	var broken map[string]any
	if err := json.Unmarshal(blob, &broken); err != nil {
		t.Fatalf("clone body: %v", err)
	}
	broken["roles"] = []any{}
	if code, payload := author.call(http.MethodPut, "/api/scripts/"+seeded.Script.ID, broken); code != http.StatusUnprocessableEntity {
		t.Fatalf("edit published into invalid = %d %s", code, payload)
	}

	var after struct {
		Script struct {
			Version int `json:"version"`
			Body    struct {
				Meta struct {
					Title string `json:"title"`
				} `json:"meta"`
			} `json:"body"`
		} `json:"script"`
	}
	author.mustJSON(http.MethodGet, "/api/scripts/"+seeded.Script.ID, nil, &after, http.StatusOK)
	if after.Script.Version != saved.Script.Version {
		t.Fatalf("rejected edit still bumped version to %d", after.Script.Version)
	}
	if after.Script.Body.Meta.Title != "雨夜灯塔（就地修订）" {
		t.Fatalf("rejected edit overwrote the body: %q", after.Script.Body.Meta.Title)
	}

	// Publishing an already published script only re-validates it.
	author.mustJSON(http.MethodPost, "/api/scripts/"+seeded.Script.ID+"/publish", nil, &saved, http.StatusOK)
	if saved.Script.Version != after.Script.Version {
		t.Fatalf("republish bumped version to %d", saved.Script.Version)
	}
}

// Forking stays available for branching a story: the copy is an editable draft
// that publishes as the next version.
func TestPublishedScriptForksIntoEditableDraft(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 10*time.Second)

	author := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"script"`
	}
	author.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	var forked struct {
		Script struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Version int    `json:"version"`
		} `json:"script"`
	}
	author.mustJSON(http.MethodPost, "/api/scripts/"+seeded.Script.ID+"/draft", nil, &forked, http.StatusCreated)
	if forked.Script.ID == "" || forked.Script.ID == seeded.Script.ID {
		t.Fatalf("fork did not create a new record: %+v", forked)
	}
	if forked.Script.Status != "draft" {
		t.Fatalf("fork status = %q", forked.Script.Status)
	}

	// The copy is editable, and publishing it yields the next version.
	var detail struct {
		Script map[string]any `json:"script"`
	}
	author.mustJSON(http.MethodGet, "/api/scripts/"+forked.Script.ID, nil, &detail, http.StatusOK)
	body, _ := detail.Script["body"].(map[string]any)
	meta, _ := body["meta"].(map[string]any)
	meta["title"] = "雨夜灯塔（分支版）"
	author.mustJSON(http.MethodPut, "/api/scripts/"+forked.Script.ID, body, nil, http.StatusOK)

	var published struct {
		Script struct {
			Status  string `json:"status"`
			Version int    `json:"version"`
		} `json:"script"`
	}
	author.mustJSON(http.MethodPost, "/api/scripts/"+forked.Script.ID+"/publish", nil, &published, http.StatusOK)
	if published.Script.Status != "published" {
		t.Fatalf("published status = %q", published.Script.Status)
	}
	if published.Script.Version != forked.Script.Version+1 {
		t.Fatalf("version = %d, want %d", published.Script.Version, forked.Script.Version+1)
	}
}
