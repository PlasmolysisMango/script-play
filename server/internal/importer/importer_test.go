package importer

import (
	"os"
	"path/filepath"
	"testing"

	"script-play/internal/domain"
	"script-play/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st), st
}

func TestNewCreatesInbox(t *testing.T) {
	svc, st := newTestService(t)
	want := filepath.Join(st.Dir(), "inbox")
	if svc.InboxDir() != want {
		t.Fatalf("inbox = %q, want %q", svc.InboxDir(), want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("inbox directory missing: %v", err)
	}
}

func TestFromJSONRejectsBrokenScript(t *testing.T) {
	svc, _ := newTestService(t)
	rec, verrs, err := svc.FromJSON("u1", []byte(`{"schemaVersion":1,"meta":{"title":""}}`))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(verrs) == 0 {
		t.Fatal("empty script must produce validation errors")
	}
	if rec.Status != "draft" {
		t.Fatalf("status = %q, want draft", rec.Status)
	}
	verrs, err = svc.Publish(rec)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(verrs) == 0 {
		t.Fatal("publishing an invalid script must report validation errors")
	}
	if rec.Status != "draft" {
		t.Fatalf("invalid script must stay draft, got %q", rec.Status)
	}
}

func TestFromJSONAcceptsExtendedScript(t *testing.T) {
	svc, st := newTestService(t)
	raw := `{
	  "schemaVersion": 1,
	  "meta": {"title": "测试本", "playerCount": {"min": 2, "max": 2}},
	  "truth": {"killerRoleId": "r2", "summary": "真相是乙。"},
	  "phases": [
	    {"id": "p1", "type": "reading", "name": "阅读剧本"},
	    {"id": "p2", "type": "search", "name": "搜证", "cluePool": ["c1"], "perPlayerPick": 1},
	    {"id": "p3", "type": "discussion", "name": "讨论", "speechPerPlayer": 1},
	    {"id": "p4", "type": "vote", "name": "投票"},
	    {"id": "p5", "type": "reveal", "name": "复盘"}
	  ],
	  "roles": [
	    {"id": "r1", "name": "甲", "public": {"intro": "甲"}, "private": {"script": "甲本"}},
	    {"id": "r2", "name": "乙", "public": {"intro": "乙"}, "private": {"script": "乙本"}}
	  ],
	  "clues": [{"id": "c1", "title": "刀", "content": "一把刀", "isPublic": false}]
	}`
	rec, verrs, err := svc.FromJSON("u1", []byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(verrs) != 0 {
		t.Fatalf("unexpected validation errors: %v", verrs)
	}
	if rec.Source != "json" {
		t.Fatalf("source = %q, want json", rec.Source)
	}
	if rec.Body.Phases[0].Type != domain.PhaseReading {
		t.Fatalf("first phase = %s", rec.Body.Phases[0].Type)
	}
	verrs, err = svc.Publish(rec)
	if err != nil || len(verrs) != 0 {
		t.Fatalf("publish: %v %v", err, verrs)
	}
	if rec.Status != "published" || rec.Version != 2 {
		t.Fatalf("status=%s version=%d", rec.Status, rec.Version)
	}
	if _, err := st.GetScript(rec.ID); err != nil {
		t.Fatalf("published script not stored: %v", err)
	}
}

// strategyScript is a minimal valid script whose phases leave both strategy
// fields empty, so normalize has something to fill in.
const strategyScript = `{
  "schemaVersion": 1,
  "meta": {"title": "策略测试本", "playerCount": {"min": 2, "max": 2}},
  "truth": {"killerRoleId": "r2"},
  "phases": [
    {"id": "p1", "type": "reading", "name": "阅读剧本"},
    {"id": "p2", "type": "search", "name": "搜证", "cluePool": ["c1"], "perPlayerPick": 1},
    {"id": "p3", "type": "discussion", "name": "讨论", "speechPerPlayer": 1},
    {"id": "p4", "type": "vote", "name": "投票"},
    {"id": "p5", "type": "reveal", "name": "复盘"}
  ],
  "roles": [
    {"id": "r1", "name": "甲", "public": {"intro": "甲"}, "private": {"script": "甲本"}},
    {"id": "r2", "name": "乙", "public": {"intro": "乙"}, "private": {"script": "乙本"}}
  ],
  "clues": [{"id": "c1", "title": "刀", "content": "一把刀"}]
}`

func TestNormalizeWritesStrategyDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	rec, verrs, err := svc.FromJSON("u1", []byte(strategyScript))
	if err != nil || len(verrs) != 0 {
		t.Fatalf("import: %v %v", err, verrs)
	}
	sc := rec.Body
	if sc.Phases[1].SearchMode != domain.SearchPick {
		t.Fatalf("searchMode = %q, want pick", sc.Phases[1].SearchMode)
	}
	for _, i := range []int{1, 2} {
		if sc.Phases[i].Order != domain.OrderRoundRobin {
			t.Fatalf("phases[%d].order = %q, want round_robin", i, sc.Phases[i].Order)
		}
	}
}

// A draft may be saved with holes in it; a published script may not, and saving
// one is what produces the next version.
func TestSaveBumpsPublishedAndKeepsDraftForgiving(t *testing.T) {
	svc, st := newTestService(t)
	rec, verrs, err := svc.FromJSON("u1", []byte(strategyScript))
	if err != nil || len(verrs) != 0 {
		t.Fatalf("import: %v %v", err, verrs)
	}

	// Draft: an incomplete body is accepted and recorded as warnings.
	good := *rec.Body
	holes := *rec.Body
	holes.Roles = nil
	verrs, err = svc.Save(rec, &holes)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if len(verrs) == 0 || len(rec.Warnings) == 0 {
		t.Fatalf("draft save must report warnings: %v / %v", verrs, rec.Warnings)
	}
	if rec.Version != 1 {
		t.Fatalf("draft version = %d, want unchanged", rec.Version)
	}

	// The draft is still a draft once the holes are filled again.
	if verrs, err = svc.Save(rec, &good); err != nil || len(verrs) != 0 {
		t.Fatalf("save complete draft: %v %v", err, verrs)
	}
	if rec.Status != "draft" {
		t.Fatalf("status = %q, want draft", rec.Status)
	}

	// Publish the good body, then edit it in place.
	if verrs, err = svc.Publish(rec); err != nil || len(verrs) != 0 {
		t.Fatalf("publish: %v %v", err, verrs)
	}
	published := rec.Version
	bumped := *rec.Body
	bumped.Meta.Title = "策略测试本（修订）"
	verrs, err = svc.Save(rec, &bumped)
	if err != nil || len(verrs) != 0 {
		t.Fatalf("save published: %v %v", err, verrs)
	}
	if rec.Version != published+1 {
		t.Fatalf("version = %d, want %d", rec.Version, published+1)
	}

	// A broken revision must not replace the published one.
	verrs, err = svc.Save(rec, &holes)
	if err != nil {
		t.Fatalf("save broken published: %v", err)
	}
	if len(verrs) == 0 {
		t.Fatal("a broken revision must not replace a published script")
	}
	stored, getErr := st.GetScript(rec.ID)
	if getErr != nil {
		t.Fatalf("reload: %v", getErr)
	}
	if stored.Version != rec.Version || stored.Body.Meta.Title != "策略测试本（修订）" {
		t.Fatalf("rejected save changed the record: v%d %q", stored.Version, stored.Body.Meta.Title)
	}
}

func TestForkDraftCopiesBodyIntoNewDraft(t *testing.T) {
	svc, st := newTestService(t)
	rec, _, err := svc.FromJSON("u1", []byte(`{"meta":{"title":"甲本"}}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec.Body.Meta.Title = "甲本 v2"
	draft, err := svc.ForkDraft(rec, "u2")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if draft.ID == rec.ID {
		t.Fatal("fork must be a new record")
	}
	if draft.Status != "draft" || draft.OwnerUserID != "u2" {
		t.Fatalf("draft status=%s owner=%s", draft.Status, draft.OwnerUserID)
	}
	if draft.Body == rec.Body {
		t.Fatal("fork must deep copy the body")
	}
	if _, err := st.GetScript(draft.ID); err != nil {
		t.Fatalf("forked draft not stored: %v", err)
	}
}
