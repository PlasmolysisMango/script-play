package engine

import (
	"strings"
	"testing"

	"script-play/internal/domain"
)

func readHoldScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "阅读锁测试", PlayerCount: domain.CountRange{Min: 1, Max: 1}},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "第一幕"},
			{ID: "p2", Type: domain.PhaseSearch, Name: "第二轮搜证"},
			{ID: "p3", Type: domain.PhaseReveal, Name: "真相"},
		},
		Roles: []domain.Role{{
			ID:   "r1",
			Name: "甲",
			Private: domain.RolePrivate{
				Script: "第一幕正文\n<!-- read-hold:p2 -->\n第二幕正文\n<!-- /read-hold -->\n你的秘密",
			},
		}},
		Clues: []domain.Clue{{ID: "c1", Title: "照片", Content: "一张照片。", ImageURL: "/img/photo.png"}},
	}
}

func TestRoleBookHidesHeldPagesUntilPhase(t *testing.T) {
	sc := readHoldScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"}})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 0

	v := VisibleFor(g, sc, &g.Seats[0], VisibleOptions{})
	script := v.OwnRole.Private.Script
	if strings.Contains(script, "第二幕正文") {
		t.Fatal("a held page reached the seat before its phase")
	}
	if !strings.Contains(script, "未解锁 · 第二轮搜证") {
		t.Fatalf("locked page did not leave a phase-named placeholder:\n%s", script)
	}
	if !strings.Contains(script, "你的秘密") || strings.Contains(script, "read-hold") {
		t.Fatalf("unlocked tail or directive markers were mangled:\n%s", script)
	}

	g.PhaseIndex = 1
	v = VisibleFor(g, sc, &g.Seats[0], VisibleOptions{})
	script = v.OwnRole.Private.Script
	if !strings.Contains(script, "第二幕正文") || strings.Contains(script, "未解锁") || strings.Contains(script, "read-hold") {
		t.Fatalf("page did not open when its phase was reached:\n%s", script)
	}
}

func TestTranscriptCarriesClueImage(t *testing.T) {
	sc := readHoldScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"}})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	g.AppendEvent(domain.Event{Type: domain.EvClueRevealed, ClueID: "c1", Text: "公开了线索【照片】", Visibility: domain.VisPublic})

	v := VisibleFor(g, sc, &g.Seats[0], VisibleOptions{})
	if len(v.Transcript) != 1 || v.Transcript[0].ImageURL != "/img/photo.png" {
		t.Fatalf("clue image did not reach the transcript line: %+v", v.Transcript)
	}
}
