package engine

import (
	"encoding/json"
	"os"
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

// The shipped K10 books must not hand out their later steps on the first
// reading: p0 stops at 第六步, p6 opens it, and p9 opens the third search.
func TestK10RoleBooksUnlockWithPhases(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/K10-梁溪诡影/script.json")
	if err != nil {
		t.Skipf("K10 script not on disk: %v", err)
	}
	var sc domain.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse K10: %v", err)
	}
	idx := map[string]int{}
	for i, ph := range sc.Phases {
		idx[ph.ID] = i
	}
	role := func(id string) *domain.Role {
		for i := range sc.Roles {
			if sc.Roles[i].ID == id {
				return &sc.Roles[i]
			}
		}
		t.Fatalf("role %s missing", id)
		return nil
	}
	gate := func(id string, phaseIndex int) string {
		r := role(id)
		return gateRoleScript(r.Private.Script, &sc, phaseIndex)
	}

	cases := []struct {
		role     string
		phase    string
		want     []string
		notWant  []string
		lockedAt string
	}{
		{role: "r_jiaming", phase: "p0", notWant: []string{"### 第六步", "### 第九步"}, lockedAt: "未解锁 · 公聊案情（第二轮）"},
		{role: "r_jiaming", phase: "p6", want: []string{"### 第六步"}, notWant: []string{"### 第九步"}, lockedAt: "未解锁 · 第三轮搜证 · 祸起萧墙"},
		{role: "r_jiaming", phase: "p9", want: []string{"### 第六步", "### 第九步"}},
		{role: "r_furen", phase: "p0", notWant: []string{"### 第九步"}, lockedAt: "未解锁 · 第三轮搜证 · 祸起萧墙"},
		{role: "r_furen", phase: "p9", want: []string{"### 第九步"}},
	}
	for _, tc := range cases {
		got := gate(tc.role, idx[tc.phase])
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s at %s: missing %q", tc.role, tc.phase, w)
			}
		}
		for _, n := range tc.notWant {
			if strings.Contains(got, n) {
				t.Errorf("%s at %s: leaked %q", tc.role, tc.phase, n)
			}
		}
		if tc.lockedAt != "" && !strings.Contains(got, tc.lockedAt) {
			t.Errorf("%s at %s: missing placeholder %q", tc.role, tc.phase, tc.lockedAt)
		}
		if strings.Contains(got, "read-hold") {
			t.Errorf("%s at %s: directive leaked into the book", tc.role, tc.phase)
		}
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

// The identity facts a role can state without investigation must reach that
// seat only: 林夫人 knows her own past and what she did to 娟娘, 江碧玉 knows
// her birth, and nobody else is handed those certainties on the first page.
func TestK10KnownFactsStayWithTheirRole(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/K10-梁溪诡影/script.json")
	if err != nil {
		t.Skipf("K10 script not on disk: %v", err)
	}
	var sc domain.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse K10: %v", err)
	}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat_furen", RoomID: "room1", RoleID: "r_furen", Kind: domain.SeatHuman, DisplayName: "林夫人"},
		{ID: "seat_biyu", RoomID: "room1", RoleID: "r_biyu", Kind: domain.SeatHuman, DisplayName: "江碧玉"},
		{ID: "seat_jiaming", RoomID: "room1", RoleID: "r_jiaming", Kind: domain.SeatHuman, DisplayName: "韩家明"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 0

	facts := func(i int) map[string]bool {
		out := map[string]bool{}
		v := VisibleFor(g, &sc, &g.Seats[i], VisibleOptions{})
		for _, f := range v.OwnFacts {
			out[f.ID] = true
		}
		return out
	}
	furen, biyu, jiaming := facts(0), facts(1), facts(2)
	if !furen["f_lady"] || !furen["f_juan"] {
		t.Fatalf("林夫人 facts = %+v, want f_lady+f_juan", furen)
	}
	if furen["f_biyu"] {
		t.Fatalf("林夫人 leaked 江碧玉's fact: %+v", furen)
	}
	if !biyu["f_biyu"] || len(biyu) != 1 {
		t.Fatalf("江碧玉 facts = %+v, want only f_biyu", biyu)
	}
	if len(jiaming) != 0 {
		t.Fatalf("韩家明 got facts he does not know yet: %+v", jiaming)
	}
}
