package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
)

// The acceptance run kept meeting three ways a room could outlive its table:
// a scene that advanced instantly, a discussion nobody spoke in, and a script
// whose last phase is not a reveal. Each one has an engine-side answer now.

func humanTable(t *testing.T, sc *domain.Script, cfg ActorConfig) *Actor {
	t.Helper()
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
	a := NewActor(g, sc, nil, nil, nil, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)
	return a
}

// Scripts without a reveal phase (play/score, e.g. 生日快乐) never reach an
// ending on their own, so the host needs one explicit exit that also frees the
// concurrency slot.
func TestHostEndFinishesTheTable(t *testing.T) {
	sc := fixtureScript()
	sc.Phases = []domain.Phase{
		{ID: "p1", Type: domain.PhaseOpening, Name: "开场"},
		{ID: "p2", Type: domain.PhaseDiscussion, Name: "讨论", SpeechPerPlayer: 1},
	}
	a := humanTable(t, sc, ActorConfig{FloorTimeout: 10 * time.Second, AgentTimeout: 2 * time.Second})

	waitFor(t, 5*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.Visible.Phase.ID == "p1"
	})
	mustHandle(t, a, "seat1", ClientMessage{Type: "host.end"})

	waitFor(t, 5*time.Second, func() bool { return a.Snapshot().Status == domain.RoomFinished })
	select {
	case <-a.Done():
	default:
		t.Fatal("the host's ending did not close the actor's done channel")
	}
	found := false
	for _, ev := range a.Snapshot().Events {
		if ev.Type == domain.EvSystem && strings.Contains(ev.Text, "主持人结束了本局") {
			found = true
		}
	}
	if !found {
		t.Fatal("the ending is missing from the transcript")
	}
}

// A reading scene has a floor: clicking "next" twice must not skip the act
// before anybody could read the role book. A script's timeoutSec is a
// suggestion, not a lock — a printed 900-second scene used to ignore the host
// for fifteen minutes.
func TestReadingPhaseHoldsAnEarlySkip(t *testing.T) {
	sc := fixtureScript()
	sc.Phases = []domain.Phase{{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本", TimeoutSec: 900}}
	a := humanTable(t, sc, ActorConfig{
		FloorTimeout: 10 * time.Second,
		AgentTimeout: 2 * time.Second,
		ReadingMin:   1500 * time.Millisecond,
	})

	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.Snapshot().Events {
			if ev.Type == domain.EvPhase && ev.PhaseID == "p1" {
				return true
			}
		}
		return false
	})
	err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "host.skip"})
	if err == nil || !strings.Contains(err.Error(), "阅读阶段还剩") {
		t.Fatalf("skipping a fresh reading scene = %v, want a floor refusal", err)
	}

	// Once the floor has passed the same click goes through: the scene is held,
	// not locked.
	time.Sleep(1600 * time.Millisecond)
	mustHandle(t, a, "seat1", ClientMessage{Type: "host.skip"})
	waitFor(t, 5*time.Second, func() bool { return a.Snapshot().Status == domain.RoomFinished })
}

// A host-only creator sits in a spectator seat: the table still answers to the
// host's commands, but the host must not be able to speak as a player.
func TestSpectatorHostCommandsWithoutSpeaking(t *testing.T) {
	sc := fixtureScript()
	sc.Phases = []domain.Phase{
		{ID: "p1", Type: domain.PhaseDiscussion, Name: "公聊", SpeechPerPlayer: 1, Order: domain.OrderRoundRobin},
	}
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
			JoinedAt:    time.Now(),
		})
	}
	g.Seats = append(g.Seats, domain.Seat{
		ID:          "seat4",
		RoomID:      "room1",
		Kind:        domain.SeatSpectator,
		DisplayName: "主持人",
		IsHost:      true,
		Connected:   true,
		JoinedAt:    time.Now(),
	})
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{
		FloorTimeout: 100 * time.Millisecond,
		AgentTimeout: 2 * time.Second,
		DiscussIdle:  10 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)

	// Wait until the scripted round is over and the floor is open.
	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.Snapshot().Events {
			if ev.Type == domain.EvSystem && strings.Contains(ev.Text, "进入自由讨论") {
				return true
			}
		}
		return false
	})

	err := a.Handle(context.Background(), "seat4", ClientMessage{Type: "speak.text", Text: "我是主持人"})
	if err == nil || !strings.Contains(err.Error(), "观战者不能发言") {
		t.Fatalf("spectator host speaking = %v, want a refusal", err)
	}
	for _, ev := range a.Snapshot().Events {
		if ev.Type == domain.EvSpeech && strings.Contains(ev.Text, "我是主持人") {
			t.Fatal("the spectator's line reached the transcript")
		}
	}

	// The host's own controls stay available from that same seat.
	mustHandle(t, a, "seat4", ClientMessage{Type: "host.end"})
	waitFor(t, 5*time.Second, func() bool { return a.Snapshot().Status == domain.RoomFinished })
}

// A discussion nobody speaks in used to hold the room for as long as the host
// kept the tab open (252s in the acceptance run). Silence now ends the scene.
func TestSilentDiscussionAdvancesByItself(t *testing.T) {
	sc := fixtureScript()
	sc.Phases = []domain.Phase{
		{ID: "p1", Type: domain.PhaseDiscussion, Name: "公聊", SpeechPerPlayer: 1, Order: domain.OrderRoundRobin},
	}
	a := humanTable(t, sc, ActorConfig{
		FloorTimeout: 250 * time.Millisecond,
		AgentTimeout: 2 * time.Second,
		DiscussIdle:  300 * time.Millisecond,
	})

	waitFor(t, 10*time.Second, func() bool { return a.Snapshot().Status == domain.RoomFinished })
	found := false
	for _, ev := range a.Snapshot().Events {
		if ev.Type == domain.EvSystem && strings.Contains(ev.Text, "无人发言，本阶段自动结束") {
			found = true
		}
	}
	if !found {
		t.Fatal("the idle discussion ended without saying why")
	}
}
