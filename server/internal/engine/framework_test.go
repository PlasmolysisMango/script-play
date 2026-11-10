package engine

import (
	"context"
	"strings"
	"testing"
	"time"

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

// K 系列的牌堆 kind 是卡册名，搜身信息落在 location/title 上；退让时也必须
// 认出这些卡，不能把本人身上的物证发给本人。
func TestPatDownOfReadsLocationAndTitle(t *testing.T) {
	seat := domain.Seat{RoleID: "r1"}
	cases := []struct {
		name string
		clue domain.Clue
		want bool
	}{
		{"kind", domain.Clue{Kind: "搜身线索", SubjectRoleID: "r1"}, true},
		{"location", domain.Clue{Kind: "纤毫毕现", Location: "易水寒搜身线索", SubjectRoleID: "r1"}, true},
		{"title", domain.Clue{Kind: "微光", Title: "李斯楠搜身线索", SubjectRoleID: "r1"}, true},
		{"room", domain.Clue{Kind: "私人房间", Location: "西蒙的房间", SubjectRoleID: "r1"}, false},
		{"other", domain.Clue{Kind: "纤毫毕现", Location: "易水寒搜身线索", SubjectRoleID: "r2"}, false},
	}
	for _, tc := range cases {
		if got := patDownOf(&tc.clue, seat); got != tc.want {
			t.Errorf("%s: patDownOf = %v, want %v", tc.name, got, tc.want)
		}
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

// K6 的「说到 A 和 B 这两个词」：两个词不必同一句，但得都当众说过。
func TestKeywordsAllWaitsForEveryWord(t *testing.T) {
	sc := frameworkScript()
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c7", Title: "记忆碎片 B·亡青叶", Content: "你想起了亡青叶。",
		OwnerRoleID: "r1", Keywords: []string{"红烟深处", "亡青叶"}, KeywordsAll: true,
	})
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	own := VisibleOptions{}

	a.appendSpeech(g.Seats[1], "听说红烟深处三百年前就灭门了")
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], own), "c7"); ok {
		t.Fatal("只说了其中一个词就翻页了")
	}
	a.appendSpeech(g.Seats[2], "亡青叶这个名字你们听过吗？")
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], own), "c7"); !ok {
		t.Fatal("两个词都当众说过了，记忆还没翻页")
	}
}

// K10 的曲径通幽：底卡写着「获得这张线索，可开启曲径通幽 N」，那张 N 号卡
// 不在本轮牌堆里，但它就该摆到抽到底卡的人面前；别人则看不到也拿不走。
func TestGrantsClueHandsTheNamedCardToTheFinder(t *testing.T) {
	sc := frameworkScript()
	sc.Clues[0].GrantsClue = []string{"c5"}
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c5", Title: "曲径通幽 5", Content: "暗道一直通到花园墙外。",
		RequiresItems: []string{"钳子"}, UnlockedBy: []string{"c1"},
	})
	sc.Phases[1].CluePool = []string{"c1"}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	ph := &sc.Phases[1]

	for _, c := range a.availableCluesLocked(g.Seats[1], ph) {
		if c.ID == "c5" {
			t.Fatal("没抽到底卡的人却看到了指定的深入调查卡")
		}
	}
	if len(a.grantedOptionsLocked(g.Seats[1], ph)) != 0 {
		t.Fatal("没抽到底卡的人却拿到了点名入口")
	}
	if err := a.claimClue(g.Seats[1], "c5", ph); err == nil {
		t.Fatal("没抽到底卡的人直接把深入调查卡拿走了")
	}

	if err := a.claimClue(g.Seats[0], "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	var offered bool
	for _, o := range a.grantedOptionsLocked(g.Seats[0], ph) {
		if o.ID == "c5" && o.Granted {
			offered = true
		}
	}
	if !offered {
		t.Fatal("抽到底卡后，指定的深入调查卡没有摆到本人面前")
	}
	for _, c := range a.availableCluesLocked(g.Seats[0], ph) {
		if c.ID == "c5" {
			t.Fatal("深入调查卡不该混进牌堆，否则随机抽取会把它随手发出去")
		}
	}
	before := a.searched[g.Seats[0].ID]
	if err := a.claimClue(g.Seats[0], "c5", ph); err != nil {
		t.Fatalf("点名称取指定的深入调查卡被拒: %v", err)
	}
	if a.searched[g.Seats[0].ID] != before {
		t.Fatalf("深入调查是底卡的附赠，不该占本轮张数: %d -> %d", before, a.searched[g.Seats[0].ID])
	}
	if st := g.ClueState["c5"]; st.FoundBySeat != g.Seats[0].ID {
		t.Fatalf("c5 没有落到抽到底卡的人手里: %+v", st)
	}
}

