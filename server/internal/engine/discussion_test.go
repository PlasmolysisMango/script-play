package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"script-play/internal/domain"
)

// talkTable builds a three-human table parked on the discussion phase, with the
// floor already open. Everything before it (opening, search) is skipped so a
// test can get straight to the queue.
func talkTable(t *testing.T) *Actor {
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
		FloorTimeout: 15 * time.Second,
		AgentTimeout: 2 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	go a.Start(ctx)
	return a
}

func mustHandle(t *testing.T, a *Actor, seatID string, msg ClientMessage) {
	t.Helper()
	if err := a.Handle(context.Background(), seatID, msg); err != nil {
		t.Fatalf("%s %s: %v", seatID, msg.Type, err)
	}
}

// openTalk walks the scripted opening round and returns once the floor is open
// to requests, which is where every free-discussion rule applies.
func openTalk(t *testing.T, a *Actor) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})
	mustHandle(t, a, "seat1", ClientMessage{Type: "host.skip"})

	waitFor(t, 5*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat1", "轮到你搜证")
	})
	mustHandle(t, a, "seat1", ClientMessage{Type: "host.skip"})

	for spoke := 0; spoke < 3; spoke++ {
		holder := ""
		waitFor(t, 8*time.Second, func() bool {
			for _, id := range []string{"seat1", "seat2", "seat3"} {
				st, ok := a.StateForSeat(id)
				if ok && st.Floor != nil && st.Floor.SeatID == id {
					holder = id
					return true
				}
			}
			return false
		})
		mustHandle(t, a, holder, ClientMessage{Type: "speak.text", Text: "开场陈述"})
		released := holder
		waitFor(t, 5*time.Second, func() bool {
			st, _ := a.StateForSeat(released)
			return st.Floor == nil || st.Floor.SeatID != released
		})
	}

	waitFor(t, 8*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.TalkOpen
	})
}

func floorOf(a *Actor, seatID string) *Floor {
	st, ok := a.StateForSeat(seatID)
	if !ok {
		return nil
	}
	return st.Floor
}

// The answer to a line goes to the front of the queue: the seat that was
// addressed holds the floor next, before the seats that were already waiting.
func TestReplyMovesTheAnsweredSeatToTheFront(t *testing.T) {
	a := talkTable(t)
	openTalk(t, a)

	// seat3 asks first, seat1 then presses seat2. Without the reply rule seat2
	// would wait behind seat3.
	mustHandle(t, a, "seat3", ClientMessage{Type: "speak.request"})
	waitFor(t, 5*time.Second, func() bool {
		q := floorOrQueue(a)
		return q == "seat3"
	})
	mustHandle(t, a, "seat3", ClientMessage{Type: "speak.text", Text: "我看是陆沉干的。"})

	waitFor(t, 3*time.Second, func() bool {
		f := floorOf(a, "seat3")
		return f == nil || f.SeatID != "seat3"
	})

	mustHandle(t, a, "seat1", ClientMessage{Type: "speak.text", Text: "陆沉，你昨晚在哪？", ReplyTo: "seat2"})
	waitFor(t, 5*time.Second, func() bool {
		f := floorOf(a, "seat2")
		return f != nil && f.SeatID == "seat2" && f.Kind == SlotReply && f.ReplyTo == "seat1"
	})
}

// Declining is a normal move: the floor moves on and others may speak.
func TestDeclineReleasesTheFloorAndOthersContinue(t *testing.T) {
	a := talkTable(t)
	openTalk(t, a)

	mustHandle(t, a, "seat1", ClientMessage{Type: "speak.text", Text: "陆沉，你解释一下。", ReplyTo: "seat2"})
	waitFor(t, 5*time.Second, func() bool {
		f := floorOf(a, "seat2")
		return f != nil && f.SeatID == "seat2"
	})
	mustHandle(t, a, "seat2", ClientMessage{Type: "speak.pass"})

	waitFor(t, 5*time.Second, func() bool {
		return hasSystemEvent(a.Snapshot(), "seat2", "表示不作回应")
	})
	waitFor(t, 3*time.Second, func() bool {
		f := floorOf(a, "seat2")
		return f == nil || f.SeatID != "seat2"
	})

	mustHandle(t, a, "seat3", ClientMessage{Type: "speak.request"})
	waitFor(t, 5*time.Second, func() bool {
		f := floorOf(a, "seat3")
		return f != nil && f.SeatID == "seat3"
	})
	mustHandle(t, a, "seat3", ClientMessage{Type: "speak.text", Text: "我补充一句。"})
	waitFor(t, 3*time.Second, func() bool {
		return countEvents(a.Snapshot(), "seat3", "我补充一句") == 1
	})
}

