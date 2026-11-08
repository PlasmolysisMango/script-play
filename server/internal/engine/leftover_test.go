package engine

import (
	"testing"

	"script-play/internal/domain"
)

// leftoverScript is a search round with more cards than the table can draw and
// a printed rule that the narrator takes whatever is left.
func leftoverScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "剩卡测试本", PlayerCount: domain.CountRange{Min: 3, Max: 3}},
		Truth:         domain.Truth{KillerRoleID: "r2"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{
				ID: "p2", Type: domain.PhaseSearch, Name: "搜证", SearchMode: domain.SearchPick,
				CluePool: []string{"c1", "c2", "c3"}, PerPlayerPick: 1,
				LeftoverRoleID: "r3", HostExtraPicks: 2,
			},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
			{ID: "p4", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{ID: "r1", Name: "甲", Private: domain.RolePrivate{Script: "甲的本"}},
			{ID: "r2", Name: "乙", Private: domain.RolePrivate{Script: "乙的本"}},
			{ID: "r3", Name: "旁白", Private: domain.RolePrivate{Script: "旁白的本"}},
		},
		Clues: []domain.Clue{
			{ID: "c1", Title: "桌上的信", Content: "信纸是湿的。"},
			{ID: "c2", Title: "窗台", Content: "窗台有鞋印。", GrantsItem: "车票"},
			{ID: "c3", Title: "甲的口袋", Content: "口袋里有钥匙。", OwnerRoleID: "r1"},
		},
	}
}

func leftoverActor() (*Actor, *domain.Script, *domain.Game) {
	sc := leftoverScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "host", RoomID: "room1", Kind: domain.SeatHuman, DisplayName: "主持人", IsHost: true},
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "旁白"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	return NewActor(g, sc, nil, nil, nil, ActorConfig{}), sc, g
}

// The printed "剩下的归旁白" rule must hand every unclaimed pool card to the
// narrator when the round closes, without moving another player's private item.
func TestLeftoversGoToThePrintedRole(t *testing.T) {
	a, sc, g := leftoverActor()
	ph := &sc.Phases[1]

	if err := a.claimClue(g.Seats[1], "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	a.mu.Lock()
	a.assignLeftoversLocked(ph)
	a.mu.Unlock()

	if st := g.ClueState["c1"]; st.FoundBySeat != "seat1" {
		t.Fatalf("c1 moved away from its finder: %+v", st)
	}
	if st := g.ClueState["c2"]; st.FoundBySeat != "seat3" {
		t.Fatalf("leftover c2 = %+v, want narrator seat3", st)
	}
	if !g.HasItems("seat3", []string{"车票"}) {
		t.Fatal("leftover c2 did not grant its printed item to the narrator")
	}
	if st := g.ClueState["c3"]; st.FoundBySeat != "" {
		t.Fatalf("private item c3 was handed to the narrator: %+v", st)
	}
}

// If the printed recipient is not seated, the leftover cards still belong to
// the table: reveal them rather than dropping game material on the floor.
func TestLeftoversFallBackToPublicWhenRecipientIsMissing(t *testing.T) {
	sc := leftoverScript()
	g := domain.NewGame("g2", "room2", "sc1", []domain.Seat{
		{ID: "host", RoomID: "room2", Kind: domain.SeatHuman, DisplayName: "主持人", IsHost: true},
		{ID: "seat1", RoomID: "room2", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})

	a.mu.Lock()
	a.assignLeftoversLocked(&sc.Phases[1])
	a.mu.Unlock()
	if st := g.ClueState["c2"]; st.FoundBySeat != "" || !st.Revealed {
		t.Fatalf("leftover without a recipient = %+v, want public reveal", st)
	}
}

// The acting bonus is a host-only command: each grant adds one draw to the
// target and spends one of the phase's printed extra picks.
func TestHostGrantsExtraSearchPicks(t *testing.T) {
	a, sc, g := leftoverActor()
	ph := &sc.Phases[1]
	a.mu.Lock()
	a.hostExtraLeft = ph.HostExtraPicks
	a.searchOpen = true
	a.order = append([]domain.Seat(nil), g.Seats[1:]...)
	a.mu.Unlock()

	if err := a.Handle(t.Context(), "host", ClientMessage{Type: "search.grant", TargetID: "seat2", PhaseID: ph.ID}); err != nil {
		t.Fatalf("host grant: %v", err)
	}
	a.mu.Lock()
	limit := a.searchLimitLocked(g.Seats[2], ph)
	a.mu.Unlock()
	if limit != 2 {
		t.Fatalf("search limit after one grant = %d, want 2", limit)
	}

	if err := a.Handle(t.Context(), "seat1", ClientMessage{Type: "search.grant", TargetID: "seat2"}); err == nil {
		t.Fatal("a player was allowed to grant a search pick")
	}
	if err := a.Handle(t.Context(), "host", ClientMessage{Type: "search.grant", TargetID: "seat2", PhaseID: "p1"}); err == nil {
		t.Fatal("a grant for another phase was accepted")
	}
	if err := a.Handle(t.Context(), "host", ClientMessage{Type: "search.grant", TargetID: "seat2", PhaseID: ph.ID}); err != nil {
		t.Fatalf("second grant: %v", err)
	}
	if err := a.Handle(t.Context(), "host", ClientMessage{Type: "search.grant", TargetID: "seat2", PhaseID: ph.ID}); err == nil {
		t.Fatal("host granted past the printed budget")
	}
	// The two bonuses are real turns: the target may take both remaining cards.
	if err := a.claimClue(g.Seats[2], "c1", ph); err != nil {
		t.Fatalf("bonus pick 1: %v", err)
	}
	if err := a.claimClue(g.Seats[2], "c2", ph); err != nil {
		t.Fatalf("bonus pick 2: %v", err)
	}
}

// A grant's budget must also reach the host's own state snapshot; the host is
// the only seat that renders the bonus panel.
func TestHostStateCarriesExtraSearchBudget(t *testing.T) {
	a, sc, _ := leftoverActor()
	a.mu.Lock()
	a.hostExtraLeft = sc.Phases[1].HostExtraPicks
	a.mu.Unlock()

	host, ok := a.StateForSeat("host")
	if !ok {
		t.Fatal("host state missing")
	}
	if host.Visible.HostExtraLeft != sc.Phases[1].HostExtraPicks {
		t.Fatalf("host sees %d extra picks, want %d", host.Visible.HostExtraLeft, sc.Phases[1].HostExtraPicks)
	}
	player, _ := a.StateForSeat("seat1")
	if player.Visible.HostExtraLeft != 0 {
		t.Fatalf("player sees host-only budget: %d", player.Visible.HostExtraLeft)
	}
}

// Once the search walk closes, a late click must not burn the next round's
// budget or leave a bonus nobody will be prompted for.
func TestGrantClosesWithTheSearchLoop(t *testing.T) {
	a, sc, g := leftoverActor()
	ph := &sc.Phases[1]
	a.mu.Lock()
	a.hostExtraLeft = ph.HostExtraPicks
	a.searchOpen = false
	a.order = append([]domain.Seat(nil), g.Seats[1:]...)
	a.mu.Unlock()
	if err := a.Handle(t.Context(), "host", ClientMessage{Type: "search.grant", TargetID: "seat2", PhaseID: ph.ID}); err == nil {
		t.Fatal("grant accepted after the search walk closed")
	}
	a.mu.Lock()
	left := a.hostExtraLeft
	a.mu.Unlock()
	if left != ph.HostExtraPicks {
		t.Fatalf("late grant spent budget: %d left, want %d", left, ph.HostExtraPicks)
	}
}