// K10 的曲径通幽 06-10 印着「××专属线索」：进入阶段就该交到主人手里，
// 牌堆里没有它们，别人也永远拿不到。
func TestPrivateCluesGoStraightToTheirOwner(t *testing.T) {
	sc := frameworkScript()
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c7", Title: "甲的专属线索", Content: "一封只有甲见过的信。", OwnerRoleID: "r1",
	})
	sc.Phases[1].PrivateClues = []string{"c7"}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.mu.Lock()
	a.dealPrivateCluesLocked(&sc.Phases[1])
	a.mu.Unlock()

	if st := g.ClueState["c7"]; st.FoundBySeat != g.Seats[0].ID || st.Revealed {
		t.Fatalf("专属线索没有私发给主人: %+v", st)
	}
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[1], VisibleOptions{}), "c7"); ok {
		t.Fatal("专属线索漏给了别的座位")
	}
	if err := a.claimClue(g.Seats[1], "c7", &sc.Phases[1]); err == nil {
		t.Fatal("别人把专属线索抽走了")
	}
}

// TestGrantedCardStaysClaimableAfterTheTurnEnds：底卡点名的深入调查卡不占本轮
// 张数，但它要的物品往往在同轮别人抽完才凑齐。本人的 2/4/6 张抽完之后，这张
// 卡的入口必须还在，否则玩家会看到「可获得曲径通幽 N」却永远点不了。
func TestGrantedCardStaysClaimableAfterTheTurnEnds(t *testing.T) {
	sc := frameworkScript()
	sc.Clues[0].GrantsClue = []string{"c5"}
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c5", Title: "曲径通幽 5", Content: "钳子拧开暗门。", Kind: "深入调查",
		RequiresItems: []string{"钳子"}, UnlockedBy: []string{"c1"},
	})
	sc.Phases[1].CluePool = []string{"c1", "c2", "c3"}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	ph := &sc.Phases[1]

	if err := a.claimClue(g.Seats[0], "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	// 依次坐到下一位：配额记满，麦克风也交出去了。
	a.mu.Lock()
	a.searched[g.Seats[0].ID] = ph.PickLimit()
	a.floor = &Floor{SeatID: g.Seats[1].ID, PhaseID: ph.ID, ExpiresAt: time.Now().Add(time.Minute)}
	a.mu.Unlock()

	if len(a.grantedOptionsLocked(g.Seats[0], ph)) == 0 {
		t.Fatal("回合结束后，点名称取曲径通幽的入口消失了")
	}
	if err := a.Handle(context.Background(), g.Seats[0].ID, ClientMessage{Type: "search.clue", ClueID: "c5"}); err != nil {
		t.Fatalf("回合结束后点名称取被拒: %v", err)
	}
	if st := g.ClueState["c5"]; st.FoundBySeat != g.Seats[0].ID {
		t.Fatalf("c5 没有落到抽到底卡的人手里: %+v", st)
	}
	if a.searched[g.Seats[0].ID] != ph.PickLimit() {
		t.Fatalf("点名取卡不该再动配额: %d", a.searched[g.Seats[0].ID])
	}
}

