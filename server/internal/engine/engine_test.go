package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"script-play/internal/domain"
	"script-play/internal/llm"
	"script-play/internal/persona"
)

func TestMachineWalksEveryPhase(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	m := NewMachine(sc)
	if m.Len() != len(sc.Phases) {
		t.Fatalf("len = %d, want %d", m.Len(), len(sc.Phases))
	}
	if cur := m.Current(g); cur == nil || cur.ID != "p1" {
		t.Fatalf("first phase = %+v, want p1", cur)
	}

	var seen []string
	for {
		ph, done := m.Advance(g)
		if done {
			break
		}
		if ph == nil {
			t.Fatal("advance returned a nil phase without finishing")
		}
		seen = append(seen, ph.ID)
	}
	want := []string{"p2", "p3", "p4", "p5"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("advance order = %v, want %v", seen, want)
	}
	if g.Round != 1 {
		t.Fatalf("round = %d, want 1", g.Round)
	}
	if ph, done := m.Advance(g); !done {
		t.Fatalf("second advance past the end should finish, got %+v", ph)
	}

	// The summary must flag exactly one current phase.
	current := 0
	for _, s := range m.Summary(g) {
		if s.Status == "current" {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("current phase count = %d, want 1", current)
	}
}

func TestSchedulerOrderAndRoundRobin(t *testing.T) {
	base := time.Date(2024, 5, 1, 20, 0, 0, 0, time.UTC)
	seats := []domain.Seat{
		{ID: "s2", Kind: domain.SeatAgent, JoinedAt: base.Add(2 * time.Second)},
		{ID: "spec", Kind: domain.SeatSpectator, JoinedAt: base},
		{ID: "s1", Kind: domain.SeatHuman, IsHost: true, JoinedAt: base.Add(time.Second)},
		{ID: "s3", Kind: domain.SeatAgent, JoinedAt: base.Add(3 * time.Second)},
	}
	sch := NewScheduler(seats)
	order := sch.Order()
	var ids []string
	for _, s := range order {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "s1,s2,s3" {
		t.Fatalf("order = %v, want host first then join order", ids)
	}

	counts := map[string]int{}
	for i := 0; i < 3; i++ {
		next := sch.NextAfter("", counts, 1)
		if next == nil {
			t.Fatalf("turn %d: no seat available", i)
		}
		counts[next.ID]++
	}
	if counts["s1"] != 1 || counts["s2"] != 1 || counts["s3"] != 1 {
		t.Fatalf("quota not shared: %v", counts)
	}
	if next := sch.NextAfter("", counts, 1); next != nil {
		t.Fatalf("expected nil when every seat spoke, got %s", next.ID)
	}
}

func TestSchedulerSkipsSpectators(t *testing.T) {
	sch := NewScheduler([]domain.Seat{
		{ID: "spec", Kind: domain.SeatSpectator},
		{ID: "a", Kind: domain.SeatAgent},
	})
	if got := len(sch.Order()); got != 1 {
		t.Fatalf("player count = %d, want 1", got)
	}
}

func TestQuota(t *testing.T) {
	cases := []struct{ done, limit, want int }{
		{0, 2, 2}, {1, 2, 1}, {2, 2, 0}, {3, 2, 0}, {0, 0, 0}, {0, -1, 0},
	}
	for _, c := range cases {
		if got := Quota(c.done, c.limit); got != c.want {
			t.Errorf("Quota(%d,%d) = %d, want %d", c.done, c.limit, got, c.want)
		}
	}
}

func TestLeakGuardCatchesTruthAndRepairsIt(t *testing.T) {
	sc := fixtureScript()
	seat := &domain.Seat{ID: "seat1", RoleID: "r1"}
	guard := NewLeakGuard(sc, seat)

	if hit, leaked := guard.Check("我当时看见" + fixtureTruthMethod + "，真的。"); !leaked {
		t.Fatalf("truth quotation not detected (hit=%q)", hit)
	} else {
		clean := guard.Sanitize("我当时看见"+fixtureTruthMethod+"，真的。", hit)
		if strings.Contains(clean, fixtureTruthMethod) {
			t.Fatalf("sanitize kept the leak: %q", clean)
		}
	}

	// The seat's own secret is allowed.
	if _, leaked := guard.Check("其实" + "沈曼丽当晚并不在房间。mine-r1-secret"); leaked {
		t.Fatal("own secret must not be flagged as a leak")
	}
	// Foreign secret is not allowed.
	if _, leaked := guard.Check(fixtureR2Secret); !leaked {
		t.Fatal("foreign secret must be flagged as a leak")
	}
	// Echoing the prompt is a leak.
	if _, leaked := guard.Check("【你的秘密（是否透露由你决定）】"); !leaked {
		t.Fatal("prompt echo must be flagged as a leak")
	}
	if _, leaked := guard.Check("我一直在房间里，没听到什么动静。"); leaked {
		t.Fatal("ordinary roleplay line must not be flagged")
	}
}

// captureProvider records every prompt and answers deterministically, so the
// full game loop can run without a real LLM.
type captureProvider struct {
	mu   sync.Mutex
	reqs []llm.Request
}

func (c *captureProvider) Name() string { return "capture" }

func (c *captureProvider) Stream(ctx context.Context, req llm.Request, sink func(string) error) error {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	text := "我先说说我的看法，当时我在楼下，听见楼上有动静。"
	if req.JSON {
		text = `{"vote":"周叙","reason":"他的时间线对不上。"}`
	}
	if sink != nil {
		if err := sink(text); err != nil {
			return err
		}
	}
	return nil
}

func (c *captureProvider) prompts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.reqs))
	for _, r := range c.reqs {
		body := r.System
		for _, m := range r.Messages {
			body += "\n" + m.Text
		}
		out = append(out, body)
	}
	return out
}

