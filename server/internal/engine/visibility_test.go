package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
	"script-play/internal/persona"
)

// The fixture below is deliberately small but exercises every visibility rule:
// public role intros, private role scripts and secrets, an owner-only clue, a
// found-but-unrevealed clue, a revealed clue and the hidden truth.
const (
	fixtureTruthMethod  = "用冰锥刺入后颈，伪装成自杀。"
	fixtureTruthSummary = "周叙提前关掉灯塔电源，在黑暗中动手。"
	fixtureR1Script     = "沈曼丽知道自己在说谎。r1-script"
	fixtureR2Script     = "陆沉十点前一直在厨房洗碗。r2-script"
	fixtureR3Script     = "周叙在十点整关掉了电源。r3-script"
	fixtureR2Secret     = "我挪用了灯塔的维护款。r2-secret"
)

func fixtureScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta: domain.Meta{
			Title:       "雨夜灯塔（测试）",
			PlayerCount: domain.CountRange{Min: 3, Max: 4},
		},
		Truth: domain.Truth{
			KillerRoleID: "r3",
			Method:       fixtureTruthMethod,
			Summary:      fixtureTruthSummary,
		},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseOpening, Name: "开场", DMScript: "欢迎来到灯塔。"},
			{ID: "p2", Type: domain.PhaseSearch, Name: "第一轮搜证", Round: 1, CluePool: []string{"c1", "c2", "c3", "c4"}, PerPlayerPick: 1},
			{ID: "p3", Type: domain.PhaseDiscussion, Name: "第一轮讨论", Round: 1, SpeechPerPlayer: 1, Order: domain.OrderRoundRobin},
			{ID: "p4", Type: domain.PhaseVote, Name: "投票"},
			{ID: "p5", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{
				ID:   "r1",
				Name: "沈曼丽",
				Public: domain.RolePublic{
					Intro: "灯塔管理员的妻子。",
				},
				Private: domain.RolePrivate{
					Script:       fixtureR1Script,
					KnownFactIDs: []string{"f1"},
					Secrets:      []domain.Secret{{ID: "s1", Text: "沈曼丽当晚并不在房间。mine-r1-secret", RevealCost: 0.7}},
				},
			},
			{
				ID:     "r2",
				Name:   "陆沉",
				Public: domain.RolePublic{Intro: "灯塔的看守。"},
				Private: domain.RolePrivate{
					Script:  fixtureR2Script,
					Secrets: []domain.Secret{{ID: "s2", Text: fixtureR2Secret, RevealCost: 0.4}},
				},
			},
			{
				ID:     "r3",
				Name:   "周叙",
				Public: domain.RolePublic{Intro: "路过的记者。"},
				Private: domain.RolePrivate{
					Script: fixtureR3Script,
				},
			},
		},
		Clues: []domain.Clue{
			{ID: "c1", Title: "大厅告示", Content: "告示：十点熄灯。", IsPublic: true, Links: domain.ClueLinks{Facts: []string{"f2"}}},
			{ID: "c2", Title: "陆沉的怀表", Content: "怀表停在十点零五分。", OwnerRoleID: "r2", Links: domain.ClueLinks{Facts: []string{"f3"}}},
			{ID: "c3", Title: "楼梯上的水渍", Content: "水渍一路延伸到机房。"},
			{ID: "c4", Title: "被剪断的电线", Content: "电线是被人剪断的。"},
		},
		Facts: []domain.Fact{
			{ID: "f1", Text: "案发时在下雨。"},
			{ID: "f2", Text: "灯塔每晚十点熄灯。"},
			{ID: "f3", Text: "怀表属于陆沉。"},
		},
	}
}