// K6 的「当前两个阶段都已解锁」：记忆碎片可以串成一条链。
func TestKeywordChainWaitsForTheEarlierPages(t *testing.T) {
	sc := frameworkScript()
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c6", Title: "记忆碎片 C·它是谁", Content: "你解锁了全部的记忆。",
		OwnerRoleID: "r1", Keywords: []string{"天魔书"}, UnlockedBy: []string{"c4"},
	})
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	own := VisibleOptions{}

	a.appendSpeech(g.Seats[1], "那本天魔书到底在谁手上？")
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], own), "c6"); ok {
		t.Fatal("前一页还没翻开，后面那一页先解了")
	}
	a.appendSpeech(g.Seats[1], "她一直捧着那只带缺口的花瓶")
	a.appendSpeech(g.Seats[2], "天魔书在藏珍阁")
	if _, ok := visibleClue(VisibleFor(g, sc, &g.Seats[0], own), "c6"); !ok {
		t.Fatal("前一页翻开后，下一句关键词没能解锁下一页")
	}
}

// K12《娘娘千岁》带来的三件事：每个角色不同的起手分且分数保密、搜证次数按
// 恩宠值名次分配（9/8/7/6）、投票定罪扣分 / 翻牌侍寝加分，最后由「水落石出」
// 结算废后与冷宫。
func favorScript() (*domain.Script, *domain.Game) {
	sc := &domain.Script{
		SchemaVersion: 1,
		Meta: domain.Meta{
			Title:       "后宫测试本",
			PlayerCount: domain.CountRange{Min: 3, Max: 3},
			PointsLabel: "恩宠值",
			HidePoints:  true,
		},
		Truth: domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{
				ID: "p2", Type: domain.PhaseSearch, Name: "初次搜证",
				CluePool: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, PerPlayerPick: 1,
				SearchMode: domain.SearchPick, Order: domain.OrderFavor, PickByRank: []int{3, 2, 1},
			},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票定罪", VotePrompt: "谁害死了皇帝？", Penalty: 5},
			{ID: "p4", Type: domain.PhaseVote, Name: "翻牌侍寝", Reward: 5},
			{ID: "p5", Type: domain.PhaseVote, Name: "水落石出", SettleFavor: true, ThroneRoleID: "r1"},
			{ID: "p6", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{ID: "r1", Name: "皇后", Points: 4, Private: domain.RolePrivate{Script: "皇后的本"}},
			{ID: "r2", Name: "德妃", Points: 9, Private: domain.RolePrivate{Script: "德妃的本"}},
			{ID: "r3", Name: "淑妃", Points: 2, Private: domain.RolePrivate{Script: "淑妃的本"}},
		},
		Clues: []domain.Clue{
			{ID: "c1", Title: "一", Content: "一"},
			{ID: "c2", Title: "二", Content: "二"},
			{ID: "c3", Title: "三", Content: "三"},
			{ID: "c4", Title: "四", Content: "四"},
			{ID: "c5", Title: "五", Content: "五"},
			{ID: "c6", Title: "六", Content: "六"},
		},
	}
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三"},
	})
	g.Status = domain.RoomRunning
	return sc, g
}

func TestFavorPointsStartPerRoleAndStayHidden(t *testing.T) {
	sc, g := favorScript()
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.mu.Lock()
	g.Points = map[string]int{}
	if !a.hasPointPoolLocked() {
		t.Fatal("role.points 给了分数，引擎却没认出这是有分数的本")
	}
	for _, s := range g.Seats {
		g.Points[s.ID] = a.startPointsLocked(s)
	}
	a.mu.Unlock()
	if g.Points["seat2"] != 9 || g.Points["seat1"] != 4 {
		t.Fatalf("各宫起手分不对: %+v", g.Points)
	}

	// 自己的分数看得到，别人的看不到；结算后才摆上桌面。
	a.mu.Lock()
	mine := a.stateLocked(&g.Seats[1], false)
	a.mu.Unlock()
	if len(mine.TablePoints) != 1 || mine.TablePoints["seat2"] != 9 {
		t.Fatalf("未结算时不该看到别人的分数: %+v", mine.TablePoints)
	}
	a.mu.Lock()
	ph := &sc.Phases[4]
	a.settleFavorLocked(ph)
	all := a.stateLocked(&g.Seats[1], false)
	a.mu.Unlock()
	if len(all.TablePoints) != 3 {
		t.Fatalf("水落石出之后分数应该全公开: %+v", all.TablePoints)
	}
}