// Two seats may ask at the same time; only the head speaks, and the second one
// keeps its words until it reaches the head.
func TestQueueKeepsOneSpeakerAtATime(t *testing.T) {
	a := talkTable(t)
	openTalk(t, a)

	mustHandle(t, a, "seat1", ClientMessage{Type: "speak.request"})
	mustHandle(t, a, "seat2", ClientMessage{Type: "speak.request"})

	waitFor(t, 5*time.Second, func() bool {
		f := floorOf(a, "seat1")
		return f != nil && f.SeatID == "seat1"
	})
	// seat2 is queued, not speaking: its typed line must not appear yet.
	mustHandle(t, a, "seat2", ClientMessage{Type: "speak.text", Text: "等一下我还有话说"})
	if n := countEvents(a.Snapshot(), "seat2", "等一下我还有话说"); n != 0 {
		t.Fatalf("a queued seat spoke over the floor holder")
	}

	mustHandle(t, a, "seat1", ClientMessage{Type: "speak.text", Text: "我先讲"})
	waitFor(t, 5*time.Second, func() bool {
		return countEvents(a.Snapshot(), "seat2", "等一下我还有话说") == 1
	})

	// The held line is played exactly once, in order after seat1's.
	var order []string
	for _, ev := range a.Snapshot().Events {
		if ev.Type != domain.EvSpeech {
			continue
		}
		if ev.Text == "我先讲" || ev.Text == "等一下我还有话说" {
			order = append(order, ev.Text)
		}
	}
	if len(order) != 2 || order[0] != "我先讲" || order[1] != "等一下我还有话说" {
		t.Fatalf("queued speech is out of order: %v", order)
	}
}

// floorOrQueue reports whoever holds or heads the microphone, so a test can
// wait for "somebody got the floor" without guessing which seat was next.
func floorOrQueue(a *Actor) string {
	st, ok := a.StateForSeat("seat1")
	if !ok {
		return ""
	}
	if st.Floor != nil {
		return st.Floor.SeatID
	}
	if len(st.Queue) > 0 {
		return st.Queue[0].SeatID
	}
	return ""
}

// Search options are printed on the back of the card: a place, never the title
// of the evidence behind it.
func TestSearchOptionsCarryOnlyTheBackHint(t *testing.T) {
	sc := fixtureScript()
	sc.Clues[2].Back = "楼梯拐角" // c3, the one without an owner
	sc.Clues[3].Back = ""     // c4 has no back text: the ordinal fallback applies
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Status = domain.RoomRunning
	g.PhaseIndex = 1 // the search phase
	g.Seats = []domain.Seat{{
		ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman,
		DisplayName: "玩家r1", IsHost: true, Connected: true, JoinedAt: time.Now(),
	}}

	a := NewActor(g, sc, nil, nil, nil, ActorConfig{})
	st, ok := a.StateForSeat("seat1")
	if !ok {
		t.Fatal("no state for seat1")
	}
	if len(st.Search) == 0 {
		t.Fatal("search phase exposes no options")
	}

	titles := map[string]bool{}
	for _, c := range sc.Clues {
		titles[c.Title] = true
	}
	seenBack := false
	for _, o := range st.Search {
		if o.Hint == "" {
			t.Fatalf("option %s has no hint at all", o.ID)
		}
		if titles[o.Hint] {
			t.Fatalf("option %s leaks the clue title: %q", o.ID, o.Hint)
		}
		for title := range titles {
			if strings.Contains(o.Hint, title) {
				t.Fatalf("option %s hint contains the title %q: %q", o.ID, title, o.Hint)
			}
		}
		if o.Hint == "楼梯拐角" {
			seenBack = true
		}
	}
	if !seenBack {
		t.Fatalf("the card back text is not used as the hint: %+v", st.Search)
	}
}

