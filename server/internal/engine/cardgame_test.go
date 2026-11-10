package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

// cardScript is a tiny printed deck: two clue colours, one function card of
// each kind, the three skills and the four word cards of a two-role table.
func cardScript() *domain.Script {
	return &domain.Script{
		SchemaVersion: 1,
		Mode:          domain.ModeCardGame,
		Meta:          domain.Meta{Title: "卡牌本", PlayerCount: domain.CountRange{Min: 2, Max: 2}},
		Truth:         domain.Truth{KillerRoleID: "r2", Summary: "乙是凶手"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhasePlay, Name: "出牌"},
			{ID: "p3", Type: domain.PhaseScore, Name: "填积分卡"},
			{ID: "p4", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{ID: "r1", Letter: "A", Name: "甲", Private: domain.RolePrivate{Script: "甲的本", Keyword: "麻烦"}},
			{ID: "r2", Letter: "B", Name: "乙", Private: domain.RolePrivate{Script: "乙的本", Keyword: "真相"}},
		},
		CardGame: &domain.CardGame{
			Deal:       2,
			Points:     []int{10, 7, 4, 1, 0},
			KillerRank: 1,
			Cards: []domain.Card{
				{ID: "red1", Kind: domain.CardClue, Color: "现场", Number: 1, Title: "现场 1", Text: "掉了一半的牛奶。", First: true},
				{ID: "red2", Kind: domain.CardClue, Color: "现场", Number: 2, Title: "现场 2", Text: "剪坏的裙子。"},
				{ID: "blue1", Kind: domain.CardClue, Color: "短信", Number: 1, Title: "短信 1", Text: "你过界了。"},
				{ID: "blue3", Kind: domain.CardClue, Color: "短信", Number: 3, Title: "短信 3", Text: "我为你定做一部电影。"},
				{ID: "inspect", Kind: domain.CardInspect, Title: "调查身份", Text: "查看任意一位玩家的一个关键词"},
				{ID: "stall", Kind: domain.CardStall, Title: "陷入僵局", Text: "从已出牌堆挑两张非功能牌给任意一人"},
				{ID: "swap", Kind: domain.CardSwap, Title: "交换线索", Text: "与指定一人交换线索"},
				{ID: "stop", Kind: domain.CardStop, Color: "现场", Title: "天黑请停止搜索", Text: "下一位随意打出一张"},
				{ID: "recolor", Kind: domain.CardRecolor, Color: "现场", Title: "线索中断", Text: "指定一种花色"},
			},
			Words: []domain.CardWord{
				{RoleID: "r1", Word: "情爱"}, {RoleID: "r1", Word: "争夺"},
				// 乙的一张卡印着「需要公开」，发牌时就已当众念过，技能牌不该再念一遍。
				{RoleID: "r2", Word: "虚荣", Open: true}, {RoleID: "r2", Word: "堕落"},
			},
		},
	}
}

// cardActor builds a table with a hand-written layout so every assertion is
// about a rule, not about the shuffle.
func cardActor(t *testing.T, hand1, hand2 []string) (*Actor, *domain.Script, *domain.Game) {
	t.Helper()
	sc := cardScript()
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1
	g.Cards = &domain.CardState{
		Order:   []string{"seat1", "seat2"},
		Hands:   map[string][]string{"seat1": hand1, "seat2": hand2},
		Answers: map[string]domain.CardAnswer{},
	}
	return NewActor(g, sc, nil, nil, nil, ActorConfig{}), sc, g
}

func mustPlay(t *testing.T, a *Actor, seatID, cardID string, extra ClientMessage) bool {
	t.Helper()
	msg := extra
	msg.Type = "card.play"
	msg.CardID = cardID
	ended, err := a.handleCardCommand(*a.g.SeatByID(seatID), msg)
	if err != nil {
		t.Fatalf("play %s: %v", cardID, err)
	}
	return ended
}

