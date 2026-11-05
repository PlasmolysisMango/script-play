package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"script-play/internal/domain"
	"script-play/internal/llm"
	"script-play/internal/persona"
)

// buildBody is the prompt of one turn, assembled the way the engine does it.
func buildBody(t *testing.T, pb *PromptBuilder, v Visible, instruction string) string {
	t.Helper()
	req := pb.Build(PromptInput{
		Visible:     v,
		Persona:     persona.Get("guarded"),
		Instruction: instruction,
	})
	if len(req.Messages) != 1 {
		t.Fatalf("expected one user message, got %d", len(req.Messages))
	}
	return req.Messages[0].Text
}

// A long argument must not cost a seat its identity. This is the defect the
// budget replaced: the old builder kept the tail of the prompt, so the bigger
// the table talk grew the more of the role book's opening it threw away.
func TestPromptKeepsRoleBookOpeningUnderALongLog(t *testing.T) {
	sc := fixtureScript()
	const opening = "我是沈曼丽，灯塔管理员的妻子。"
	sc.Roles[0].Private.Script = opening + strings.Repeat("那晚灯塔的每一级台阶我都走过，往事一层层压在我身上。", 250)

	g := fixtureGame()
	for i := 0; i < 40; i++ {
		g.AppendEvent(domain.Event{
			Type: domain.EvSpeech, SeatID: "seat2", RoleID: "r2", Speaker: "陆沉",
			Text: "我一直在厨房洗碗，没上过楼，也没听见楼上的动静，这一点我记得很清楚。",
		})
	}

	seat := g.SeatByID("seat1")
	pb := NewPromptBuilder()
	body := buildBody(t, pb, VisibleFor(g, sc, seat, VisibleOptions{MaxTranscript: 40}), "现在轮到你发言。")

	if got, max := runes(body), pb.MaxChars; got > max {
		t.Errorf("prompt is %d runes, budget is %d", got, max)
	}
	if !strings.Contains(body, opening) {
		t.Error("the role book opening was dropped")
	}
	if !strings.Contains(body, "【公开发言记录】") {
		t.Error("the table was dropped")
	}
	if !strings.Contains(body, "现在轮到你发言") {
		t.Error("the instruction was dropped")
	}
}

// When even the frame does not fit, the book gives way in its middle: a seat
// that cannot remember who it is has nothing left to play.
func TestPromptClipsRoleBookFromTheMiddle(t *testing.T) {
	sc := fixtureScript()
	const opening = "我是沈曼丽，灯塔管理员的妻子。"
	const ending = "这是角色本的最后一句。"
	sc.Roles[0].Private.Script = opening +
		strings.Repeat("那晚灯塔的每一级台阶我都走过，往事一层层压在我身上。", 250) + ending

	g := fixtureGame()
	seat := g.SeatByID("seat1")
	pb := &PromptBuilder{MaxTranscript: 40, MaxChars: 6000}
	body := buildBody(t, pb, VisibleFor(g, sc, seat, VisibleOptions{MaxTranscript: 40}), "现在轮到你发言。")

	if got := runes(body); got > pb.MaxChars {
		t.Errorf("prompt is %d runes, budget is %d", got, pb.MaxChars)
	}
	if !strings.Contains(body, opening) {
		t.Error("the book was clipped at its opening instead of its middle")
	}
	if !strings.Contains(body, "（中段省略）") {
		t.Error("expected an explicit elision mark in the book")
	}
	if !strings.Contains(body, ending) {
		t.Error("the tail of the book was dropped")
	}
	if !strings.Contains(body, "现在轮到你发言") {
		t.Error("the instruction was dropped")
	}
}

// Everything the recap already covers disappears from the prompt, but the log
// itself stays complete for the humans scrolling back.
func TestRecapAnchorDropsFoldedLines(t *testing.T) {
	sc := fixtureScript()
	g := fixtureGame()
	for i := 0; i < 10; i++ {
		g.AppendEvent(domain.Event{
			Type: domain.EvSpeech, SeatID: "seat2", RoleID: "r2", Speaker: "陆沉",
			Text: fmt.Sprintf("旧发言第 %d 段，讲的是很早以前的时间线。", i),
		})
	}
	g.AppendEvent(domain.Event{
		Type: domain.EvSpeech, SeatID: "seat3", RoleID: "r3", Speaker: "周叙",
		Text: "新发言：我要指认陆沉。",
	})
	g.Recap = &domain.Recap{
		Text:    "陆沉：一直待在厨房。周叙：开始怀疑陆沉。",
		UptoSeq: g.NextSeq - 2,
		At:      time.Now(),
	}

	seat := g.SeatByID("seat1")
	body := buildBody(t, NewPromptBuilder(), VisibleFor(g, sc, seat, VisibleOptions{MaxTranscript: 40}), "现在轮到你发言。")

	if !strings.Contains(body, "【前情提要（更早的发言已压缩）】") || !strings.Contains(body, g.Recap.Text) {
		t.Error("the recap was not rendered into the prompt")
	}
	if strings.Contains(body, "旧发言第 3 段") {
		t.Error("a line the recap covers was rendered again")
	}
	if !strings.Contains(body, "新发言：我要指认陆沉。") {
		t.Error("a line past the anchor was dropped")
	}

	// The transcript handed to the client is untouched: a human can scroll.
	v := VisibleFor(g, sc, seat, VisibleOptions{MaxTranscript: 200})
	if len(v.Transcript) < 12 {
		t.Fatalf("transcript shrank to %d lines", len(v.Transcript))
	}
}

