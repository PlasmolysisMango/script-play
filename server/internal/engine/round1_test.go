package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

func voteSeats() []domain.Seat {
	var seats []domain.Seat
	for i, roleID := range []string{"r1", "r2", "r3"} {
		seats = append(seats, domain.Seat{
			ID: "seat" + string(rune('1'+i)), RoomID: "room1", RoleID: roleID,
			Kind: domain.SeatHuman, DisplayName: "玩家" + roleID, IsHost: i == 0,
			Connected: true, JoinedAt: time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	return seats
}

func endingScript() *domain.Script {
	sc := fixtureScript()
	sc.Truth = domain.Truth{
		KillerRoleID: "r3",
		Summary:      "周叙关掉了灯塔电源。",
		Endings: []domain.TruthEnding{
			{ID: "e_r3", Name: "结局·周叙", KillerRoleID: "r3", Text: "周叙承认了自己的手法。"},
			{ID: "e_r2", Name: "结局·陆沉", KillerRoleID: "r2", Text: "陆沉被当成了凶手。"},
			{ID: "e_none", Name: "结局·悬案", Text: "没有人被定罪。"},
		},
		DefaultEndingID: "e_none",
	}
	return sc
}

// revealWith starts a room on the reveal phase with a preset ballot and
// returns the reveal event it produced.
func revealWith(t *testing.T, votes map[string]string) domain.Event {
	t.Helper()
	sc := endingScript()
	g := domain.NewGame("g1", "room1", "sc1", voteSeats())
	g.Status = domain.RoomRunning
	g.PhaseIndex = 4
	g.Votes = votes
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		for _, ev := range a.Snapshot().Events {
			if ev.Type == domain.EvReveal {
				return true
			}
		}
		return false
	})
	var reveal domain.Event
	for _, ev := range a.Snapshot().Events {
		if ev.Type == domain.EvReveal {
			reveal = ev
		}
	}
	return reveal
}

// The epilogue belongs to the ballot: the unique accused role picks one of the
// script's endings, and the other endings never reach the table.
func TestRevealPicksTheEndingTheVoteEarned(t *testing.T) {
	reveal := revealWith(t, map[string]string{"seat1": "r2", "seat2": "r2", "seat3": "r3"})
	if !strings.Contains(reveal.Text, "陆沉被当成了凶手") {
		t.Fatalf("reveal did not carry the accused role's ending:\n%s", reveal.Text)
	}
	if strings.Contains(reveal.Text, "周叙承认了自己的手法") || strings.Contains(reveal.Text, "没有人被定罪") {
		t.Fatalf("reveal leaked a different ending:\n%s", reveal.Text)
	}
	if reveal.Meta["endingId"] != "e_r2" || reveal.Meta["accusedRoleId"] != "r2" {
		t.Fatalf("reveal meta = %+v", reveal.Meta)
	}
}

// A tied ballot has no accused role, so the script's neutral ending is used.
func TestRevealTieFallsBackToDefaultEnding(t *testing.T) {
	reveal := revealWith(t, map[string]string{"seat1": "r2", "seat2": "r3", "seat3": ""})
	if !strings.Contains(reveal.Text, "没有人被定罪") {
		t.Fatalf("tied ballot did not get the default ending:\n%s", reveal.Text)
	}
	if strings.Contains(reveal.Text, "陆沉被当成了凶手") || strings.Contains(reveal.Text, "周叙承认了自己的手法") {
		t.Fatalf("tied ballot leaked a role ending:\n%s", reveal.Text)
	}
	if reveal.Meta["endingId"] != "e_none" || reveal.Meta["accusedRoleId"] != "" {
		t.Fatalf("reveal meta = %+v", reveal.Meta)
	}
}

func TestRevealAllAbstainUsesDefaultEnding(t *testing.T) {
	reveal := revealWith(t, map[string]string{"seat1": "", "seat2": "", "seat3": ""})
	if !strings.Contains(reveal.Text, "没有人被定罪") {
		t.Fatalf("all-abstain ballot did not get the default ending:\n%s", reveal.Text)
	}
	if reveal.Meta["endingId"] != "e_none" {
		t.Fatalf("reveal meta = %+v", reveal.Meta)
	}
}

// An accused role without its own ending still names the accused but uses the
// default epilogue instead of leaving the table without one.
func TestRevealUnknownAccusedKeepsDefaultEnding(t *testing.T) {
	reveal := revealWith(t, map[string]string{"seat1": "r1", "seat2": "r1", "seat3": "r1"})
	if !strings.Contains(reveal.Text, "没有人被定罪") {
		t.Fatalf("default ending missing:\n%s", reveal.Text)
	}
	if reveal.Meta["endingId"] != "e_none" || reveal.Meta["accusedRoleId"] != "r1" {
		t.Fatalf("reveal meta = %+v", reveal.Meta)
	}
}

// 「专属线索」 may print an item to its owner. The item has to reach the
// inventory at deal time, otherwise a later clue that needs it is unreachable.
func TestPrivateClueGrantsItsItem(t *testing.T) {
	sc := fixtureScript()
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c5", Title: "一张草稿", Content: "潦草的字迹。", OwnerRoleID: "r1", GrantsItem: "钥匙",
	})
	sc.Phases[1].PrivateClues = []string{"c5"}

	g := domain.NewGame("g1", "room1", "sc1", voteSeats())
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1 // search: the private clue is dealt on entry
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: 2 * time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "你获得了物品【钥匙】")
	})
	if !g.HasItems("seat1", []string{"钥匙"}) {
		t.Fatalf("items = %+v, want 钥匙 on seat1", g.Items)
	}
	v := VisibleFor(g, sc, &g.Seats[0], VisibleOptions{})
	if !contains(v.Items, "钥匙") {
		t.Fatalf("visible items = %+v", v.Items)
	}
	// The item name is table talk, so every seat may see who carries it; the
	// clue text behind it must stay private.
	other := VisibleFor(g, sc, &g.Seats[1], VisibleOptions{})
	var holder *PublicSeat
	for i := range other.Seats {
		if other.Seats[i].ID == "seat1" {
			holder = &other.Seats[i]
		}
	}
	if holder == nil || !contains(holder.Items, "钥匙") {
		t.Fatalf("other seat cannot see the holder's items: %+v", other.Seats)
	}
	for _, c := range other.OwnClues {
		if c.ID == "c5" {
			t.Fatalf("item name leaked its private clue: %+v", c)
		}
	}
}

