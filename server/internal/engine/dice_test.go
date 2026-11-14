package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"script-play/internal/domain"
	"script-play/internal/llm"
)

func diceTestScript() *domain.Script {
	role := func(id, name string) domain.Role {
		return domain.Role{ID: id, Name: name, Private: domain.RolePrivate{Script: "我是" + name + "。"}}
	}
	return &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "骰子测试", PlayerCount: domain.CountRange{Min: 3, Max: 3}},
		Roles:         []domain.Role{role("r1", "甲"), role("r2", "乙"), role("r3", "丙")},
		Phases: []domain.Phase{
			{
				ID:   "p1",
				Type: domain.PhaseDice,
				Name: "骰子提问",
				Dice: &domain.DiceRules{
					Rounds:   1,
					Sides:    6,
					TieBreak: map[string]int{"r1": 3, "r2": 2, "r3": 1},
					Questions: map[string][]string{
						"r1": {"你听过太阳吗？"},
						"r2": {"你见过项链吗？"},
						"r3": {"你相信命运吗？"},
					},
					Keywords: map[string]map[string]string{
						"r1": {"太阳": "猜想者号。"},
						"r2": {"项链": "和人打架的时候顺来的。"},
						"r3": {"命运": "反正我从来没成功过。"},
					},
				},
			},
			{ID: "p2", Type: domain.PhaseOpening, Name: "结束"},
		},
	}
}

func TestDiceRoundRollsAskAndAnswers(t *testing.T) {
	sc := diceTestScript()
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
			JoinedAt:    time.Now(),
		})
	}
	a := NewActor(g, sc, nil, nil, nil, ActorConfig{FloorTimeout: time.Second, AgentTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.Dice != nil && st.Dice.Stage == diceStageRolling
	})
	for _, id := range []string{"seat1", "seat2", "seat3"} {
		if err := a.Handle(context.Background(), id, ClientMessage{Type: "dice.roll"}); err != nil {
			t.Fatalf("%s roll: %v", id, err)
		}
	}
	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.Dice != nil && st.Dice.Stage == diceStageAsking
	})

	var asker, target string
	for _, id := range []string{"seat1", "seat2", "seat3"} {
		st, _ := a.StateForSeat(id)
		if st.Dice == nil {
			t.Fatalf("%s has no dice view", id)
		}
		if st.Dice.AskerSeatID == "" || st.Dice.TargetSeatID == "" {
			t.Fatalf("round did not resolve: %+v", st.Dice)
		}
		if st.Dice.NeedAsk {
			asker = id
			if len(st.Dice.Choices) == 0 {
				t.Fatalf("asker has no question cards")
			}
			if st.Dice.Choices[0] == "" {
				t.Fatalf("empty question card")
			}
		} else if len(st.Dice.Choices) != 0 {
			t.Fatalf("%s received another role's question cards", id)
		}
		if st.Dice.TargetSeatID == id {
			target = id
		}
	}
	if asker == "" || target == "" || asker == target {
		t.Fatalf("bad ask/target: asker=%q target=%q", asker, target)
	}

	// Ask with a question containing the target's keyword so the required
	// fragment has to reach the answer prompt only.
	question := "你见过项链吗？"
	if target == "seat1" {
		question = "你听过太阳吗？"
	} else if target == "seat3" {
		question = "你相信命运吗？"
	}
	if err := a.Handle(context.Background(), asker, ClientMessage{Type: "dice.ask", Text: question}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat(target)
		return ok && st.Dice != nil && st.Dice.Stage == diceStageAnswering && st.Dice.NeedAnswer
	})
	targetState, _ := a.StateForSeat(target)
	if len(targetState.Dice.Required) == 0 {
		t.Fatalf("target has no required answer fragments: %+v", targetState.Dice)
	}
	if err := a.Handle(context.Background(), target, ClientMessage{Type: "speak.text", Text: "我回答：" + targetState.Dice.Required[0]}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.PhaseDone
	})
	found := false
	for _, ev := range a.Snapshot().Events {
		if ev.Type == domain.EvSpeech && strings.Contains(ev.Text, "我回答：") {
			found = true
		}
	}
	if !found {
		t.Fatalf("answer never reached the transcript")
	}
}

