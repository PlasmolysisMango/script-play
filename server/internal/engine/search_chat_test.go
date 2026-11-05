package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

// The transcript box stays usable during search: a line typed while the seat
// still owes a search action must land as speech instead of being swallowed by
// the search waiter.
func TestSpeechDuringSearchIsKept(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID:          "seat" + string(rune('1'+i)),
			RoomID:      "room1",
			RoleID:      roleID,
			Kind:        domain.SeatHuman,
			DisplayName: "玩家" + roleID,
			PersonaID:   "guarded",
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	actor := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 5 * time.Second,
		AgentTimeout: 2 * time.Second,
		AutoAdvance:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go actor.Start(ctx)

	// Opening advances on its own; wait until seat1 is the one being asked.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hasSystemEvent(actor.Snapshot(), "seat1", "轮到你搜证") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hasSystemEvent(actor.Snapshot(), "seat1", "轮到你搜证") {
		t.Fatalf("seat1 never got a search prompt: %+v", actor.Snapshot().Events)
	}

	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "speak.text", Text: "我先说两句：停电时我在走廊。"}); err != nil {
		t.Fatalf("speak during search: %v", err)
	}
	// The waiter goroutine appends the line, so poll instead of reading once.
	speechDeadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(speechDeadline) && !found {
		for _, ev := range actor.Snapshot().Events {
			if ev.Type == domain.EvSpeech && ev.SeatID == "seat1" && strings.Contains(ev.Text, "停电时我在走廊") {
				found = true
			}
		}
		if !found {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("speech typed during search was dropped: %+v", actor.Snapshot().Events)
	}

	// The turn is still open: the clue pick must still work. Public clues are
	// already known, so take the last private option in the list.
	st, ok := actor.StateForSeat("seat1")
	if !ok || len(st.Search) == 0 {
		t.Fatalf("search options missing after speech: %+v", st.Search)
	}
	pick := st.Search[len(st.Search)-1].ID
	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "search.clue", ClueID: pick}); err != nil {
		t.Fatalf("search after speech: %v", err)
	}
	// The pick is applied by the search waiter, so poll for the assignment.
	pickDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(pickDeadline) {
		if actor.Snapshot().ClueState[pick].FoundBySeat == "seat1" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("clue %s not assigned to seat1: %+v", pick, actor.Snapshot().ClueState[pick])
}

func TestClueRevealDuringSearchTurnIsKept(t *testing.T) {
	sc := fixtureScript()
	// Two picks per player keeps the turn open after the first pick.
	sc.Phases[1].PerPlayerPick = 2
	g := domain.NewGame("g1", "room1", "sc1", nil)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID:          "seat" + string(rune('1'+i)),
			RoomID:      "room1",
			RoleID:      roleID,
			Kind:        domain.SeatHuman,
			DisplayName: "玩家" + roleID,
			PersonaID:   "guarded",
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	actor := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 5 * time.Second,
		AgentTimeout: 2 * time.Second,
		AutoAdvance:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go actor.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !hasSystemEvent(actor.Snapshot(), "seat1", "轮到你搜证") {
		time.Sleep(10 * time.Millisecond)
	}
	if !hasSystemEvent(actor.Snapshot(), "seat1", "轮到你搜证") {
		t.Fatalf("seat1 never got a search prompt: %+v", actor.Snapshot().Events)
	}

	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "search.clue", ClueID: "c1"}); err != nil {
		t.Fatalf("search c1: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return actor.Snapshot().ClueState["c1"].FoundBySeat == "seat1" })

	// The seat still owes a pick, so this lands in the search waiter.
	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "clue.reveal", ClueID: "c1"}); err != nil {
		t.Fatalf("reveal during search: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		if !actor.Snapshot().ClueState["c1"].Revealed {
			return false
		}
		for _, ev := range actor.Snapshot().Events {
			if ev.Type == domain.EvClueRevealed && ev.ClueID == "c1" {
				return true
			}
		}
		return false
	})

	// The turn survived the side command: the second pick still goes through.
	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "search.clue", ClueID: "c3"}); err != nil {
		t.Fatalf("search c3 after reveal: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return actor.Snapshot().ClueState["c3"].FoundBySeat == "seat1" })
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func hasSystemEvent(g domain.Game, seatID, needle string) bool {
	for _, ev := range g.Events {
		if ev.Type == domain.EvSystem && ev.SeatID == seatID && strings.Contains(ev.Text, needle) {
			return true
		}
	}
	return false
}