// fixtureGame builds seats plus the event log used by the golden projections.
func fixtureGame() *domain.Game {
	base := time.Date(2024, 5, 1, 20, 0, 0, 0, time.UTC)
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "阿真", PersonaID: "guarded", IsHost: true, Connected: true, JoinedAt: base},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatAgent, DisplayName: "AI 陆沉", PersonaID: "sly", ModelID: "Auto", Connected: true, JoinedAt: base.Add(time.Second)},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatAgent, DisplayName: "AI 周叙", PersonaID: "candid", ModelID: "Auto", Connected: true, JoinedAt: base.Add(2 * time.Second)},
		{ID: "seat4", RoomID: "room1", Kind: domain.SeatSpectator, DisplayName: "旁观者", Connected: true, JoinedAt: base.Add(3 * time.Second)},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 2 // discussion
	g.Round = 1
	g.ClueState["c3"] = domain.ClueState{ClueID: "c3", FoundBySeat: "seat2"}
	g.ClueState["c4"] = domain.ClueState{ClueID: "c4", FoundBySeat: "seat1", Revealed: true, RevealedBy: "seat1"}

	g.AppendEvent(domain.Event{Type: domain.EvPhase, PhaseID: "p3", Text: "进入阶段：第一轮讨论"})
	g.AppendEvent(domain.Event{Type: domain.EvSpeech, SeatID: "seat1", RoleID: "r1", Speaker: "沈曼丽", Text: "我先说，我一直在房间里。"})
	g.AppendEvent(domain.Event{Type: domain.EvDM, Speaker: "主持人", Text: "请大家留意时间线。"})
	g.AppendEvent(domain.Event{Type: domain.EvClueFound, SeatID: "seat2", RoleID: "r2", ClueID: "c3", Visibility: SeatVisibility("seat2"), Text: "你搜到线索【楼梯上的水渍】"})
	g.AppendEvent(domain.Event{Type: domain.EvSpeech, SeatID: "seat2", RoleID: "r2", Speaker: "陆沉", Text: "我在厨房，没上过楼。"})
	return g
}

type seatProjection struct {
	SeatID      string
	OwnRole     string
	OwnClues    []string
	PublicClues []string
	OwnFacts    []string
	HasTruth    bool
	Transcript  []int
	PublicRoles int
	Seats       int
}

func projectVisible(v Visible) seatProjection {
	p := seatProjection{
		SeatID:      v.Seat.ID,
		OwnClues:    []string{},
		PublicClues: []string{},
		OwnFacts:    []string{},
		Transcript:  []int{},
		PublicRoles: len(v.PublicRoles),
		Seats:       len(v.Seats),
	}
	if v.OwnRole != nil {
		p.OwnRole = v.OwnRole.ID
	}
	for _, c := range v.OwnClues {
		p.OwnClues = append(p.OwnClues, c.ID)
	}
	for _, c := range v.PublicClues {
		p.PublicClues = append(p.PublicClues, c.ID)
	}
	for _, f := range v.OwnFacts {
		p.OwnFacts = append(p.OwnFacts, f.ID)
	}
	p.HasTruth = v.Truth != nil
	for _, line := range v.Transcript {
		p.Transcript = append(p.Transcript, line.Seq)
	}
	return p
}

// TestVisibilityGolden exhaustively asserts the visible field set of every
// seat. One extra clue, fact or transcript line fails the test.
func TestVisibilityGolden(t *testing.T) {
	sc := fixtureScript()
	g := fixtureGame()

	golden := map[string]seatProjection{
		"seat1": {
			SeatID: "seat1", OwnRole: "r1",
			OwnClues:    []string{},
			PublicClues: []string{"c1", "c4"},
			OwnFacts:    []string{"f1", "f2"},
			Transcript:  []int{1, 2, 3, 5},
			PublicRoles: 3, Seats: 4,
		},
		"seat2": {
			SeatID: "seat2", OwnRole: "r2",
			OwnClues:    []string{"c2", "c3"},
			PublicClues: []string{"c1", "c4"},
			OwnFacts:    []string{"f2", "f3"},
			Transcript:  []int{1, 2, 3, 5},
			PublicRoles: 3, Seats: 4,
		},
		"seat3": {
			SeatID: "seat3", OwnRole: "r3",
			OwnClues:    []string{},
			PublicClues: []string{"c1", "c4"},
			OwnFacts:    []string{"f2"},
			Transcript:  []int{1, 2, 3, 5},
			PublicRoles: 3, Seats: 4,
		},
		"seat4": {
			SeatID: "seat4",
			// Spectators have no role and therefore no private block at all.
			OwnClues:    []string{},
			PublicClues: []string{"c1", "c4"},
			OwnFacts:    []string{"f2"},
			Transcript:  []int{1, 2, 3, 5},
			PublicRoles: 3, Seats: 4,
		},
	}

	for seatID, want := range golden {
		seat := g.SeatByID(seatID)
		if seat == nil {
			t.Fatalf("fixture missing seat %s", seatID)
		}
		got := projectVisible(VisibleFor(g, sc, seat, VisibleOptions{}))
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("seat %s\n got: %+v\nwant: %+v", seatID, got, want)
		}
	}
}