// TestActorRunsFullGameWithAgents is the mock end-to-end: no network, every
// phase executes, and every prompt that reached the provider is leak free.
func TestActorRunsFullGameWithAgents(t *testing.T) {
	sc := fixtureScript()
	g := domain.NewGame("g1", "room1", "sc1", nil)
	g.Status = domain.RoomLobby
	base := time.Now().Add(-time.Hour)
	for i, roleID := range []string{"r1", "r2", "r3"} {
		g.Seats = append(g.Seats, domain.Seat{
			ID:          "seat" + string(rune('1'+i)),
			RoomID:      "room1",
			RoleID:      roleID,
			Kind:        domain.SeatAgent,
			DisplayName: "AI" + roleID,
			PersonaID:   []string{"guarded", "sly", "candid"}[i],
			ModelID:     "capture/test",
			Connected:   true,
			JoinedAt:    base.Add(time.Duration(i) * time.Second),
		})
	}

	reg := llm.NewRegistry("capture", 2)
	cap := &captureProvider{}
	reg.Register(cap)

	actor := NewActor(g, sc, reg, nil, nil, ActorConfig{
		FloorTimeout: 2 * time.Second,
		AgentTimeout: 5 * time.Second,
		AutoAdvance:  true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go actor.Start(ctx)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if actor.Status() == domain.RoomFinished {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if actor.Status() != domain.RoomFinished {
		t.Fatalf("game did not finish, status = %s", actor.Status())
	}

	snap := actor.Snapshot()
	phaseEvents := map[string]bool{}
	var reveal, voteResult bool
	for _, ev := range snap.Events {
		switch ev.Type {
		case domain.EvPhase:
			phaseEvents[ev.PhaseID] = true
		case domain.EvReveal:
			reveal = true
		case domain.EvVoteResult:
			voteResult = true
		}
	}
	for _, p := range sc.Phases {
		if !phaseEvents[p.ID] {
			t.Errorf("phase %s never started", p.ID)
		}
	}
	if !voteResult {
		t.Error("vote phase produced no tally")
	}
	if !reveal || !strings.Contains(eventText(&snap, domain.EvReveal), "周叙") {
		t.Error("reveal phase must name the killer")
	}

	prompts := cap.prompts()
	if len(prompts) < len(sc.Phases) {
		t.Fatalf("only %d prompts captured for %d phases", len(prompts), len(sc.Phases))
	}
	for i, p := range prompts {
		for _, secret := range []string{fixtureTruthMethod, fixtureTruthSummary} {
			if strings.Contains(p, secret) {
				t.Errorf("prompt #%d leaked truth %q", i, secret)
			}
		}
	}
	// Seat prompts may only carry their own private script.
	for i, req := range cap.reqs {
		own := ""
		for _, s := range g.Seats {
			if s.ID == req.SeatID {
				own = s.RoleID
			}
		}
		for _, r := range sc.Roles {
			if r.ID == own {
				continue
			}
			if strings.Contains(prompts[i], r.Private.Script) {
				t.Errorf("prompt #%d (role %s) contains the script of %s", i, own, r.ID)
			}
		}
		if own == "" {
			t.Errorf("prompt #%d has no seat", i)
		}
	}
}

// TestPersonaRevealPressure documents the numbers behind "different
// personalities reveal different amounts".
func TestPersonaRevealPressure(t *testing.T) {
	candid := persona.Get("candid")
	guarded := persona.Get("guarded")
	pressure := persona.RevealPressure{Asked: true, RoundNormalised: 0.5}
	if !candid.ShouldReveal(0.6, pressure) {
		t.Error("candid persona should reveal a mid-cost secret under pressure")
	}
	if guarded.ShouldReveal(0.9, pressure) {
		t.Error("guarded persona should keep a high-cost secret")
	}
	if guarded.MaxSentences() > candid.MaxSentences() {
		t.Error("candid persona should talk at least as much as guarded")
	}
}

func eventText(g *domain.Game, kind string) string {
	var out []string
	for _, ev := range g.Events {
		if ev.Type == kind {
			out = append(out, ev.Text)
		}
	}
	return strings.Join(out, "\n")
}