func TestSearchQuotaFollowsFavorRank(t *testing.T) {
	sc, g := favorScript()
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.mu.Lock()
	g.Points = map[string]int{}
	for _, s := range g.Seats {
		g.Points[s.ID] = a.startPointsLocked(s)
	}
	a.phaseGen = 0
	a.mu.Unlock()

	ph := &sc.Phases[1]
	if !a.prepareOrder(context.Background(), 0, ph) {
		t.Fatal("按恩宠值排序不该失败")
	}
	a.layoutQuotaLocked(ph)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.order[0].RoleID != "r2" {
		t.Fatalf("恩宠值最高的德妃应该先搜: %s", a.order[0].RoleID)
	}
	want := map[string]int{"seat2": 3, "seat1": 2, "seat3": 1}
	for _, s := range g.Seats {
		if got := a.searchLimitLocked(s, ph); got != want[s.ID] {
			t.Fatalf("%s 的搜证次数应为 %d，实际 %d", s.ID, want[s.ID], got)
		}
	}
}

// 连续投票（K12、K23）每一轮是独立的一轮：上一轮的票不能留到下一轮的计票面板上。
func TestEachVoteRoundStartsWithACleanBallot(t *testing.T) {
	sc, g := favorScript()
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.mu.Lock()
	g.Votes = map[string]string{"seat1": "r2", "seat2": "r2", "seat3": ""}
	g.PhaseIndex = 2 // p3 投票定罪
	a.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.beginPhase(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(g.Votes) != 0 {
		t.Fatalf("新一轮投票还带着上一轮的票: %+v", g.Votes)
	}
	if got := a.stateLocked(&g.Seats[0], false).VoteCount; got != 0 {
		t.Fatalf("计票人数应从 0 开始，实际 %d", got)
	}
}

func TestVotePenaltyAndRewardMovePoints(t *testing.T) {
	sc, g := favorScript()
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.mu.Lock()
	g.Points = map[string]int{}
	for _, s := range g.Seats {
		g.Points[s.ID] = a.startPointsLocked(s)
	}
	// 两票定德妃的罪，随后一票把侍寝给了皇后。
	g.Votes = map[string]string{"seat1": "r2", "seat3": "r2", "seat2": ""}
	a.applyVoteEffectLocked(&sc.Phases[2])
	if g.Points["seat2"] != 4 {
		t.Fatalf("定罪应扣 5 点: %d", g.Points["seat2"])
	}
	g.Votes = map[string]string{"seat1": "r1", "seat2": "r1", "seat3": ""}
	a.applyVoteEffectLocked(&sc.Phases[3])
	if g.Points["seat1"] != 9 {
		t.Fatalf("侍寝应加 5 点: %d", g.Points["seat1"])
	}
	a.mu.Unlock()
}

func TestSettleFavorDeposesAWeakQueen(t *testing.T) {
	sc, g := favorScript()
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.mu.Lock()
	g.Points = map[string]int{"seat1": 1, "seat2": 8, "seat3": 3}
	a.settleFavorLocked(&sc.Phases[4])
	events := a.g.Events
	a.mu.Unlock()
	last := events[len(events)-1].Text
	if !strings.Contains(last, "被废后") || !strings.Contains(last, "德妃") {
		t.Fatalf("皇后不是最高分时应被废后: %s", last)
	}
}