// TestVisibilityNeverIncludesForeignPrivateData serialises the whole Visible
// value and proves that no foreign secret, script or truth fragment rides along.
func TestVisibilityNeverIncludesForeignPrivateData(t *testing.T) {
	sc := fixtureScript()
	g := fixtureGame()

	ownMarkers := map[string][]string{
		"seat1": {"r1-script", "mine-r1-secret"},
		"seat2": {"r2-script", "r2-secret"},
		"seat3": {"r3-script"},
		"seat4": {},
	}
	allMarkers := []string{"r1-script", "r2-script", "r3-script", "r2-secret", "mine-r1-secret"}

	for seatID, own := range ownMarkers {
		seat := g.SeatByID(seatID)
		blob, err := json.Marshal(VisibleFor(g, sc, seat, VisibleOptions{}))
		if err != nil {
			t.Fatalf("marshal visible: %v", err)
		}
		text := string(blob)
		for _, marker := range allMarkers {
			mine := false
			for _, o := range own {
				if o == marker {
					mine = true
				}
			}
			if strings.Contains(text, marker) && !mine {
				t.Errorf("seat %s leaked foreign private marker %q", seatID, marker)
			}
		}
		for _, secret := range []string{fixtureTruthMethod, fixtureTruthSummary} {
			if strings.Contains(text, secret) {
				t.Errorf("seat %s leaked truth %q", seatID, secret)
			}
		}
		if strings.Contains(text, `"modelId"`) {
			t.Errorf("seat %s leaked seat model configuration", seatID)
		}
		if strings.Contains(text, `"personaId"`) {
			t.Errorf("seat %s leaked seat persona configuration", seatID)
		}
	}
}

// TestVisibilityTruthOnlyOnReveal pins the one exception to the truth rule.
func TestVisibilityTruthOnlyOnReveal(t *testing.T) {
	sc := fixtureScript()
	g := fixtureGame()
	seat := g.SeatByID("seat1")

	if v := VisibleFor(g, sc, seat, VisibleOptions{IncludeTruth: true}); v.Truth != nil {
		t.Fatal("truth must stay hidden before the reveal phase")
	}

	g.PhaseIndex = 4 // reveal
	v := VisibleFor(g, sc, seat, VisibleOptions{IncludeTruth: true})
	if v.Truth == nil || v.Truth.KillerRoleID != "r3" {
		t.Fatal("host view must carry the truth during reveal")
	}
	if !strings.Contains(string(mustJSON(v)), fixtureTruthSummary) {
		t.Fatal("reveal view should expose the truth summary")
	}
}

