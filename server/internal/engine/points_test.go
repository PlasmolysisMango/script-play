package engine

import (
	"testing"

	"script-play/internal/domain"
)

// K6《后天魔之谜》的桌面规则：每人 11 根仙骨，抽一张花 1 根，每轮有上限，
// 顺序印死，五位仙主各有一个只能发动一次的技能。这份最小的本子把四类效果
// 都摆一遍，好让引擎的每一处结算都有测试兜着。
func pointsScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Meta: domain.Meta{
			Title:       "仙骨测试本",
			PlayerCount: domain.CountRange{Min: 3, Max: 3},
			StartPoints: 11,
			PointsLabel: "仙骨",
		},
		Truth: domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{
				ID: "p2", Type: domain.PhaseSearch, Name: "第一轮搜证", Round: 1,
				CluePool:      []string{"c1", "c2", "c3", "c4", "c5"},
				PerPlayerPick: 4, DrawCost: 1, SearchMode: domain.SearchPick,
				Order: domain.OrderFixed, OrderRoleIDs: []string{"r2", "r1", "r3"},
			},
			{ID: "p3", Type: domain.PhaseVote, Name: "投票"},
			{ID: "p4", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{
				ID: "r1", Name: "北照君", Private: domain.RolePrivate{Script: "甲的本"},
				Skill: &domain.Skill{
					ID: "sk1", Name: "君子如玉", Text: "发动 3 根仙骨，掠夺下一名抽卡道友的抽卡权。",
					Effect: domain.SkillStealDraws, Cost: 3, Target: "next",
				},
			},
			{
				ID: "r2", Name: "千秋", Private: domain.RolePrivate{Script: "乙的本"},
				Skill: &domain.Skill{
					ID: "sk2", Name: "随心所欲", Text: "只有自身仙骨为 6 根时可用，焚尽 6 根，毁去目标剩余的仙骨。",
					Effect: domain.SkillBurnPoints, ExactCost: 6, Target: "other",
				},
			},
			{
				ID: "r3", Name: "流兮", Private: domain.RolePrivate{Script: "丙的本"},
				Skill: &domain.Skill{
					ID: "sk3", Name: "情深不寿", Text: "发动 3 根仙骨，令北照君恢复 1 根仙骨。",
					Effect: domain.SkillGrantPoints, Cost: 3, TargetRoleID: "r1", Value: 1,
				},
			},
		},
		Clues: []domain.Clue{
			{ID: "c1", Title: "石室", Content: "一份染血的传位书。"},
			{ID: "c2", Title: "炼丹房", Content: "满地朱砂粉。"},
			{ID: "c3", Title: "藏珍阁", Content: "记载昙花樽的手记。"},
			{ID: "c4", Title: "入口长廊", Content: "地上躺着无数魔族的尸体。"},
			{ID: "c5", Title: "密室", Content: "碎石下有碎裂的琉璃。"},
		},
	}
}

func pointsActor() (*Actor, *domain.Script, *domain.Game) {
	sc := pointsScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "北照君"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "千秋"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "流兮"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	g.Points = map[string]int{"seat1": 11, "seat2": 11, "seat3": 11}
	return NewActor(g, sc, nil, nil, nil, ActorConfig{}), sc, g
}

// 抽一张花一根仙骨，钱不够就抽不动；抽不动时不能把回合吞掉。
func TestDrawSpendsOneBoneAndRefusesWhenBroke(t *testing.T) {
	a, sc, g := pointsActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	if err := a.claimClue(seat, "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	if got := g.Points[seat.ID]; got != 10 {
		t.Fatalf("仙骨 = %d, want 10", got)
	}

	g.Points[seat.ID] = 0
	if err := a.claimClue(seat, "c2", ph); err == nil {
		t.Fatal("一根仙骨都没有却抽到了卡")
	}
	if st := g.ClueState["c2"]; st.FoundBySeat != "" {
		t.Fatalf("抽卡失败后卡却被记成了已搜走：%+v", st)
	}
}

// 剩余次数要同时受「本轮上限」和「手中仙骨」两个约束。
func TestPicksLeftFollowsBothTheRoundAndThePurse(t *testing.T) {
	a, sc, g := pointsActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	if got := a.picksLeftLocked(seat, ph); got != 4 {
		t.Fatalf("11 根仙骨、上限 4 次，应剩 4 次，实得 %d", got)
	}
	g.Points[seat.ID] = 2
	if got := a.picksLeftLocked(seat, ph); got != 2 {
		t.Fatalf("只剩 2 根仙骨时最多抽 2 张，实得 %d", got)
	}
	g.Points[seat.ID] = 11
	a.searched[seat.ID] = 3
	if got := a.picksLeftLocked(seat, ph); got != 1 {
		t.Fatalf("已抽 3 张、上限 4 张，应剩 1 次，实得 %d", got)
	}
}

// 顺序印死时照 orderRoleIds 走，没列到的角色不占回合。
func TestFixedOrderFollowsThePrintedList(t *testing.T) {
	a, sc, g := pointsActor()
	ph := &sc.Phases[1]

	if !a.prepareOrder(nil, a.phaseGen, ph) {
		t.Fatal("fixed 顺序没排出来")
	}
	want := []string{"seat2", "seat1", "seat3"}
	if len(a.order) != len(want) {
		t.Fatalf("order = %v, want %v", a.orderNamesLocked(a.order), want)
	}
	for i, id := range want {
		if a.order[i].ID != id {
			t.Fatalf("第 %d 位 = %s，want %s", i+1, a.order[i].ID, id)
		}
	}
	_ = g
}

