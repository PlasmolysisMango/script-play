package engine

import (
	"testing"

	"script-play/internal/domain"
)

// The script below is the smallest one that exercises every rule the framework
// gained for K11《再见苏西》: a public search round, a 搜身 card, an item-gated
// deep-dive chain and a keyword-triggered memory fragment.
func frameworkScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta: domain.Meta{
			Title:       "框架测试本",
			PlayerCount: domain.CountRange{Min: 3, Max: 3},
		},
		Truth: domain.Truth{KillerRoleID: "r3"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{
				ID: "p2", Type: domain.PhaseSearch, Name: "第一轮公开搜证", Round: 1,
				CluePool: []string{"c1", "c2", "c3", "c4"}, PerPlayerPick: 5,
				SearchMode: domain.SearchPick, RevealOnFind: true,
			},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
			{ID: "p4", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{ID: "r1", Name: "甲", Private: domain.RolePrivate{Script: "甲的本"}},
			{ID: "r2", Name: "乙", Private: domain.RolePrivate{Script: "乙的本"}},
			{ID: "r3", Name: "丙", Private: domain.RolePrivate{Script: "丙的本"}},
		},
		Clues: []domain.Clue{
			{ID: "c1", Title: "后院储藏室", Content: "箱子里少了一把钳子。", GrantsItem: "钳子"},
			{ID: "c2", Title: "地窖的电线", Content: "备用电源的线芯裸露。", RequiresItems: []string{"钳子"}, UnlockedBy: []string{"c1"}},
			{ID: "c3", Title: "甲的口袋", Content: "口袋里有一张车票。", SubjectRoleID: "r1"},
			{ID: "c4", Title: "记忆碎片 A·花瓶", Content: "你想起了那只带缺口的花瓶。", OwnerRoleID: "r1", Keywords: []string{"花瓶"}},
		},
	}
}

func frameworkActor() (*Actor, *domain.Script, *domain.Game) {
	sc := frameworkScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1 // the search phase
	return NewActor(g, sc, nil, nil, nil, ActorConfig{}), sc, g
}

func visibleClue(v Visible, id string) (domain.Clue, bool) {
	for _, list := range [][]domain.Clue{v.OwnClues, v.PublicClues} {
		for _, c := range list {
			if c.ID == id {
				return c, true
			}
		}
	}
	return domain.Clue{}, false
}

// A public search round opens every card on pickup: no second click, and the
// rest of the table can read it straight away.
func TestRevealOnFindPutsTheClueOnTheTable(t *testing.T) {
	a, sc, g := frameworkActor()
	seat := g.Seats[0]
	if err := a.claimClue(seat, "c1", &sc.Phases[1]); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	if st := g.ClueState["c1"]; !st.Revealed {
		t.Fatalf("revealOnFind phase left c1 private: %+v", st)
	}
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[1], VisibleOptions{}), "c1"); !ok {
		t.Fatal("another seat cannot see the clue found in a public round")
	}
	if !g.HasItems(seat.ID, []string{"钳子"}) {
		t.Fatalf("grantsItem did not land in the inventory: %+v", g.Items)
	}
	var announced bool
	for _, ev := range g.Events {
		if ev.Type == domain.EvClueRevealed && ev.ClueID == "c1" && ev.Visibility == domain.VisPublic {
			announced = true
		}
	}
	if !announced {
		t.Fatal("no public clue.revealed event was appended")
	}
}

// A 搜身 card names its subject: that seat may never draw it, everybody else
// still can.
func TestSubjectRoleCannotDrawItsOwnSearchClue(t *testing.T) {
	a, sc, g := frameworkActor()
	ph := &sc.Phases[1]

	for _, c := range a.availableCluesLocked(g.Seats[0], ph) {
		if c.ID == "c3" {
			t.Fatal("the subject of a 搜身 clue was offered her own card")
		}
	}
	if err := a.claimClue(g.Seats[0], "c3", ph); err == nil {
		t.Fatal("the subject managed to claim her own 搜身 clue")
	}

	var offered bool
	for _, c := range a.availableCluesLocked(g.Seats[1], ph) {
		if c.ID == "c3" {
			offered = true
		}
	}
	if !offered {
		t.Fatal("another seat was not offered the 搜身 clue")
	}
	if err := a.claimClue(g.Seats[1], "c3", ph); err != nil {
		t.Fatalf("another seat could not claim the 搜身 clue: %v", err)
	}
}

