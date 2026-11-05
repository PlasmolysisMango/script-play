// Package importer turns the standard script JSON into an editable draft and
// publishes validated versions.
//
// Parsing raw material (zip, docx, pdf, photos) is deliberately out of scope:
// those files are stored untouched under data/inbox by the HTTP layer, and a
// developer converts them into the standard JSON offline before importing.
package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"script-play/internal/domain"
	"script-play/internal/store"
)

// Service owns draft creation, publication and the raw-material inbox.
type Service struct {
	store *store.Store
	inbox string
}

// New creates the service and makes sure the inbox directory exists.
func New(st *store.Store) *Service {
	inbox := filepath.Join(st.Dir(), "inbox")
	// Absolute on purpose: the path is shown to the admin and printed in the API
	// response, and a developer pastes it into a terminal from another cwd.
	if abs, err := filepath.Abs(inbox); err == nil {
		inbox = abs
	}
	// The upload handler retries the mkdir, so a failing filesystem must not stop
	// the server from booting: editing existing drafts still works.
	_ = os.MkdirAll(inbox, 0o755)
	return &Service{store: st, inbox: inbox}
}

// InboxDir is where uploaded originals are kept for offline conversion.
func (s *Service) InboxDir() string { return s.inbox }

// FromJSON validates an uploaded script JSON and stores it as a draft.
func (s *Service) FromJSON(owner string, raw []byte) (*domain.ScriptRecord, []domain.ValidationError, error) {
	var sc domain.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, nil, fmt.Errorf("JSON 解析失败：%w", err)
	}
	normalize(&sc)
	errs := sc.Validate()
	now := time.Now().UTC()
	rec := &domain.ScriptRecord{
		ID:          store.NewID("script"),
		OwnerUserID: owner,
		Status:      "draft",
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
		Source:      "json",
		Body:        &sc,
	}
	for _, e := range errs {
		rec.Warnings = append(rec.Warnings, e.Error())
	}
	s.store.PutScript(rec)
	return rec, errs, nil
}

// normalize fills the minimum needed for the engine to run.
func normalize(sc *domain.Script) {
	if sc.SchemaVersion == 0 {
		sc.SchemaVersion = 1
	}
	for i := range sc.Roles {
		if sc.Roles[i].ID == "" {
			sc.Roles[i].ID = fmt.Sprintf("r%d", i+1)
		}
		if sc.Roles[i].PersonaID == "" {
			sc.Roles[i].PersonaID = "guarded"
		}
		if strings.TrimSpace(sc.Roles[i].Private.Script) == "" {
			sc.Roles[i].Private.Script = sc.Roles[i].Public.Intro
		}
		for j := range sc.Roles[i].Private.Secrets {
			if sc.Roles[i].Private.Secrets[j].ID == "" {
				sc.Roles[i].Private.Secrets[j].ID = fmt.Sprintf("%s_s%d", sc.Roles[i].ID, j+1)
			}
			if sc.Roles[i].Private.Secrets[j].RevealCost == 0 {
				sc.Roles[i].Private.Secrets[j].RevealCost = 0.7
			}
		}
	}
	for i := range sc.Clues {
		if sc.Clues[i].ID == "" {
			sc.Clues[i].ID = fmt.Sprintf("c%d", i+1)
		}
	}
	if len(sc.Phases) == 0 {
		pool := make([]string, 0, len(sc.Clues))
		for _, c := range sc.Clues {
			pool = append(pool, c.ID)
		}
		// Reading first, because the validator insists on it and because it is
		// true: every script opens with the role books in hand.
		sc.Phases = []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseDiscussion, Name: "自我介绍与案情讨论", Round: 1, SpeechPerPlayer: 1, Order: domain.OrderRoundRobin},
			{ID: "p3", Type: domain.PhaseSearch, Name: "第一轮搜证", Round: 1, PerPlayerPick: 2, SearchMode: domain.SearchPick, Order: domain.OrderRoundRobin, CluePool: pool},
			{ID: "p4", Type: domain.PhaseDiscussion, Name: "第一轮公聊", Round: 1, SpeechPerPlayer: 2, Order: domain.OrderRoundRobin},
			{ID: "p5", Type: domain.PhaseVote, Name: "投票"},
			{ID: "p6", Type: domain.PhaseReveal, Name: "复盘"},
		}
	}
	for i := range sc.Phases {
		if sc.Phases[i].ID == "" {
			sc.Phases[i].ID = fmt.Sprintf("p%d", i+1)
		}
		if sc.Phases[i].Name == "" {
			sc.Phases[i].Name = sc.Phases[i].Type
		}
		// Strategy fields are written out instead of left implicit: the studio
		// edits what it reads, and an empty value would show as "默认" forever.
		switch sc.Phases[i].Type {
		case domain.PhaseSearch:
			if sc.Phases[i].SearchMode == "" {
				sc.Phases[i].SearchMode = domain.SearchPick
			}
			if sc.Phases[i].Order == "" {
				sc.Phases[i].Order = domain.OrderRoundRobin
			}
		case domain.PhaseDiscussion:
			if sc.Phases[i].Order == "" {
				sc.Phases[i].Order = domain.OrderRoundRobin
			}
		}
	}
	if sc.Meta.PlayerCount.Min == 0 {
		sc.Meta.PlayerCount.Min = 2
	}
	if sc.Meta.PlayerCount.Max == 0 {
		sc.Meta.PlayerCount.Max = len(sc.Roles)
	}
}