// 技能一：抢走下一名道友这一轮的抽卡权。
func TestStealDrawsTakesTheNextSeatsTurns(t *testing.T) {
	a, sc, g := pointsActor()
	ph := &sc.Phases[1]
	if !a.prepareOrder(nil, a.phaseGen, ph) {
		t.Fatal("fixed 顺序没排出来")
	}
	seat := g.Seats[0]        // 北照君
	target := g.Seats[2]      // 流兮，顺序里紧挨在他后面
	a.searched[target.ID] = 1 // 流兮已经抽过一张

	if err := a.useSkill(seat, ""); err != nil {
		t.Fatalf("发动君子如玉：%v", err)
	}
	if got := g.Points[seat.ID]; got != 8 {
		t.Fatalf("发动后仙骨 = %d, want 8", got)
	}
	if !g.SkillsUsed[seat.ID] {
		t.Fatal("发动过的技能没被记下来")
	}
	if left := a.picksLeftLocked(target, ph); left != 0 {
		t.Fatalf("目标还能抽 %d 张，抽卡权没被夺走", left)
	}
	if left := a.picksLeftLocked(seat, ph); left != 7 {
		t.Fatalf("自己应有 4+3 次抽卡权，实得 %d", left)
	}
	if err := a.useSkill(seat, ""); err == nil {
		t.Fatal("同一个技能发动了第二次")
	}
}

// 技能二：只有正好 6 根仙骨时才能烧，烧完对方归零。
func TestBurnPointsNeedsExactlySixBones(t *testing.T) {
	a, _, g := pointsActor()
	seat := g.Seats[1] // 千秋
	target := g.Seats[0]

	if err := a.useSkill(seat, target.ID); err == nil {
		t.Fatal("11 根仙骨时发动了「只有 6 根」的技能")
	}
	g.Points[seat.ID] = 6
	if err := a.useSkill(seat, target.ID); err != nil {
		t.Fatalf("6 根仙骨时发动被拒：%v", err)
	}
	if g.Points[seat.ID] != 0 {
		t.Fatalf("施术者仙骨 = %d, want 0", g.Points[seat.ID])
	}
	if g.Points[target.ID] != 0 {
		t.Fatalf("目标仙骨 = %d, want 0", g.Points[target.ID])
	}
}

// 技能三：定点补给，只认印在卡上的那个人。
func TestGrantPointsHealsTheNamedRole(t *testing.T) {
	a, _, g := pointsActor()
	seat := g.Seats[2] // 流兮
	g.Points["seat1"] = 4

	if err := a.useSkill(seat, ""); err != nil {
		t.Fatalf("发动情深不寿：%v", err)
	}
	if g.Points["seat1"] != 5 {
		t.Fatalf("北照君仙骨 = %d, want 5", g.Points["seat1"])
	}
	if g.Points[seat.ID] != 8 {
		t.Fatalf("流兮仙骨 = %d, want 8", g.Points[seat.ID])
	}
}

// 技能四：全线展示，一张也不留给出牌的人藏着。
func TestShowCluesPutsTheTargetsHandOnTheTable(t *testing.T) {
	a, sc, g := pointsActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]
	target := g.Seats[1]
	// 先让千秋摸两张牌。
	a.g.Points[target.ID] = 11
	if err := a.claimClue(target, "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	if err := a.claimClue(target, "c2", ph); err != nil {
		t.Fatalf("claim c2: %v", err)
	}

	seatSK := &domain.Skill{
		ID: "sk4", Name: "向死而生", Text: "要求任一道友向全员展示手中所有线索卡。",
		Effect: domain.SkillShowClues, AllCost: true, Target: "other", Public: true,
	}
	g.Seats[0].RoleID = "r1"
	sc.Roles[0].Skill = seatSK

	if err := a.useSkill(seat, target.ID); err != nil {
		t.Fatalf("发动向死而生：%v", err)
	}
	if g.Points[seat.ID] != 0 {
		t.Fatalf("发动后应烧光所有仙骨，还剩 %d", g.Points[seat.ID])
	}
	for _, id := range []string{"c1", "c2"} {
		if st := g.ClueState[id]; !st.Revealed {
			t.Fatalf("%s 没有向全员公开：%+v", id, st)
		}
	}
	// 旁观座位的可见范围里要有这两张。
	seen := 0
	for _, c := range VisibleFor(g, sc, &g.Seats[2], VisibleOptions{}).PublicClues {
		if c.ID == "c1" || c.ID == "c2" {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("第三人只看到 %d 张公开线索，want 2", seen)
	}
}

// 技能只在搜证环节能发；不在搜证时给一句人话。
func TestSkillOnlyFiresInsideASearchRound(t *testing.T) {
	a, sc, g := pointsActor()
	g.PhaseIndex = 2 // 投票

	err := a.useSkill(g.Seats[0], g.Seats[1].ID)
	if err == nil {
		t.Fatal("投票环节还能发动搜证技能")
	}
	if _, ok := err.(ErrNotAllowed); !ok {
		t.Fatalf("错误类型 = %T, want ErrNotAllowed", err)
	}
	_ = sc
}

// 主动结束本轮：剩余额度作废，仙骨留在自己手里。
func TestSearchPassKeepsTheBones(t *testing.T) {
	a, sc, g := pointsActor()
	ph := &sc.Phases[1]
	seat := g.Seats[0]

	if err := a.claimClue(seat, "c1", ph); err != nil {
		t.Fatalf("claim c1: %v", err)
	}
	a.searched[seat.ID] = ph.PickLimit() // 等价于收到 search.pass 后的状态
	if left := a.picksLeftLocked(seat, ph); left != 0 {
		t.Fatalf("结束本轮后还能抽 %d 张", left)
	}
	if g.Points[seat.ID] != 10 {
		t.Fatalf("结束本轮不该再扣仙骨，实得 %d", g.Points[seat.ID])
	}
}