// A 「可获得曲径通幽」 card is a bonus printed on a parent clue, not a draw: the
// table can still pick it up in the discussion after the search round closes.
func TestDeepDiveCardStaysClaimableInDiscussion(t *testing.T) {
	sc := fixtureScript()
	for i := range sc.Clues {
		if sc.Clues[i].ID == "c1" {
			sc.Clues[i].GrantsClue = []string{"c5"}
		}
	}
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c5", Title: "旧信", Content: "一封没寄出的信。", Back: "曲径通幽 1",
	})

	g := domain.NewGame("g1", "room1", "sc1", voteSeats())
	g.Status = domain.RoomRunning
	g.PhaseIndex = 2 // discussion
	g.ClueState["c1"] = domain.ClueState{ClueID: "c1", FoundBySeat: "seat1"}
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: 2 * time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.Visible.Phase.Type == domain.PhaseDiscussion
	})
	st, _ := a.StateForSeat("seat1")
	found := false
	for _, o := range st.Claimable {
		if o.ID == "c5" && o.Granted {
			found = true
		}
	}
	if !found {
		t.Fatalf("claimable = %+v, want c5", st.Claimable)
	}

	if err := a.Handle(ctx, "seat1", ClientMessage{Type: "search.clue", ClueID: "c5"}); err != nil {
		t.Fatalf("claim in discussion: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		got := a.Snapshot().ClueState["c5"]
		return got.FoundBySeat == "seat1"
	})
}

// An AI seat picks up its unlocked deep-dive cards on its own turn instead of
// leaving a mustReveal card stranded in an inventory nobody clicks.
func TestAgentClaimsGrantedCardOnItsTurn(t *testing.T) {
	sc := fixtureScript()
	for i := range sc.Clues {
		if sc.Clues[i].ID == "c1" {
			sc.Clues[i].GrantsClue = []string{"c5"}
		}
	}
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c5", Title: "旧信", Content: "一封没寄出的信。", Back: "曲径通幽 1",
	})

	g := fixtureGame() // seat2 is an agent
	g.ClueState["c1"] = domain.ClueState{ClueID: "c1", FoundBySeat: "seat2"}
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	a.agentClaimGranted(g.Seats[1], &sc.Phases[2])

	if got := g.ClueState["c5"]; got.FoundBySeat != "seat2" {
		t.Fatalf("c5 state = %+v, want seat2", got)
	}
}