// Lines that do not fit are folded into one bullet per speaker rather than
// vanishing, so an agent still knows where everybody stands.
func TestPromptDigestsLinesItCannotAfford(t *testing.T) {
	names := []string{"陆沉", "周叙", "沈曼丽"}
	lines := make([]TranscriptLine, 0, 12)
	for i := 1; i <= 11; i++ {
		lines = append(lines, TranscriptLine{
			Seq:     i,
			Kind:    domain.EvSpeech,
			Speaker: names[i%len(names)],
			Text:    fmt.Sprintf("%02d 号发言：%s结尾标记", i, strings.Repeat("我把时间线再说一遍，", 6)),
		})
	}
	lines = append(lines, TranscriptLine{Seq: 12, Kind: domain.EvSpeech, Speaker: "陆沉", Text: "最新一句：我要指认周叙。"})

	v := Visible{Phase: domain.Phase{Name: "第一轮讨论"}, Round: 1, Transcript: lines}
	pb := &PromptBuilder{MaxTranscript: 40, MaxChars: 900}
	body := buildBody(t, pb, v, "现在轮到你发言。")

	if got := runes(body); got > pb.MaxChars {
		t.Errorf("prompt is %d runes, budget is %d", got, pb.MaxChars)
	}
	if !strings.Contains(body, "【更早的发言（要点）】") {
		t.Error("dropped lines left no digest")
	}
	if !strings.Contains(body, "【公开发言记录】") {
		t.Error("no whole recent lines survived")
	}
	if !strings.Contains(body, "最新一句：我要指认周叙。") {
		t.Error("the newest line must always be kept whole")
	}
	if strings.Contains(body, "01 号发言：") {
		t.Error("a folded line was quoted in full")
	}
}

// The compressor runs off the critical path and stores its anchor so the next
// prompt can stop paying for lines it already summarised.
func TestStartRecapStoresAnAnchor(t *testing.T) {
	sc := fixtureScript()
	g := fixtureGame()
	for i := 0; i < 10; i++ {
		g.AppendEvent(domain.Event{
			Type: domain.EvSpeech, SeatID: "seat3", RoleID: "r3", Speaker: "周叙",
			Text: fmt.Sprintf("我说的第 %d 件事：十点前后我一直在大厅。", i),
		})
	}
	// A host-only line must never enter a recap: every seat can read one.
	g.AppendEvent(domain.Event{
		Type: domain.EvDM, Speaker: "主持人", Visibility: domain.VisDM,
		Text: "主持人密语：凶手是周叙。",
	})

	reg := llm.NewRegistry("capture", 2)
	cap := &captureProvider{}
	reg.Register(cap)
	a := NewActor(g, sc, reg, nil, nil, ActorConfig{AgentTimeout: 2 * time.Second})

	a.startRecap(0)
	waitFor(t, 3*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.g.Recap != nil
	})

	a.mu.Lock()
	recap, upto := a.g.Recap.Text, a.g.Recap.UptoSeq
	a.mu.Unlock()
	if strings.TrimSpace(recap) == "" {
		t.Fatal("stored an empty recap")
	}
	if want := g.NextSeq - 1; upto != want {
		t.Errorf("anchor is %d, want %d", upto, want)
	}

	prompts := cap.prompts()
	if len(prompts) == 0 {
		t.Fatal("the summariser was never called")
	}
	for _, p := range prompts {
		if strings.Contains(p, "主持人密语") {
			t.Fatal("a host-only line leaked into a recap source")
		}
		if !strings.Contains(p, "周叙") {
			t.Fatal("the recap source never saw the public speeches")
		}
	}

	seat := g.SeatByID("seat1")
	body := buildBody(t, NewPromptBuilder(), VisibleFor(g, sc, seat, VisibleOptions{MaxTranscript: 40}), "现在轮到你发言。")
	if !strings.Contains(body, recap) {
		t.Error("the stored recap never reached the prompt")
	}
	if strings.Contains(body, "我说的第 3 件事") {
		t.Error("a line the recap covers was rendered again")
	}
}

// The anchor only moves forward: a slow compression that started earlier must
// not roll back what a later one already covered.
func TestRecapAnchorOnlyMovesForward(t *testing.T) {
	a := NewActor(fixtureGame(), fixtureScript(), nil, nil, nil, ActorConfig{})

	// No registry is a normal deployment state (mock-less tests, broken config):
	// compression silently does nothing instead of failing the phase.
	a.startRecap(0)

	a.applyRecap("第一版提要", 5, "p1")
	a.applyRecap("第二版提要", 9, "p2")
	a.applyRecap("迟到的旧提要", 6, "p3")

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.g.Recap.Text != "第二版提要" || a.g.Recap.UptoSeq != 9 {
		t.Fatalf("recap rolled back: %q @%d", a.g.Recap.Text, a.g.Recap.UptoSeq)
	}
}
