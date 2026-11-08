package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"script-play/internal/domain"
	"script-play/internal/store"
)

type scriptSummary struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary,omitempty"`
	Status    string            `json:"status"`
	Version   int               `json:"version"`
	Source    string            `json:"source"`
	Tags      []string          `json:"tags,omitempty"`
	Roles     int               `json:"roles"`
	Clues     int               `json:"clues"`
	Players   domain.CountRange `json:"playerCount"`
	Owner     bool              `json:"owner"`
	Warnings  int               `json:"warnings"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

func summarise(rec *domain.ScriptRecord, userID string) scriptSummary {
	out := scriptSummary{
		ID:        rec.ID,
		Status:    rec.Status,
		Version:   rec.Version,
		Source:    rec.Source,
		Owner:     rec.OwnerUserID == userID,
		Warnings:  len(rec.Warnings),
		UpdatedAt: rec.UpdatedAt,
	}
	if rec.Body != nil {
		out.Title = rec.Body.Meta.Title
		out.Summary = rec.Body.Meta.Summary
		out.Tags = rec.Body.Meta.Tags
		out.Roles = len(rec.Body.Roles)
		out.Clues = len(rec.Body.Clues)
		out.Players = rec.Body.Meta.PlayerCount
	}
	return out
}

func (s *Server) handleListScripts(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	recs := s.store.ListScripts(u.ID)
	if u.IsAdmin() {
		// The library is curated by admins: they need to see drafts that other
		// admin accounts (or the seeding system account) own.
		recs = s.store.AllScripts()
	}
	out := make([]scriptSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, summarise(rec, u.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": out})
}

// ownsScript reports whether u may edit rec. Admins curate the whole library,
// which also keeps the seeded sample (owned by the system account) editable.
func ownsScript(u *domain.User, rec *domain.ScriptRecord) bool {
	return u.IsAdmin() || rec.OwnerUserID == u.ID
}

func (s *Server) handleGetScript(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, err := s.store.GetScript(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("剧本不存在"))
		return
	}
	owner := ownsScript(u, rec)
	if !owner && rec.Status != "published" {
		fail(w, http.StatusForbidden, errors.New("无权查看该剧本"))
		return
	}
	if owner {
		writeJSON(w, http.StatusOK, map[string]any{"script": rec, "owner": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"script": redact(rec), "owner": false})
}

// redact removes the solution and every private block for non-owners.
func redact(rec *domain.ScriptRecord) *domain.ScriptRecord {
	cp := *rec
	if rec.Body == nil {
		return &cp
	}
	body := *rec.Body
	body.Truth = domain.Truth{}
	roles := make([]domain.Role, 0, len(rec.Body.Roles))
	for _, r := range rec.Body.Roles {
		r.Private = domain.RolePrivate{Script: "（未发布给非作者查看）"}
		roles = append(roles, r)
	}
	body.Roles = roles
	body.Clues = nil
	body.Facts = nil
	phases := make([]domain.Phase, len(body.Phases))
	copy(phases, body.Phases)
	for i := range phases {
		phases[i].DMScript = ""
	}
	body.Phases = phases
	cp.Body = &body
	cp.RawSource = ""
	return &cp
}

func (s *Server) handleCreateScript(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	rec, verrs, err := s.imp.FromJSON(u.ID, raw)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"script": rec, "errors": verrs})
}

func (s *Server) handleSeedSample(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, verrs, err := s.imp.FromJSON(u.ID, []byte(SampleScriptJSON))
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if len(verrs) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"script": rec, "errors": verrs})
		return
	}
	if _, err := s.imp.Publish(rec); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"script": rec})
}

func (s *Server) handleUpdateScript(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, err := s.store.GetScript(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("剧本不存在"))
		return
	}
	if !ownsScript(u, rec) {
		fail(w, http.StatusForbidden, errors.New("只有作者可以编辑"))
		return
	}
	var body domain.Script
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	wasPublished := rec.Status == "published"
	verrs, err := s.imp.Save(rec, &body)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if wasPublished && len(verrs) > 0 {
		// Nothing was stored: a published script can only be replaced by a valid
		// revision, otherwise the next room would bind a broken story.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": verrs})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"script": rec, "errors": verrs})
}

// handleForkScript copies a published script into an editable draft.
func (s *Server) handleForkScript(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, err := s.store.GetScript(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("剧本不存在"))
		return
	}
	if !ownsScript(u, rec) {
		fail(w, http.StatusForbidden, errors.New("只有作者可以另存草稿"))
		return
	}
	draft, err := s.imp.ForkDraft(rec, u.ID)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"script": draft, "errors": []domain.ValidationError{}})
}

func (s *Server) handlePublishScript(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, err := s.store.GetScript(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("剧本不存在"))
		return
	}
	if !ownsScript(u, rec) {
		fail(w, http.StatusForbidden, errors.New("只有作者可以发布"))
		return
	}
	verrs, err := s.imp.Publish(rec)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if len(verrs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": verrs})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"script": rec})
}

func (s *Server) handleDeleteScript(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rec, err := s.store.GetScript(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("剧本不存在"))
		return
	}
	if !ownsScript(u, rec) {
		fail(w, http.StatusForbidden, errors.New("只有作者可以删除"))
		return
	}
	if err := s.store.DeleteScript(rec.ID); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sampleTitle identifies the bundled example, which is seeded exactly once.
const sampleTitle = "雨夜灯塔（示例）"

// HasSampleScript reports whether the example is already in the library. When it
// is, boot needs no system owner account at all.
func HasSampleScript(st *store.Store) bool {
	for _, rec := range st.ListScripts("") {
		if rec.Body != nil && rec.Body.Meta.Title == sampleTitle {
			return true
		}
	}
	return false
}

// SeedSample inserts the sample script on first boot.
func SeedSample(st *store.Store, sysUser *domain.User) error {
	if HasSampleScript(st) {
		return nil
	}
	var sc domain.Script
	if err := json.Unmarshal([]byte(SampleScriptJSON), &sc); err != nil {
		return err
	}
	if errs := sc.Validate(); len(errs) > 0 {
		return fmt.Errorf("示例剧本校验失败：%v", errs[0])
	}
	now := time.Now().UTC()
	st.PutScript(&domain.ScriptRecord{
		ID:          store.NewID("script"),
		OwnerUserID: sysUser.ID,
		Status:      "published",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
		Source:      "sample",
		Body:        &sc,
	})
	return nil
}
