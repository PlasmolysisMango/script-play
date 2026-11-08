package engine

import (
	"context"
	"testing"

	"script-play/internal/domain"
)

// K5 deals 「查理的记忆」 to the cat alone: the printed rule says only Charlie
// may take those cards, so the phase must not walk the five wizards through an
// empty turn first.
func onlyRoleScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "单人抽卡", PlayerCount: domain.CountRange{Min: 2, Max: 2}},
		Truth:         domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseSearch, Name: "查理的记忆", SearchMode: domain.SearchPick,
				PerPlayerPick: 1, OnlyRoleIDs: []string{"r2"}, CluePool: []string{"m1", "m2"}},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
		},
		Roles: []domain.Role{
			{ID: "r1", Name: "甲"},
			{ID: "r2", Name: "查理"},
		},
		Clues: []domain.Clue{
			{ID: "m1", Kind: "查理的记忆", Title: "车祸", Content: "十字路口的车祸。"},
			{ID: "m2", Kind: "查理的记忆", Title: "女佣", Content: "床边落泪的女佣。"},
		},
	}
}

func TestOnlyRolePhaseKeepsThatSeatAlone(t *testing.T) {
	sc := onlyRoleScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	ph := &sc.Phases[1]

	if !a.prepareOrder(context.Background(), a.phaseGen, ph) {
		t.Fatal("prepareOrder returned false for a round_robin phase")
	}
	if len(a.order) != 1 || a.order[0].RoleID != "r2" {
		t.Fatalf("order = %v, want only 查理", a.orderNamesLocked(a.order))
	}
}

// A host-assigned round only deals to the named roles, so the order the host
// publishes is a permutation of those seats — not of the whole table.
func TestOnlyRoleHostOrderCoversJustThoseSeats(t *testing.T) {
	sc := onlyRoleScript()
	ph := &sc.Phases[1]
	ph.Order = domain.OrderHostAssigned
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})

	if err := a.applyOrder(ph, []string{"seat1", "seat2"}); err == nil {
		t.Fatal("主持人把不能行动的角色也排进顺序却通过了")
	}
	if err := a.applyOrder(ph, []string{"seat2"}); err != nil {
		t.Fatalf("只排能行动的角色被拒：%v", err)
	}
	if len(a.order) != 1 || a.order[0].ID != "seat2" {
		t.Fatalf("order = %s, want 玩家二", a.orderNamesLocked(a.order))
	}
	prompt := a.orderPromptLocked()
	if len(prompt.Seats) != 1 || prompt.Seats[0].SeatID != "seat2" {
		t.Fatalf("主持人被要求给 %d 个座位排序，want 1", len(prompt.Seats))
	}
}

// The pool alone would still let a stray request through, so claimClue also
// refuses seats the phase did not name.
func TestOnlyRoleCardsStayWithTheirOwner(t *testing.T) {
	sc := onlyRoleScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	ph := &sc.Phases[1]

	if err := a.claimClue(g.Seats[0], "m1", ph); err == nil {
		t.Fatal("巫师从查理的记忆里拿走了卡")
	}
	if got := len(a.availableCluesLocked(g.Seats[1], ph)); got != 2 {
		t.Fatalf("查理可抽 = %d, want 2", got)
	}
}