// A malformed card.play must not buy a fresh timeout. The acceptance run saw
// one agent repeat the same target-less skill 388 times while each refusal
// reset the ninety-second clock; three refusals now hand the turn to auto-play.
func TestInvalidCardCommandsDoNotStallTheTurn(t *testing.T) {
	a, _, g := cardActor(t, []string{"stall", "red2"}, []string{"blue1", "red1"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	go a.awaitPlay(ctx, 0, *g.SeatByID("seat1"))
	waitFor(t, 2*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.waiters["seat1"] != nil
	})

	for i := 0; i < playInvalidLimit; i++ {
		if err := a.Handle(ctx, "seat1", ClientMessage{Type: "card.play", CardID: "stall"}); err != nil {
			t.Fatalf("invalid command %d: %v", i+1, err)
		}
	}
	waitFor(t, 3*time.Second, func() bool {
		for _, id := range g.Cards.Hands["seat1"] {
			if id == "stall" {
				return false
			}
		}
		return true
	})
	waitFor(t, 2*time.Second, func() bool {
		for _, ev := range a.Snapshot().Events {
			if strings.Contains(ev.Text, "连续无效出牌") {
				return true
			}
		}
		return false
	})
}

// A card is legal when it matches the pile's colour or number; black skill
// cards are always legal. Everything else is refused with a reason.
func TestCardPlaysFollowColourOrNumber(t *testing.T) {
	a, _, g := cardActor(t, []string{"red2", "blue3", "inspect"}, []string{"blue1", "red1"})

	mustPlay(t, a, "seat1", "red2", ClientMessage{})
	if _, err := a.handleCardCommand(*g.SeatByID("seat2"), ClientMessage{Type: "card.play", CardID: "blue1"}); err == nil {
		t.Fatal("短信 1 matches neither 现场 nor 2 but was accepted")
	}
	mustPlay(t, a, "seat2", "red1", ClientMessage{})
	// 1 matches the pile's number, 调查身份 is black: both legal.
	if !a.playableLocked(a.cardGame().CardByID("blue1"), a.topCardLocked(g.Cards), "") {
		t.Fatal("blue1 shares the pile's number and should be playable after red1")
	}
	top := a.cardGame().CardByID("blue1")
	if !a.playableLocked(a.cardGame().CardByID("blue3"), top, "") {
		t.Fatal("blue3 shares the 短信 colour and should be playable")
	}
	if a.playableLocked(a.cardGame().CardByID("red2"), top, "") {
		t.Fatal("red2 matches neither 短信 nor 1 but was offered")
	}
	if !a.playableLocked(a.cardGame().CardByID("inspect"), top, "") {
		t.Fatal("black skill cards are legal on any pile")
	}
}

// 调查身份 reads the target's still-covered word card and nothing else: the
// words land in a seat-scoped event, never on the public log.
func TestInspectTellsOnlyThePlayerWhoPlayedIt(t *testing.T) {
	a, _, g := cardActor(t, []string{"inspect", "red1"}, []string{"blue1"})
	mustPlay(t, a, "seat1", "inspect", ClientMessage{To: "seat2"})

	var mine, public string
	for _, ev := range g.Events {
		if strings.Contains(ev.Text, "堕落") {
			if ev.Visibility == SeatVisibility("seat1") {
				mine = ev.Text
			} else {
				public = ev.Text
			}
		}
	}
	if !strings.Contains(mine, "堕落") {
		t.Fatalf("private inspect event = %q", mine)
	}
	// 虚荣 already printed 需要公开: handing it out again would be a free second
	// word cards' worth of information.
	if strings.Contains(mine, "虚荣") {
		t.Fatalf("open word card was read out again: %q", mine)
	}
	if public != "" {
		t.Fatalf("word cards leaked to the table: %q", public)
	}
}

// 陷入僵局 moves two non-function cards out of the pile into the target's hand.
func TestStallHandsTwoDiscardedCardsToTheTarget(t *testing.T) {
	a, _, g := cardActor(t, []string{"stall", "red2"}, []string{"blue1"})
	g.Cards.Discard = []string{"red1", "blue1", "stop"}
	ended := mustPlay(t, a, "seat1", "stall", ClientMessage{To: "seat2"})
	if ended {
		t.Fatal("stall ended the turn before the two picks were made")
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.stall", CardIDs: []string{"stop", "red1"}}); err == nil {
		t.Fatal("a function card was accepted as a stall pick")
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.stall", CardIDs: []string{"red1", "blue1"}}); err != nil {
		t.Fatalf("stall picks: %v", err)
	}
	got := g.Cards.Hands["seat2"]
	if len(got) != 3 || !contains(got, "red1") || !contains(got, "blue1") {
		t.Fatalf("seat2 hand after stall = %v", got)
	}
	if g.Cards.Pending != nil {
		t.Fatal("pending stayed open after the picks")
	}
}

// 交换线索 takes a random card and hands one back.
func TestSwapTradesOneCardEachWay(t *testing.T) {
	a, _, g := cardActor(t, []string{"swap", "red2", "red1"}, []string{"blue1", "blue3"})
	ended := mustPlay(t, a, "seat1", "swap", ClientMessage{To: "seat2"})
	if ended {
		t.Fatal("swap ended the turn before a card came back")
	}
	p := g.Cards.Pending
	if p == nil || p.Gained == "" {
		t.Fatal("swap did not take a card from the target")
	}
	if !contains(g.Cards.Hands["seat1"], p.Gained) {
		t.Fatal("the taken card did not land in the player's hand")
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.give", CardID: p.Gained}); err == nil {
		t.Fatal("the freshly taken card was allowed straight back")
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.give", CardID: "red2"}); err != nil {
		t.Fatalf("give back: %v", err)
	}
	if !contains(g.Cards.Hands["seat2"], "red2") {
		t.Fatalf("seat2 hand = %v, want red2", g.Cards.Hands["seat2"])
	}
}

// 天黑请停止搜索 forces the next seat to play blind, and passing is only legal
// when the hand truly has nothing that matches.
func TestStopForcesARandomCardAndPassNeedsNoLegalCard(t *testing.T) {
	a, _, g := cardActor(t, []string{"stop", "red1"}, []string{"blue1", "red2"})
	mustPlay(t, a, "seat1", "stop", ClientMessage{})
	if !g.Cards.RandomNext {
		t.Fatal("stop did not flag the next seat")
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat2"), ClientMessage{Type: "card.play", CardID: "blue1"}); err == nil {
		t.Fatal("a seat forced to play blind chose its own card")
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat2"), ClientMessage{Type: "card.random"}); err != nil {
		t.Fatalf("random play: %v", err)
	}

	// seat1 still holds red1, which matches 现场: passing must be refused.
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.pass"}); err == nil {
		t.Fatal("pass accepted while a playable card was still in hand")
	}
}

