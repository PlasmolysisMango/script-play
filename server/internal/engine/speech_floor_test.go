package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

func humanActor(t *testing.T, floor time.Duration, waitForHost, unlimited bool) *Actor {
	t.Helper()
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
			IsHost:      i == 0,
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: floor,
		AgentTimeout: 2 * time.Second,
		WaitForHost:  waitForHost,
		Unlimited:    unlimited,
		// These cases watch the per-seat clock, so let the fixture walk the
		// script on its own instead of parking at every host gate.
		AutoAdvance: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)
	return a
}

// Default rooms advance on their own: a silent seat is skipped.
func TestSpeechTimeoutSkipsByDefault(t *testing.T) {
	a := humanActor(t, 150*time.Millisecond, false, false)
	waitFor(t, 5*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "未在时限内发言，跳过")
	})
}

// Rooms created with "等主持人推进" keep the floor after the deadline, and the
// host's skip must free the waiter instead of leaving it stuck on a dead channel.
func TestSpeechTimeoutWaitsForHost(t *testing.T) {
	a := humanActor(t, 150*time.Millisecond, true, false)

	waitFor(t, 5*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "轮到你发言")
	})
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "等待主持人推进")
	})

	snap := a.Snapshot()
	if snap.PhaseIndex != 2 {
		t.Fatalf("phase moved on without the host: index %d", snap.PhaseIndex)
	}
	for _, ev := range snap.Events {
		if strings.Contains(ev.Text, "未在时限内发言") {
			t.Fatalf("seat was skipped even though the room waits for the host: %q", ev.Text)
		}
	}

	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "host.skip"}); err != nil {
		t.Fatalf("host skip: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "投票阶段")
	})

	// A stale speech waiter would swallow this vote, so recording it proves the
	// waiter was released when the phase advanced.
	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "vote.cast", RoleID: "r3"}); err != nil {
		t.Fatalf("vote after host skip: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return a.Snapshot().Votes["seat1"] == "r3"
	})
}

// A room created with "不限时" never puts a seat on a clock: the waiters park on
// the message channel past the configured timeout, and the turn only ends when
// the seat acts or the host skips the phase.
func TestUnlimitedFloorNeverTimesOut(t *testing.T) {
	a := humanActor(t, 120*time.Millisecond, false, true)

	// The fixture opens with a search phase, which is a waiter of its own.
	waitFor(t, 5*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "轮到你搜证")
	})
	time.Sleep(400 * time.Millisecond)
	assertNoEvent(t, a.Snapshot(), "搜证超时")

	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "host.skip"}); err != nil {
		t.Fatalf("host skip: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "轮到你发言（不限时）")
	})
	time.Sleep(400 * time.Millisecond)
	assertNoEvent(t, a.Snapshot(), "未在时限内发言")

	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "speak.text", Text: "我先说说自己昨晚的行踪"}); err != nil {
		t.Fatalf("speech in unlimited room: %v", err)
	}
	// The turn returns to the scheduler: the next seat gets the floor.
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat2", "轮到你发言")
	})
}

// Rooms are host-driven by default: a phase whose actions are done parks the
// table (phaseDone) and only the host's skip steps into the next one.
func TestPhaseWaitsForHostByDefault(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Seats = append(g.Seats, domain.Seat{
		ID:          "seat1",
		RoomID:      "room1",
		RoleID:      "r1",
		Kind:        domain.SeatHuman,
		DisplayName: "玩家r1",
		IsHost:      true,
		Connected:   true,
		JoinedAt:    time.Now(),
	})
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go a.Start(ctx)

	// The fixture opens with an opening phase, which has no actions to collect.
	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})
	time.Sleep(300 * time.Millisecond)
	if idx := a.Snapshot().PhaseIndex; idx != 0 {
		t.Fatalf("phase advanced without the host: index %d", idx)
	}
	waitFor(t, 3*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "", "等待主持人进入下一阶段")
	})

	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "host.skip"}); err != nil {
		t.Fatalf("host skip: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return a.Snapshot().PhaseIndex == 1 })
	waitFor(t, 3*time.Second, func() bool {
		st, _ := a.StateForSeat("seat1")
		return !st.PhaseDone
	})
}

// Advancing is always triggered by a client frame, and a websocket request
// context is dead as soon as that frame is handled. A phase that inherited it
// would cancel every waiter immediately and spin the turn walk, spamming one
// prompt per seat instead of waiting for the floor.
func TestPhaseLoopOutlivesTheAdvancingRequest(t *testing.T) {
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
			IsHost:      i == 0,
			Connected:   true,
			JoinedAt:    time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 90 * time.Second,
		AgentTimeout: 2 * time.Second,
		Unlimited:    true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})

	// The opening phase is released by the host, whose request context ends
	// right after the frame: the search phase must still get a live context.
	requestCtx, endRequest := context.WithCancel(context.Background())
	if err := a.Handle(requestCtx, "seat1", ClientMessage{Type: "host.skip"}); err != nil {
		t.Fatalf("host skip: %v", err)
	}
	endRequest()

	waitFor(t, 3*time.Second, func() bool {
		return countSystem(a.Snapshot(), "seat1", "轮到你搜证") == 1
	})
	time.Sleep(300 * time.Millisecond)
	if n := countSystem(a.Snapshot(), "seat1", "轮到你搜证"); n != 1 {
		t.Fatalf("search prompt repeated %d times: the phase is spinning on a dead context", n)
	}

	// A dead context on the walk also starved the waiters, so a real command
	// proves the phase is still listening.
	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "search.clue", ClueID: "c1"}); err != nil {
		t.Fatalf("search after host skip: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return countEvents(a.Snapshot(), "seat1", "你搜到线索") == 1
	})

	// Same trap one phase later: a discussion walk whose ctx expired used to
	// hand out two prompts per seat in a tight alternation.
	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "host.skip"}); err != nil {
		t.Fatalf("skip into discussion: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return countSystem(a.Snapshot(), "seat1", "轮到你发言") == 1
	})
	time.Sleep(300 * time.Millisecond)
	if n := countSystem(a.Snapshot(), "seat1", "轮到你发言"); n != 1 {
		t.Fatalf("speech prompt repeated %d times", n)
	}
	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "speak.text", Text: "我先说自己昨晚在灯塔"}); err != nil {
		t.Fatalf("speech: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return countSystem(a.Snapshot(), "seat2", "轮到你发言") == 1
	})
}

func countSystem(g domain.Game, seatID, needle string) int {
	n := 0
	for _, ev := range g.Events {
		if ev.Type == domain.EvSystem && ev.SeatID == seatID && strings.Contains(ev.Text, needle) {
			n++
		}
	}
	return n
}

// countEvents matches any event type: clue.found is not a system notice.
func countEvents(g domain.Game, seatID, needle string) int {
	n := 0
	for _, ev := range g.Events {
		if ev.SeatID == seatID && strings.Contains(ev.Text, needle) {
			n++
		}
	}
	return n
}

func assertNoEvent(t *testing.T, g domain.Game, needle string) {
	t.Helper()
	for _, ev := range g.Events {
		if strings.Contains(ev.Text, needle) {
			t.Fatalf("unexpected event from an unlimited room: %q", ev.Text)
		}
	}
}
