package engine

import (
	"strings"
	"testing"

	"script-play/internal/domain"
)

// drawScript is the K1 shape: one personal item plus two ship clues per seat,
// dealt out of the whole deck by kind rather than from a hand-written pool.
func drawScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "分类抽卡本", PlayerCount: domain.CountRange{Min: 2, Max: 2}},
		Truth:         domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{
				ID: "p2", Type: domain.PhaseSearch, Name: "凶案之后", SearchMode: domain.SearchPick,
				Draws: []domain.DrawRule{{Kind: "随身物品", Count: 1}, {Kind: "轮船线索", Count: 2}},
			},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
			{ID: "p4", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{ID: "r1", Name: "甲", Private: domain.RolePrivate{Script: "甲的本"}},
			{ID: "r2", Name: "乙", Private: domain.RolePrivate{Script: "乙的本"}},
		},
		Clues: []domain.Clue{
			{ID: "i1", Kind: "随身物品", Title: "怀表", Content: "表停在 8:30。"},
			{ID: "i2", Kind: "随身物品", Title: "钥匙", Content: "黄铜钥匙。"},
			{ID: "s1", Kind: "轮船线索", Title: "甲板血迹", Content: "栏杆上有血。"},
			{ID: "s2", Kind: "轮船线索", Title: "锅炉房", Content: "煤铲是湿的。"},
			{ID: "s3", Kind: "轮船线索", Title: "电报室", Content: "电报纸被撕走一页。"},
		},
	}
}

func drawActor() (*Actor, *domain.Script, *domain.Game) {
	sc := drawScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	return NewActor(g, sc, nil, nil, nil, ActorConfig{}), sc, g
}

// 「抽到自己的线索卡请放回卡堆内」的前提是牌堆里还有别的卡。只剩本人房间的
// 卡时，放回去也抽不到，这时应当直接拿走，否则末位座位会永远少一张。
func TestOwnRoomCardIsTakenOnceNothingElseIsLeft(t *testing.T) {
	sc := &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "自家房间", PlayerCount: domain.CountRange{Min: 1, Max: 1}},
		Truth:         domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseSearch, Name: "搜证", SearchMode: domain.SearchPick,
				Draws: []domain.DrawRule{{Kind: "房间", Count: 2}}},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
		},
		Roles: []domain.Role{{ID: "r1", Name: "甲"}},
		Clues: []domain.Clue{
			{ID: "a1", Kind: "房间", Title: "甲的房间", Content: "甲的抽屉。", SubjectRoleID: "r1"},
			{ID: "a2", Kind: "房间", Title: "甲的房间", Content: "甲的衣柜。", SubjectRoleID: "r1"},
		},
	}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	if got := len(a.availableCluesLocked(seat, ph)); got != 2 {
		t.Fatalf("available = %d, want 2：只剩本人房间的卡时也要能搜", got)
	}
	for i, id := range []string{"a1", "a2"} {
		if err := a.claimClue(seat, id, ph); err != nil {
			t.Fatalf("claim %d (%s): %v", i, id, err)
		}
	}
}

// The printed rule says "1 张随身物品 + 3 张轮船线索"; the order of the plan is
// what enforces it, so a seat cannot spend all four picks on the good deck.
func TestTypedDrawsKeepThePrintedMix(t *testing.T) {
	a, sc, g := drawActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	if got := ph.PickLimit(); got != 3 {
		t.Fatalf("PickLimit = %d, want 3", got)
	}
	for _, c := range a.availableCluesLocked(seat, ph) {
		if c.Kind != "随身物品" {
			t.Fatalf("first pick offered a %s card: %s", c.Kind, c.ID)
		}
	}
	if err := a.claimClue(seat, "s1", ph); err == nil {
		t.Fatal("the engine let the seat skip its item and take a ship clue first")
	}
	if err := a.claimClue(seat, "i1", ph); err != nil {
		t.Fatalf("claim item: %v", err)
	}
	for _, c := range a.availableCluesLocked(seat, ph) {
		if c.Kind != "轮船线索" {
			t.Fatalf("second pick offered a %s card: %s", c.Kind, c.ID)
		}
	}
	for _, id := range []string{"s1", "s2"} {
		if err := a.claimClue(seat, id, ph); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
	}
	if a.searched[seat.ID] != 3 {
		t.Fatalf("searched = %d, want the phase quota spent", a.searched[seat.ID])
	}
	if avail := a.availableCluesLocked(seat, ph); len(avail) != 0 {
		t.Fatalf("quota spent but %d cards still offered", len(avail))
	}
}