type diceAnswerProvider struct {
	mu   sync.Mutex
	reqs []llm.Request
}

func (d *diceAnswerProvider) Name() string { return "dice" }

func (d *diceAnswerProvider) Stream(ctx context.Context, req llm.Request, sink func(string) error) error {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.mu.Unlock()
	text := `{"tool":"speak","text":"我见过一条项链，是和人打架的时候顺来的。"}`
	if sink != nil {
		return sink(text)
	}
	return nil
}

func (d *diceAnswerProvider) prompts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.reqs))
	for _, r := range d.reqs {
		body := r.System
		for _, m := range r.Messages {
			body += "\n" + m.Text
		}
		out = append(out, body)
	}
	return out
}

func TestDiceAIAnswersHumanQuestion(t *testing.T) {
	sc := diceTestScript()
	sc.Phases[0].Dice.Sides = 1
	sc.Phases[0].Dice.TieBreak = map[string]int{"r1": 10, "r2": 1, "r3": 5}
	sc.Phases[0].Dice.Questions["r1"] = []string{"你见过项链吗？", "你听过太阳吗？"}
	g := domain.NewGame("g2", "room2", "sc1", nil)
	g.Seats = append(g.Seats,
		domain.Seat{ID: "seat1", RoomID: "room2", RoleID: "r1", Kind: domain.SeatHuman, DisplayName: "玩家r1", IsHost: true, Connected: true, JoinedAt: time.Now()},
		domain.Seat{ID: "seat2", RoomID: "room2", RoleID: "r2", Kind: domain.SeatAgent, DisplayName: "AI r2", PersonaID: "guarded", ModelID: "dice/test", Connected: true, JoinedAt: time.Now()},
		domain.Seat{ID: "seat3", RoomID: "room2", RoleID: "r3", Kind: domain.SeatAgent, DisplayName: "AI r3", PersonaID: "guarded", ModelID: "dice/test", Connected: true, JoinedAt: time.Now()},
	)
	reg := llm.NewRegistry("dice", 2)
	cap := &diceAnswerProvider{}
	reg.Register(cap)

	a := NewActor(g, sc, reg, nil, nil, ActorConfig{FloorTimeout: time.Second, AgentTimeout: 5 * time.Second, AutoAdvance: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go a.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.Dice != nil && st.Dice.Stage == diceStageRolling
	})
	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "dice.roll"}); err != nil {
		t.Fatalf("roll: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		st, ok := a.StateForSeat("seat1")
		return ok && st.Dice != nil && st.Dice.Stage == diceStageAsking
	})
	st, _ := a.StateForSeat("seat1")
	if st.Dice.AskerSeatID != "seat1" || st.Dice.TargetSeatID != "seat2" {
		t.Fatalf("unexpected ask/target: asker=%s target=%s", st.Dice.AskerSeatID, st.Dice.TargetSeatID)
	}
	if err := a.Handle(context.Background(), "seat1", ClientMessage{Type: "dice.ask", Text: "你见过项链吗？"}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, ev := range a.Snapshot().Events {
			if ev.Type == domain.EvSpeech && ev.SeatID == "seat2" {
				return true
			}
		}
		return false
	})
	foundPrompt := false
	for _, prompt := range cap.prompts() {
		if strings.Contains(prompt, "你见过项链吗？") && strings.Contains(prompt, "和人打架的时候顺来的") {
			foundPrompt = true
		}
	}
	if !foundPrompt {
		for _, ev := range a.Snapshot().Events {
			t.Logf("EV %s seat=%s text=%s", ev.Type, ev.SeatID, ev.Text)
		}
		t.Fatalf("AI answer prompt did not carry the question and required fragment; prompts=%d", len(cap.prompts()))
	}
}