// A model that answers with prose, a code fence or a bare envelope must all
// still be understood; an unknown tool degrades to speech.
func TestParseActionAcceptsEnvelopeAndProse(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want AgentAction
	}{
		{"bare envelope", `{"tool":"pass","reason":"没话说"}`, AgentAction{Tool: ToolPass, Reason: "没话说"}},
		{"fenced", "```json\n{\"tool\":\"reveal\",\"clueId\":\"c3\",\"text\":\"看这个\"}\n```", AgentAction{Tool: ToolReveal, ClueID: "c3", Text: "看这个"}},
		{"prose", "我觉得是你。", AgentAction{Tool: ToolSpeak, Text: "我觉得是你。"}},
		{"surrounded", `好的：{"tool":"speak","text":"嗯"}`, AgentAction{Tool: ToolSpeak, Text: "嗯"}},
		// A CLI provider can echo the prompt back before answering. The answer is
		// the last envelope; speaking the echo is what put `{"tool":"speak"}` on
		// the table.
		{
			"echoed prompt",
			"{\"tool\":\"speak\",\"text\":\"旧台词\"}\n\nuser{\"tool\":\"speak\",\"text\":\"旧台词\",\"replyTo\":\"b\"}\n\n" +
				"<system-reminder>\n# Environment\n- working directory: /tmp/x\n</system-reminder>\n\n" +
				"{\"tool\":\"pass\",\"reason\":\"已说过，等回应\"}",
			AgentAction{Tool: ToolPass, Reason: "已说过，等回应"},
		},
		{"machine noise", "<system-reminder>\n# Environment\n</system-reminder>\n{\"tool\":", AgentAction{Tool: ToolPass, Reason: "模型没有给出工具调用"}},
	}
	for _, c := range cases {
		got := parseAction(c.raw)
		if got.Tool != c.want.Tool || got.Text != c.want.Text || got.ClueID != c.want.ClueID {
			t.Fatalf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

// cleanSpeech keeps dialogue and drops the harness chatter around it.
func TestCleanSpeechDropsHarnessChatter(t *testing.T) {
	cases := map[string]string{
		"开场白。<system-reminder>\n# Environment\n</system-reminder>收尾。": "开场白。 收尾。",
		`{"tool":"speak","text":"我不该被念出来"}`:                           "",
		"  ": "",
	}
	for in, want := range cases {
		if got := cleanSpeech(in); got != want {
			t.Fatalf("cleanSpeech(%q) = %q, want %q", in, got, want)
		}
	}
}

// recordingHub collects what the engine pushes at clients.
type recordingHub struct {
	mu   sync.Mutex
	seen []domain.Event
}

func (h *recordingHub) Publish(ev domain.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, ev)
}

func (h *recordingHub) find(typ string) (domain.Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range h.seen {
		if ev.Type == typ {
			return ev, true
		}
	}
	return domain.Event{}, false
}

// The "somebody is composing" marker is a live signal: it goes to the sockets
// and never into the event log, or a reload would replay stale animations.
func TestThinkingMarkerIsBroadcastButNeverStored(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Seats = []domain.Seat{{
		ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatAgent,
		DisplayName: "AI 沈曼丽", PersonaID: "guarded", Connected: true, JoinedAt: time.Now(),
	}}
	hub := &recordingHub{}
	a := NewActor(g, sc, nil, nil, hub, ActorConfig{})

	a.thinking(g.Seats[0], "start")
	ev, ok := hub.find(domain.EvThinking)
	if !ok {
		t.Fatal("no thinking marker was published")
	}
	if ev.SeatID != "seat1" || ev.Meta["state"] != "start" {
		t.Fatalf("marker carries the wrong payload: %+v", ev)
	}
	for _, stored := range g.Events {
		if stored.Type == domain.EvThinking {
			t.Fatal("a transient marker was written to the event log")
		}
	}
}