// The deep-dive chain needs both the item and the parent clue before the card
// exists at all.
func TestItemAndParentGateADeepDiveClue(t *testing.T) {
	a, sc, g := frameworkActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]
	deep := a.m.TokenClue("c2")

	if a.clueBlockedLocked(seat, deep) == "" {
		t.Fatal("c2 was searchable before the parent clue or the item")
	}
	if err := a.claimClue(seat, "c2", ph); err == nil {
		t.Fatal("server accepted a claim on a gated clue")
	}

	// c1 grants 钳子 and counts as the parent of c2.
	if err := a.claimClue(seat, "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	if reason := a.clueBlockedLocked(seat, deep); reason != "" {
		t.Fatalf("c2 still blocked after item+parent: %s", reason)
	}
	if err := a.claimClue(seat, "c2", ph); err != nil {
		t.Fatalf("claim c2: %v", err)
	}

	// The item is per seat: a seat that never found c1 still cannot open c2.
	if a.clueBlockedLocked(g.Seats[1], deep) == "" {
		t.Fatal("an item held by seat1 unlocked c2 for seat2")
	}
}

// A memory fragment starts sealed: even its owner cannot see it until somebody
// says the keyword; afterwards only the owner gets it.
func TestKeywordUnsealsAMemoryFragment(t *testing.T) {
	a, sc, g := frameworkActor()

	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], VisibleOptions{}), "c4"); ok {
		t.Fatal("sealed fragment leaked to its owner before the keyword")
	}

	a.mu.Lock()
	a.unlockKeywordsLocked("我提起那只带缺口的**花瓶**，他脸色就变了")
	a.mu.Unlock()

	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], VisibleOptions{}), "c4"); !ok {
		t.Fatal("fragment did not unlock for its owner after the keyword")
	}
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[1], VisibleOptions{}), "c4"); ok {
		t.Fatal("fragment became visible to the wrong seat")
	}
	for _, cid := range a.ownClueIDsLocked(g.Seats[0]) {
		if cid == "c4" {
			return
		}
	}
	t.Fatal("unlocked fragment is not revealable by its owner")
}

// Turning off revealOnFind keeps the old behaviour: the find stays private and
// only a public reveal event exposes it.
func TestPrivateSearchStillHidesTheFind(t *testing.T) {
	a, sc, g := frameworkActor()
	sc.Phases[1].RevealOnFind = false

	if err := a.claimClue(g.Seats[0], "c1", &sc.Phases[1]); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	if st := g.ClueState["c1"]; st.Revealed {
		t.Fatalf("private search revealed the card on pickup: %+v", st)
	}
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[1], VisibleOptions{}), "c1"); ok {
		t.Fatal("private find leaked to another seat")
	}
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], VisibleOptions{}), "c1"); !ok {
		t.Fatal("finder cannot see its own private clue")
	}
}

// A card that prints "本条线索必须公开" cannot stay in one hand, even in a
// round that is otherwise private.
func TestMustRevealCardCannotStayPrivate(t *testing.T) {
	sc := frameworkScript()
	sc.Phases[1].RevealOnFind = false
	sc.Phases[1].CluePool = []string{"c5"}
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c5", Title: "地窖里的白骨", Content: "这具白骨必须当众念出。", MustReveal: true,
	})
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})

	if err := a.claimClue(g.Seats[0], "c5", &sc.Phases[1]); err != nil {
		t.Fatalf("claim c5: %v", err)
	}
	if st := g.ClueState["c5"]; !st.Revealed {
		t.Fatalf("mustReveal card stayed private: %+v", st)
	}
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[1], VisibleOptions{}), "c5"); !ok {
		t.Fatal("mustReveal card was not shown to the table")
	}
}