// Emptying a hand records a place, and the score sheet follows the printed
// card: rank points, killer identity, killer player, every seat's identity.
func TestScoreCardFollowsThePrintedScoring(t *testing.T) {
	a, _, g := cardActor(t, []string{"red2"}, []string{"red1"})
	mustPlay(t, a, "seat1", "red2", ClientMessage{})
	if len(g.Cards.Finished) != 1 || g.Cards.Finished[0] != "seat1" {
		t.Fatalf("finished = %v", g.Cards.Finished)
	}
	mustPlay(t, a, "seat2", "red1", ClientMessage{})
	if len(g.Cards.Finished) != 2 {
		t.Fatalf("finished = %v, want the whole table out", g.Cards.Finished)
	}

	a.submitScore(*g.SeatByID("seat1"), domain.CardAnswer{
		KillerRoleID: "r2",
		KillerSeatID: "seat1", // wrong player
		Guesses:      map[string]string{"seat1": "r1", "seat2": "r2"},
	})
	a.submitScore(*g.SeatByID("seat2"), domain.CardAnswer{
		KillerRoleID: "r2",
		KillerSeatID: "seat2", // itself: 认定自己是凶手且最终确定是自己，不计分
		Guesses:      map[string]string{"seat1": "r1", "seat2": "r2"},
	})
	a.mu.Lock()
	a.tallyScoreLocked()
	a.mu.Unlock()

	if len(g.Cards.Result) != 2 {
		t.Fatalf("result = %+v", g.Cards.Result)
	}
	bySeat := map[string]domain.CardScore{}
	for _, sc := range g.Cards.Result {
		bySeat[sc.SeatID] = sc
	}
	// 第一名 +10，凶手身份 +5，指认凶手玩家错 -3，两个身份各 +5.
	if got := bySeat["seat1"]; got.RankPoints != 10 || got.KillerPoints != 5 || got.PlayerPoints != -3 || got.IdentityPoints != 10 || got.Total != 22 {
		t.Fatalf("seat1 score = %+v", got)
	}
	// 第二名 +7，凶手身份 +5，指认自己不计分，身份 +10.
	if got := bySeat["seat2"]; got.RankPoints != 7 || got.KillerPoints != 5 || got.PlayerPoints != 0 || got.IdentityPoints != 10 || got.Total != 22 {
		t.Fatalf("seat2 score = %+v", got)
	}
}

// A black skill card has no colour and no number, so the next seat keeps
// following the card underneath it — otherwise only skills could ever be played.
func TestSkillCardDoesNotChangeWhatTheTableFollows(t *testing.T) {
	a, _, g := cardActor(t, []string{"red2", "inspect"}, []string{"blue1", "red1", "blue3"})
	mustPlay(t, a, "seat1", "red2", ClientMessage{})
	if _, err := a.handleCardCommand(*g.SeatByID("seat2"), ClientMessage{Type: "card.play", CardID: "blue1"}); err == nil {
		t.Fatal("songs 1 matched neither 现场 nor 2 but was accepted")
	}
	mustPlay(t, a, "seat2", "red1", ClientMessage{})
	mustPlay(t, a, "seat1", "inspect", ClientMessage{To: "seat2"})

	base := a.matchBaseLocked(g.Cards)
	if base == nil || base.ID != "red1" {
		t.Fatalf("match base = %+v, want red1", base)
	}
	if !a.playableLocked(a.cardGame().CardByID("blue1"), base, "") {
		t.Fatal("blue1 shares the base number and should still be playable")
	}
	if a.playableLocked(a.cardGame().CardByID("blue3"), base, "") {
		t.Fatal("blue3 matches neither colour nor number but was offered")
	}
	mustPlay(t, a, "seat2", "blue1", ClientMessage{})
}