// The prompt has to say what the next click is, otherwise "还要抽 3 张" leaves
// the table guessing which deck is left.
func TestTypedDrawPromptNamesTheRemainingKinds(t *testing.T) {
	a, sc, g := drawActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	if got := a.pendingDrawsLocked(seat, ph); got != "随身物品 1 张、轮船线索 2 张" {
		t.Fatalf("prompt = %q", got)
	}
	if err := a.claimClue(seat, "i1", ph); err != nil {
		t.Fatalf("claim item: %v", err)
	}
	if got := a.pendingDrawsLocked(seat, ph); got != "轮船线索 2 张" {
		t.Fatalf("prompt after the item = %q", got)
	}
}

// A drawn round still honours every other gate: a kind that has run dry must not
// be silently swapped for another one.
func TestTypedDrawStopsWhenThatDeckIsEmpty(t *testing.T) {
	a, sc, g := drawActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]
	g.ClueState["i1"] = domain.ClueState{ClueID: "i1", FoundBySeat: "seat2"}
	g.ClueState["i2"] = domain.ClueState{ClueID: "i2", FoundBySeat: "seat2"}

	avail := a.availableCluesLocked(seat, ph)
	if len(avail) != 0 {
		ids := make([]string, 0, len(avail))
		for _, c := range avail {
			ids = append(ids, c.ID)
		}
		t.Fatalf("empty item deck still offered %s", strings.Join(ids, ","))
	}
}

// 搜身线索与「本人房间的卡」不是一回事：牌堆只剩写着自己名字的搜身卡时，
// 印规则仍然是「放回」，不能因为牌堆见底就把自己的物证摊给自己看。
func TestOwnPatDownCardIsNeverRelaxed(t *testing.T) {
	sc := &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "搜身", PlayerCount: domain.CountRange{Min: 1, Max: 1}},
		Truth:         domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseSearch, Name: "搜证", SearchMode: domain.SearchRandom,
				Draws: []domain.DrawRule{{Kind: "搜身线索", Count: 2}}},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
		},
		Roles: []domain.Role{{ID: "r1", Name: "甲"}, {ID: "r2", Name: "乙"}},
		Clues: []domain.Clue{
			{ID: "b1", Kind: "搜身线索", Title: "搜身线索·甲", Content: "袖口有血。", SubjectRoleID: "r1"},
			{ID: "b2", Kind: "搜身线索", Title: "搜身线索·乙", Content: "口袋里有钥匙。", SubjectRoleID: "r2"},
		},
	}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	for _, c := range a.availableCluesLocked(seat, ph) {
		if c.ID == "b1" {
			t.Fatal("自己身上的搜身线索被摆出来给本人抽了")
		}
	}
	// 把乙的卡抽走，牌堆只剩甲自己的搜身卡。
	g.ClueState["b2"] = domain.ClueState{ClueID: "b2", FoundBySeat: "seat2"}
	if got := a.availableCluesLocked(seat, ph); len(got) != 0 {
		t.Fatalf("牌堆只剩自己的搜身线索时还发了 %d 张", len(got))
	}
	if err := a.claimClue(seat, "b1", ph); err == nil {
		t.Fatal("本人把写着自己名字的搜身线索拿走了")
	}
}