// Save overwrites the body of a record in place. A draft may be saved while it
// still has holes — that is what the warning list is for. A published script
// may not: a new room would bind the broken revision, so an invalid body is
// rejected and the stored version stays untouched.
//
// Saving a published script bumps the version, because that save is what rooms
// started afterwards will read. Running games keep the copy they started with
// (the store hands actors a deep copy), so they never see a half-edited story.
func (s *Service) Save(rec *domain.ScriptRecord, body *domain.Script) ([]domain.ValidationError, error) {
	if body == nil {
		return nil, fmt.Errorf("剧本内容为空")
	}
	normalize(body)
	errs := body.Validate()
	if rec.Status == "published" {
		if len(errs) > 0 {
			return errs, nil
		}
		rec.Body = body
		rec.Version++
		rec.Warnings = nil
		s.store.PutScript(rec)
		return nil, nil
	}
	rec.Body = body
	rec.Warnings = nil
	for _, e := range errs {
		rec.Warnings = append(rec.Warnings, e.Error())
	}
	s.store.PutScript(rec)
	return errs, nil
}

// ForkDraft copies a script into a new draft the author can edit.
//
// Editing a published script in place is the normal path (Save bumps the
// version). A fork is for branching a story instead of revising it: it keeps the
// source version number so that publishing it produces the next version rather
// than colliding with the one that is already live.
func (s *Service) ForkDraft(rec *domain.ScriptRecord, owner string) (*domain.ScriptRecord, error) {
	if rec.Body == nil {
		return nil, fmt.Errorf("剧本内容为空")
	}
	blob, err := json.Marshal(rec.Body)
	if err != nil {
		return nil, err
	}
	var body domain.Script
	if err := json.Unmarshal(blob, &body); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	draft := &domain.ScriptRecord{
		ID:          store.NewID("script"),
		OwnerUserID: owner,
		Status:      "draft",
		Version:     rec.Version,
		CreatedAt:   now,
		UpdatedAt:   now,
		Source:      rec.Source,
		Body:        &body,
		RawSource:   rec.RawSource,
		Warnings:    append([]string(nil), rec.Warnings...),
	}
	s.store.PutScript(draft)
	return draft, nil
}

// Publish promotes a draft to published, validating it first. Publishing an
// already published script only re-validates it: the version must not creep up
// every time the studio presses the button twice.
func (s *Service) Publish(rec *domain.ScriptRecord) ([]domain.ValidationError, error) {
	if rec.Body == nil {
		return nil, fmt.Errorf("剧本内容为空")
	}
	normalize(rec.Body)
	errs := rec.Body.Validate()
	if len(errs) > 0 {
		return errs, nil
	}
	if rec.Status == "published" {
		rec.Warnings = nil
		s.store.PutScript(rec)
		return nil, nil
	}
	rec.Status = "published"
	rec.Version++
	rec.Warnings = nil
	s.store.PutScript(rec)
	return nil, nil
}
