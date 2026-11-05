package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

func strategySeats(ids ...string) []domain.Seat {
	out := make([]domain.Seat, 0, len(ids))
	for i, id := range ids {
		out = append(out, domain.Seat{
			ID:          id,
			RoomID:      "room1",
			RoleID:      []string{"r1", "r2", "r3"}[i],
			Kind:        domain.SeatHuman,
			DisplayName: "玩家" + id,
			PersonaID:   "guarded",
			Connected:   true,
			IsHost:      i == 0,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func waitEvent(t *testing.T, actor *Actor, seatID, needle string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hasSystemEvent(actor.Snapshot(), seatID, needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("seat %s never saw %q: %+v", seatID, needle, actor.Snapshot().Events)
}

// Random search mode: naming a clue is refused, and "随机抽取" hands one over
// without the seat ever seeing the list.
func TestRandomSearchDrawsInsteadOfPicking(t *testing.T) {
	sc := fixtureScript()
	sc.Phases[1].SearchMode = domain.SearchRandom
	sc.Phases[1].PerPlayerPick = 2
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Seats = strategySeats("seat1", "seat2", "seat3")

	actor := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 5 * time.Second,
		AgentTimeout: 2 * time.Second,
		AutoAdvance:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go actor.Start(ctx)

	waitEvent(t, actor, "seat1", "轮到你搜证")

	// A hand-picked clue must bounce: otherwise the pool could be steered.
	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "search.clue", ClueID: "c1"}); err != nil {
		t.Fatalf("search.clue: %v", err)
	}
	waitEvent(t, actor, "seat1", "随机抽取")
	if got := actor.Snapshot().ClueState["c1"].FoundBySeat; got != "" {
		t.Fatalf("random mode accepted a hand pick: c1 -> %q", got)
	}

	st, ok := actor.StateForSeat("seat1")
	if !ok {
		t.Fatal("seat1 state missing")
	}
	if len(st.Search) != 0 {
		t.Fatalf("random mode leaked the clue list: %+v", st.Search)
	}
	if st.Draw != 2 {
		t.Fatalf("draw budget = %d, want 2", st.Draw)
	}

	// Two draws in a row, both inside the same turn.
	for i := 0; i < 2; i++ {
		if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "search.draw"}); err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		waitFor(t, 3*time.Second, func() bool { return drawnBy(actor.Snapshot(), "seat1") == i+1 })
	}

	// The turn is spent, so the order moves on to the next seat.
	waitEvent(t, actor, "seat2", "轮到你搜证")
}

// Host-assigned order: the phase parks until the host submits a full
// permutation, and the engine walks that order afterwards.
func TestHostAssignedOrderWaitsForHost(t *testing.T) {
	sc := fixtureScript()
	sc.Phases[1].Order = domain.OrderHostAssigned
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Seats = strategySeats("seat1", "seat2", "seat3")

	actor := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 5 * time.Second,
		AgentTimeout: 2 * time.Second,
		AutoAdvance:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go actor.Start(ctx)

	waitFor(t, 5*time.Second, func() bool {
		st, ok := actor.StateForSeat("seat1")
		return ok && st.Prompt != nil && st.Prompt.Kind == "order"
	})

	st, _ := actor.StateForSeat("seat1")
	if len(st.Prompt.Seats) != 3 {
		t.Fatalf("order prompt lists %d seats, want 3", len(st.Prompt.Seats))
	}

	// A short list would strand a seat, so it is rejected.
	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "order.set", SeatIDs: []string{"seat3"}}); err != nil {
		t.Fatalf("order.set short: %v", err)
	}
	waitEvent(t, actor, "seat1", "请把 3 个玩家全部排进顺序里")

	// The real layout: reverse of the seating order.
	if err := actor.Handle(ctx, "seat1", ClientMessage{Type: "order.set", SeatIDs: []string{"seat3", "seat2", "seat1"}}); err != nil {
		t.Fatalf("order.set: %v", err)
	}
	waitEvent(t, actor, "", "本阶段顺序（主持人指定）")

	applied, _ := actor.StateForSeat("seat1")
	if strings.Join(applied.Order, ",") != "seat3,seat2,seat1" {
		t.Fatalf("applied order = %v", applied.Order)
	}
	// seat3 is first in the layout, so it gets the opening turn.
	waitEvent(t, actor, "seat3", "轮到你搜证")
}

// Random order: a fresh permutation of the same players, announced once.
func TestRandomOrderShufflesPlayers(t *testing.T) {
	sc := fixtureScript()
	sc.Phases[1].Order = domain.OrderRandom
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Seats = strategySeats("seat1", "seat2", "seat3")

	actor := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 5 * time.Second,
		AgentTimeout: 2 * time.Second,
		AutoAdvance:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go actor.Start(ctx)

	waitEvent(t, actor, "", "本阶段顺序（随机）")

	st, _ := actor.StateForSeat("seat1")
	if len(st.Order) != 3 {
		t.Fatalf("random order = %v, want three seats", st.Order)
	}
	seen := map[string]int{}
	for _, id := range st.Order {
		seen[id]++
	}
	for _, id := range []string{"seat1", "seat2", "seat3"} {
		if seen[id] != 1 {
			t.Fatalf("random order %v is not a permutation (seat %s)", st.Order, id)
		}
	}
}

func drawnBy(g domain.Game, seatID string) int {
	n := 0
	for _, ev := range g.Events {
		if ev.Type == domain.EvClueFound && ev.SeatID == seatID {
			n++
		}
	}
	return n
}