// 随机出牌 is the effect of 天黑请停止搜索: without that flag it would let any
// client throw a card away that the rules would refuse.
func TestRandomPlayNeedsTheStoppedSearchFlag(t *testing.T) {
	a, _, g := cardActor(t, []string{"red1", "red2"}, []string{"blue1"})
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.random"}); err == nil {
		t.Fatal("card.random was accepted with no stopped search")
	}
	mustPlay(t, a, "seat1", "red1", ClientMessage{})
	if _, err := a.handleCardCommand(*g.SeatByID("seat2"), ClientMessage{Type: "card.random"}); err == nil {
		t.Fatal("the next seat also played at random for free")
	}
}

// 交换线索 spends one card and must take one back. Playing it as the last card
// leaves nothing to give, so the effect is void instead of parking a pending
// step that nobody can ever finish.
func TestSwapAsTheLastCardFizzlesInsteadOfPending(t *testing.T) {
	a, _, g := cardActor(t, []string{"swap"}, []string{"red1", "blue1"})
	ended := mustPlay(t, a, "seat1", "swap", ClientMessage{To: "seat2"})
	if !ended {
		t.Fatal("a fizzled swap kept the turn open")
	}
	if g.Cards.Pending != nil {
		t.Fatalf("pending stayed open: %+v", g.Cards.Pending)
	}
	if len(g.Cards.Hands["seat2"]) != 2 {
		t.Fatalf("the target's hand changed on a void swap: %v", g.Cards.Hands["seat2"])
	}
	if len(g.Cards.Finished) != 1 || g.Cards.Finished[0] != "seat1" {
		t.Fatalf("finished = %v", g.Cards.Finished)
	}
}

// 线索中断 binds the next seat only. Once he passes, the forced colour must not
// jump over him onto the seat after that.
func TestForcedColourEndsWhenTheNextSeatPasses(t *testing.T) {
	a, _, g := cardActor(t, []string{"recolor", "red1"}, []string{"blue3"})
	mustPlay(t, a, "seat1", "recolor", ClientMessage{Color: "现场"})
	if g.Cards.ForcedColor != "现场" {
		t.Fatalf("forced colour = %q", g.Cards.ForcedColor)
	}
	if _, err := a.handleCardCommand(*g.SeatByID("seat2"), ClientMessage{Type: "card.pass"}); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if g.Cards.ForcedColor != "" {
		t.Fatalf("forced colour survived a pass: %q", g.Cards.ForcedColor)
	}
}

// A finished seat never draws again, so aiming a skill card at him would only
// park cards where they can never come back out.
func TestSkillCardsRefuseAFinishedTarget(t *testing.T) {
	a, _, g := cardActor(t, []string{"inspect", "red1"}, nil)
	g.Cards.Finished = []string{"seat2"}
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.play", CardID: "inspect", To: "seat2"}); err == nil {
		t.Fatal("a skill card aimed at a finished seat was accepted")
	}
	// With nobody left to aim at, the card still leaves the hand and just fizzles.
	if _, err := a.handleCardCommand(*g.SeatByID("seat1"), ClientMessage{Type: "card.play", CardID: "inspect"}); err != nil {
		t.Fatalf("skill with no target: %v", err)
	}
	if g.Cards.Pending != nil {
		t.Fatal("a targetless skill opened a pending step")
	}
}

// Until the reveal phase the seat list, the word window and the log all have to
// stay quiet about who is who: the score sheet pays for that answer.
func TestCardGameKeepsIdentitiesSecretUntilTheReveal(t *testing.T) {
	a, sc, g := cardActor(t, []string{"red1"}, []string{"red2"})
	mustPlay(t, a, "seat1", "red1", ClientMessage{})

	v := VisibleFor(g, sc, g.SeatByID("seat1"), VisibleOptions{})
	if v.Self.RoleName != "甲" {
		t.Fatalf("own role = %q", v.Self.RoleName)
	}
	for _, s := range v.Seats {
		if s.ID == "seat1" {
			continue
		}
		if s.RoleID != "" || s.RoleName != "" {
			t.Fatalf("other seat leaked its identity: %+v", s)
		}
	}
	if len(v.CardRoles) != len(sc.Roles) {
		t.Fatalf("public name cards = %d", len(v.CardRoles))
	}
	for _, line := range v.Transcript {
		if line.RoleID != "" {
			t.Fatalf("transcript line leaked roleId: %+v", line)
		}
	}

	g.PhaseIndex = 3 // reveal
	v = VisibleFor(g, sc, g.SeatByID("seat1"), VisibleOptions{})
	var seen bool
	for _, s := range v.Seats {
		if s.ID == "seat2" {
			seen = s.RoleName == "乙" && s.RoleID == "r2"
		}
	}
	if !seen {
		t.Fatal("the reveal phase did not restore the identities")
	}
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
