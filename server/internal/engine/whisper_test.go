package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
	"script-play/internal/llm"
)

// whisperFixture puts a discussion phase in front of the framework script and
// adds one keyword fragment, so a private line can be checked against both the
// table and the keyword gate.
func whisperFixture() (*Actor, *domain.Script, *domain.Game) {
	sc := frameworkScript()
	sc.Phases = append([]domain.Phase{
		{ID: "p0", Type: domain.PhaseDiscussion, Name: "第一轮私聊", SpeechPerPlayer: 1},
	}, sc.Phases...)
	sc.Clues = append(sc.Clues, domain.Clue{
		ID: "c9", Title: "记忆碎片 A·苏西", Content: "你想起了苏西。",
		OwnerRoleID: "r2", Keywords: []string{"苏西"},
	})
	g := domain.NewGame("g1", "room1", "sc1", []domain.Seat{
		{ID: "seat1", RoomID: "room1", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家一"},
		{ID: "seat2", RoomID: "room1", RoleID: "r2", Kind: domain.SeatHuman, DisplayName: "玩家二"},
		{ID: "seat3", RoomID: "room1", RoleID: "r3", Kind: domain.SeatHuman, DisplayName: "玩家三", IsHost: true},
		{ID: "watcher", RoomID: "room1", Kind: domain.SeatSpectator, DisplayName: "旁观"},
	})
	g.Status = domain.RoomRunning
	g.PhaseIndex = 0 // the private-chat discussion
	return NewActor(g, sc, nil, nil, nil, ActorConfig{}), sc, g
}

func whisperLines(v Visible) []TranscriptLine {
	var out []TranscriptLine
	for _, l := range v.Transcript {
		if l.Kind == domain.EvWhisper {
			out = append(out, l)
		}
	}
	return out
}

// A whisper is for two people: the sender, the listener, and nobody else — not
// the third seat, not the host, not a spectator.
func TestWhisperReachesOnlyTheTwoSeats(t *testing.T) {
	a, sc, g := whisperFixture()
	if err := a.sendWhisper(g.Seats[0], "seat2", "钳子是我拿的，别声张"); err != nil {
		t.Fatalf("sendWhisper: %v", err)
	}
	for _, id := range []string{"seat1", "seat2"} {
		if lines := whisperLines(VisibleFor(g, sc, g.SeatByID(id), VisibleOptions{})); len(lines) != 1 {
			t.Fatalf("%s should see the whisper, got %d lines", id, len(lines))
		}
	}
	for _, seat := range []*domain.Seat{g.SeatByID("seat3"), g.SeatByID("watcher"), nil} {
		if lines := whisperLines(VisibleFor(g, sc, seat, VisibleOptions{})); len(lines) != 0 {
			t.Fatalf("whisper leaked to %v", seat)
		}
	}
}

// The table never heard it, so it must not open a keyword fragment either.
func TestWhisperDoesNotUnlockFragments(t *testing.T) {
	a, sc, g := whisperFixture()
	if st := g.ClueState["c9"]; st.Unlocked {
		t.Fatalf("fragment started unlocked: %+v", st)
	}
	if err := a.sendWhisper(g.Seats[0], "seat2", "苏西那天晚上在画室"); err != nil {
		t.Fatalf("sendWhisper: %v", err)
	}
	if g.ClueState["c9"].Unlocked {
		t.Fatal("a whisper opened a keyword fragment")
	}
	if _, ok := visibleClue(VisibleFor(g, sc, g.SeatByID("seat2"), VisibleOptions{}), "c9"); ok {
		t.Fatal("sealed fragment showed up after a private mention")
	}
}

// Private talk belongs to the private-chat / discussion rounds; in a search or
// vote round it would be a hole in the flow.
func TestWhisperIsRejectedOutsideDiscussion(t *testing.T) {
	a, _, g := whisperFixture()
	g.PhaseIndex = 1 // the search phase of the framework script
	err := a.sendWhisper(g.Seats[0], "seat2", "悄悄说一句")
	if _, ok := err.(ErrNotAllowed); !ok {
		t.Fatalf("whisper in a search phase: got %v, want ErrNotAllowed", err)
	}
}

func TestWhisperRejectsBadTargets(t *testing.T) {
	a, _, g := whisperFixture()
	cases := []struct {
		name string
		to   string
		text string
	}{
		{"self", "seat1", "自言自语"},
		{"spectator", "watcher", "喂"},
		{"unknown", "seat9", "有人吗"},
		{"empty", "seat2", "   "},
	}
	for _, c := range cases {
		if err := a.sendWhisper(g.Seats[0], c.to, c.text); err == nil {
			t.Fatalf("%s: whisper was accepted", c.name)
		}
	}
}

// An AI seat picks the whisper tool instead of speaking up: the table must not
// learn anything, and no public line may be produced.
func TestAgentWhisperStaysPrivate(t *testing.T) {
	a, sc, g := whisperFixture()
	seat := *g.SeatByID("seat2")
	seat.Kind = domain.SeatAgent
	if step := a.applyActionStep(seat, &sc.Phases[0], AgentAction{
		Tool: ToolWhisper, To: "seat1", Text: "是我拿的，你也别说",
	}); step {
		t.Fatal("a whisper should end the seat's turn")
	}
	if lines := whisperLines(VisibleFor(g, sc, g.SeatByID("seat1"), VisibleOptions{})); len(lines) != 1 {
		t.Fatalf("listener saw %d whispers, want 1", len(lines))
	}
	if lines := whisperLines(VisibleFor(g, sc, g.SeatByID("seat3"), VisibleOptions{})); len(lines) != 0 {
		t.Fatal("whisper leaked to the table")
	}
	for _, ev := range g.Events {
		if ev.Type == domain.EvSpeech && strings.Contains(ev.Text, "是我拿的") {
			t.Fatal("the whisper was also spoken in public")
		}
	}
}

// whisperEcho is a provider that answers every private turn with a whisper back
// to seat1, so the auto-reply path can be checked without a real model.
type whisperEcho struct{}

func (whisperEcho) Name() string { return "whispercap" }

func (whisperEcho) Stream(ctx context.Context, req llm.Request, sink func(string) error) error {
	return sendChunk(sink, `{"tool":"whisper","to":"seat1","text":"别告我，我们各退一步"}`)
}

func sendChunk(sink func(string) error, text string) error {
	if sink == nil {
		return nil
	}
	return sink(text)
}

// A human whispering to an AI must get an answer back in the same private lane:
// a public reply would hand the secret to the whole table.
func TestWhisperToAgentIsAnsweredPrivately(t *testing.T) {
	_, sc, g := whisperFixture()
	agent := g.SeatByID("seat2")
	agent.Kind = domain.SeatAgent
	agent.ModelID = "whispercap/test"

	reg := llm.NewRegistry("whispercap", 1)
	reg.Register(whisperEcho{})
	a := NewActor(g, sc, reg, nil, nil, ActorConfig{AgentTimeout: 5 * time.Second})

	if err := a.sendWhisper(g.Seats[0], "seat2", "钳子在我这，要不要合作"); err != nil {
		t.Fatalf("sendWhisper: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return len(whisperLines(VisibleFor(g, sc, g.SeatByID("seat1"), VisibleOptions{}))) == 2
	})
	if len(whisperLines(VisibleFor(g, sc, g.SeatByID("seat3"), VisibleOptions{}))) != 0 {
		t.Fatal("the AI's private answer leaked to the table")
	}
	for _, ev := range g.Events {
		if ev.Type != domain.EvWhisper && strings.Contains(ev.Text, "各退一步") {
			t.Fatalf("private answer also landed in a public event: %s", ev.Type)
		}
	}
}

func TestPairVisibilityIsOrderIndependent(t *testing.T) {
	if PairVisibility("seat2", "seat1") != PairVisibility("seat1", "seat2") {
		t.Fatal("pair visibility depends on argument order")
	}
	ev := domain.Event{Visibility: PairVisibility("seat1", "seat2")}
	if !EventVisibleTo(ev, &domain.Seat{ID: "seat1"}) || !EventVisibleTo(ev, &domain.Seat{ID: "seat2"}) {
		t.Fatal("either side of the pair must receive the event")
	}
	// "seat1" must not match "seat12".
	if EventVisibleTo(ev, &domain.Seat{ID: "seat12"}) {
		t.Fatal("pair matching used a substring instead of the whole id")
	}
}