func TestEventVisibleTo(t *testing.T) {
	g := fixtureGame()
	host := g.SeatByID("seat1")
	agent := g.SeatByID("seat2")
	spectator := g.SeatByID("seat4")

	cases := []struct {
		name string
		ev   domain.Event
		seat *domain.Seat
		want bool
	}{
		{"public to host", domain.Event{Type: domain.EvSystem, Visibility: domain.VisPublic}, host, true},
		{"public to spectator", domain.Event{Type: domain.EvSystem, Visibility: domain.VisPublic}, spectator, true},
		{"dm to host", domain.Event{Type: domain.EvDM, Visibility: domain.VisDM}, host, true},
		{"dm to agent", domain.Event{Type: domain.EvDM, Visibility: domain.VisDM}, agent, false},
		{"seat scoped owner", domain.Event{Visibility: SeatVisibility("seat2")}, agent, true},
		{"seat scoped other", domain.Event{Visibility: SeatVisibility("seat2")}, host, false},
		{"unknown scope", domain.Event{Visibility: "team:red"}, agent, false},
	}
	for _, tc := range cases {
		if got := EventVisibleTo(tc.ev, tc.seat); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// TestContextNeverLeaks is the prompt-level twin of the golden test: whatever
// the builder produces must be free of truth and foreign private material.
func TestContextNeverLeaks(t *testing.T) {
	sc := fixtureScript()
	g := fixtureGame()
	pb := NewPromptBuilder()

	for _, seatID := range []string{"seat1", "seat2", "seat3"} {
		seat := g.SeatByID(seatID)
		v := VisibleFor(g, sc, seat, VisibleOptions{})
		req := pb.Build(PromptInput{
			Visible:     v,
			Persona:     persona.Get(seat.PersonaID),
			Instruction: "现在轮到你发言。",
		})
		if len(req.Messages) != 1 {
			t.Fatalf("expected a single user message, got %d", len(req.Messages))
		}
		body := req.System + "\n" + req.Messages[0].Text
		forbidden := []string{fixtureTruthMethod, fixtureTruthSummary}
		for _, r := range sc.Roles {
			if r.ID == seat.RoleID {
				continue
			}
			forbidden = append(forbidden, r.Private.Script)
			for _, s := range r.Private.Secrets {
				forbidden = append(forbidden, s.Text)
			}
		}
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Errorf("seat %s prompt contains hidden text %q", seatID, f)
			}
		}
		ownScript := ""
		if v.OwnRole != nil {
			ownScript = v.OwnRole.Private.Script
		}
		if ownScript != "" && !strings.Contains(body, ownScript) {
			t.Errorf("seat %s prompt is missing its own script", seatID)
		}
		if seat.RoleID != "" && !strings.Contains(body, "【当前阶段】") {
			t.Errorf("seat %s prompt is missing the phase marker", seatID)
		}
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// A card game hides the deal only while it runs. Hiding it in the lobby made
// every other seat read back as "human:-" even though the host had just cast
// it, and the acceptance run rebuilt five rooms chasing that phantom.
func TestCardGameLobbyShowsAssignedRoles(t *testing.T) {
	g := fixtureGame()
	sc := fixtureScript()
	sc.Mode = domain.ModeCardGame
	g.Status = domain.RoomLobby
	if HiddenIdentities(g, sc) {
		t.Fatal("lobby hides the host's casting")
	}
	g.Status = domain.RoomRunning
	if !HiddenIdentities(g, sc) {
		t.Fatal("a running deal must stay secret")
	}
	g.PhaseIndex = len(sc.Phases) - 1 // reveal
	if HiddenIdentities(g, sc) {
		t.Fatal("the reveal opens the deal")
	}
}

// A nil slice marshals to `null`, which strict JSON clients reject. Every list a
// client renders must therefore arrive as an empty array when it is empty.
func TestVisibleNeverSerialisesNullLists(t *testing.T) {
	g := fixtureGame()
	sc := fixtureScript()
	// Fresh game: nothing found yet, no transcript, no facts.
	fresh := domain.NewGame("g2", "room2", "script2", g.Seats)
	fresh.PhaseIndex = 0
	for _, seat := range fresh.Seats {
		s := seat
		v := VisibleFor(fresh, sc, &s, VisibleOptions{MaxTranscript: 10})
		body := string(mustJSON(v))
		for _, field := range []string{"ownClues", "publicClues", "ownFacts", "publicRoles", "transcript", "seats"} {
			if strings.Contains(body, fmt.Sprintf("%q:null", field)) {
				t.Errorf("seat %s serialises %s as null: %s", seat.ID, field, body)
			}
		}
	}
}
