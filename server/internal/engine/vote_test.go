package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

// The sequential vote walk only prompts one seat at a time, but any player may
// cast a ballot as soon as the phase opens. An early ballot must be accepted
// and the walk must not prompt that seat twice.
func TestVoteAcceptsEarlyBallots(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID:          "seat" + string(rune('1'+i)),
			RoomID:      "room1",
			RoleID:      roleID,
			Kind:        domain.SeatHuman,
			DisplayName: "玩家" + roleID,
			IsHost:      i == 0,
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	g.PhaseIndex = 3 // vote
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: 5 * time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	// seat1 owns the first prompt; seat2 votes from the sideline.
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "投票阶段")
	})
	if err := a.Handle(ctx, "seat2", ClientMessage{Type: "vote.cast", RoleID: "r3"}); err != nil {
		t.Fatalf("early vote: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return a.Snapshot().Votes["seat2"] == "r3" })
	if st, _ := a.StateForSeat("seat2"); !st.Voted {
		t.Fatal("seat2 state does not report its early ballot")
	}
	if st, _ := a.StateForSeat("seat1"); st.Voted {
		t.Fatal("seat1 has not voted yet")
	}

	if err := a.Handle(ctx, "seat1", ClientMessage{Type: "vote.cast", RoleID: "r3"}); err != nil {
		t.Fatalf("seat1 vote: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat3", "投票阶段")
	})
	if err := a.Handle(ctx, "seat3", ClientMessage{Type: "vote.cast", RoleID: "r1"}); err != nil {
		t.Fatalf("seat3 vote: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})
	if got := a.Snapshot().Votes["seat2"]; got != "r3" {
		t.Fatalf("seat2 ballot = %q want r3", got)
	}

	// A repeated click after the ballot is sealed is a no-op, not an overwrite.
	if err := a.Handle(ctx, "seat2", ClientMessage{Type: "vote.cast", RoleID: "r1"}); err == nil {
		t.Fatal("late duplicate vote should be refused once the ballot is sealed")
	}
	if got := a.Snapshot().Votes["seat2"]; got != "r3" {
		t.Fatalf("duplicate vote overwrote the ballot: %q", got)
	}
}

// A finished game is still a readable final state: the last phase reports done
// and the ballot survives the reveal instead of looking like nobody voted.
func TestFinishedGameKeepsFinalStateConsistent(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID:          "seat" + string(rune('1'+i)),
			RoomID:      "room1",
			RoleID:      roleID,
			Kind:        domain.SeatHuman,
			DisplayName: "玩家" + roleID,
			IsHost:      i == 0,
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	g.PhaseIndex = 4 // reveal
	g.Votes = map[string]string{"seat1": "r3", "seat2": "r3", "seat3": ""}
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})
	if err := a.Handle(ctx, "seat1", ClientMessage{Type: "host.skip"}); err != nil {
		t.Fatalf("finish reveal: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return a.Snapshot().Status == domain.RoomFinished })

	st, ok := a.StateForSeat("seat1")
	if !ok {
		t.Fatal("finished seat state missing")
	}
	if !st.PhaseDone {
		t.Fatal("finished game still reports the last phase as not done")
	}
	if st.VoteCount != 3 {
		t.Fatalf("finished game lost the ballot: voteCount = %d", st.VoteCount)
	}
	if st.Votes["r3"] != 2 {
		t.Fatalf("finished tally = %+v, want two votes for r3", st.Votes)
	}
}

// A table can vote for somebody who is not seated: K18's culprit is the NPC
// 阿飞, who has no role button. The ballot is stored as "absent:<name>" and the
// tally spells it out instead of printing the raw key.
func TestAbsentVoteTargetsAnNPC(t *testing.T) {
	sc := fixtureScript()
	sc.Truth = domain.Truth{KillerKind: domain.KillerKindNPC, KillerName: "阿飞", Summary: "阿飞投毒。"}
	g := domain.NewGame("g1", "room1", "sc1", nil)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID:          "seat" + string(rune('1'+i)),
			RoomID:      "room1",
			RoleID:      roleID,
			Kind:        domain.SeatHuman,
			DisplayName: "玩家" + roleID,
			IsHost:      i == 0,
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	g.PhaseIndex = 3 // vote
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: 5 * time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	// Wait for the phase to open: beginPhase resets the ballot, so a vote cast
	// before the prompt would be wiped.
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "投票阶段")
	})
	target := domain.VoteAbsentPrefix + "阿飞"
	if err := a.Handle(ctx, "seat1", ClientMessage{Type: "vote.cast", RoleID: target}); err != nil {
		t.Fatalf("absent vote: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return a.Snapshot().Votes["seat1"] == target })

	// An empty name is not an absent target, and an unknown role id is still
	// rejected: only real seats or a named absent person can be voted for.
	if err := a.Handle(ctx, "seat2", ClientMessage{Type: "vote.cast", RoleID: domain.VoteAbsentPrefix}); err == nil {
		t.Fatal("empty absent name should be refused")
	}
	if err := a.Handle(ctx, "seat2", ClientMessage{Type: "vote.cast", RoleID: "r9"}); err == nil {
		t.Fatal("unknown role should be refused")
	}

	if err := a.Handle(ctx, "seat2", ClientMessage{Type: "vote.cast", RoleID: "r1"}); err != nil {
		t.Fatalf("seat2 vote: %v", err)
	}
	if err := a.Handle(ctx, "seat3", ClientMessage{Type: "vote.cast", RoleID: "r1"}); err != nil {
		t.Fatalf("seat3 vote: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})

	snap := a.Snapshot()
	var castText, resultText string
	for _, e := range snap.Events {
		switch {
		case e.Type == domain.EvVote && e.SeatID == "seat1":
			castText = e.Text
		case e.Type == domain.EvVoteResult:
			resultText = e.Text
		}
	}
	if !strings.Contains(castText, "阿飞（不在座）") {
		t.Fatalf("seat1 cast text = %q", castText)
	}
	if !strings.Contains(resultText, "阿飞（不在座） 1 票") {
		t.Fatalf("vote result = %q", resultText)
	}
	if st, _ := a.StateForSeat("seat1"); st.Votes[target] != 1 {
		t.Fatalf("seat state tally = %+v", st.Votes)
	}
}

// The reveal spells out every case of a multi-case script, so a table that
// solved one case is not told it was wrong by a union-only verdict.
func TestRevealListsPerCaseTruth(t *testing.T) {
	sc := fixtureScript()
	sc.Truth = domain.Truth{
		KillerRoleID: "r3",
		KillerRoleIDs: []string{
			"r3", "r2",
		},
		KillerKind: domain.KillerKindMultiple,
		Summary:    "两案各有真凶。",
		Cases: []domain.TruthCase{
			{ID: "c1", Name: "第一案", KillerRoleID: "r3"},
			{ID: "c2", Name: "第二案", KillerRoleID: "r2"},
		},
	}
	g := domain.NewGame("g1", "room1", "sc1", nil)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID: "seat" + string(rune('1'+i)), RoomID: "room1", RoleID: roleID,
			Kind: domain.SeatHuman, DisplayName: "玩家" + roleID, IsHost: i == 0,
			Connected: true, JoinedAt: time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	g.PhaseIndex = 4 // reveal
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})
	var reveal domain.Event
	for _, e := range a.Snapshot().Events {
		if e.Type == domain.EvReveal {
			reveal = e
		}
	}
	if !strings.Contains(reveal.Text, "第一案：凶手是 周叙") {
		t.Fatalf("reveal text = %q", reveal.Text)
	}
	if !strings.Contains(reveal.Text, "第二案：凶手是 陆沉") {
		t.Fatalf("reveal text = %q", reveal.Text)
	}
	if reveal.Meta["cases"] != "2" {
		t.Fatalf("reveal meta = %+v", reveal.Meta)
	}
}
