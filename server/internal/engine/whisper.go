package engine

import (
	"context"
	"fmt"
	"strings"

	"script-play/internal/domain"
)

// A whisper is the private lane of a discussion: two people lean in while the
// rest of the table keeps talking.
//
// It is deliberately outside the speaking queue. Making a private exchange hold
// the floor would freeze five players for the length of a secret, which is not
// how a table works; and a whisper touches neither the keyword gate nor the
// recap, because the table never heard it.
func (a *Actor) sendWhisper(from domain.Seat, toID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrNotAllowed{Reason: "私聊内容是空的"}
	}
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if a.g.Status != domain.RoomRunning || ph == nil {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "对局没在进行"}
	}
	if from.Kind == domain.SeatSpectator {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "观战者不能私聊"}
	}
	if ph.Type != domain.PhaseDiscussion {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "只有讨论阶段可以私聊，现在请把话说到明面上"}
	}
	to := a.g.SeatByID(toID)
	switch {
	case to == nil || to.Kind == domain.SeatSpectator || to.RoleID == "":
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "私聊对象不在座"}
	case to.ID == from.ID:
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "不能和自己私聊"}
	}
	target := *to
	a.whisperLocked(from, target, ph, text)
	gen := a.phaseGen
	a.mu.Unlock()

	// Only a human draws an answer. Two agents left to reply to each other
	// would whisper forever, and every line costs a model call.
	if from.Kind == domain.SeatHuman && target.Kind == domain.SeatAgent {
		go a.replyWhisper(gen, from, target, text)
	}
	return nil
}

// whisperLocked writes the pair-visible line. Callers hold a.mu.
func (a *Actor) whisperLocked(from, to domain.Seat, ph *domain.Phase, text string) {
	a.appendLocked(domain.Event{
		Type:       domain.EvWhisper,
		PhaseID:    ph.ID,
		SeatID:     from.ID,
		RoleID:     from.RoleID,
		Speaker:    a.speakerLocked(from),
		Visibility: PairVisibility(from.ID, to.ID),
		Text:       text,
		Meta: map[string]string{
			"to":       to.ID,
			"toName":   a.speakerLocked(to),
			"fromName": a.speakerLocked(from),
		},
	})
	a.persistLocked()
}

// replyWhisper asks an AI seat to answer one private line. It is single-flight
// per seat: the second whisper of a rapid pair is dropped rather than queued,
// because a human who just sent two lines is waiting on one answer anyway.
func (a *Actor) replyWhisper(gen int, from, to domain.Seat, text string) {
	a.mu.Lock()
	if a.finished || gen != a.phaseGen || a.whisperBusy[to.ID] {
		a.mu.Unlock()
		return
	}
	a.whisperBusy[to.ID] = true
	v := VisibleFor(a.g, a.sc, &to, VisibleOptions{MaxTranscript: 30})
	p := a.personas[to.ID]
	who := a.speakerLocked(from)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.whisperBusy, to.ID)
		a.mu.Unlock()
	}()

	req := a.pb.BuildAction(PromptInput{
		Visible: v,
		Persona: p,
		Tools:   whisperReplyTools(from),
		Fresh:   nil,
		Instruction: fmt.Sprintf(
			"%s 刚刚私下对你说：%s\n这是只说给你们两个人听的话。用 whisper 回他一句，别人听不到；不想接话就 pass。",
			who, text),
	})
	ctx := a.base
	if ctx == nil {
		ctx = context.Background()
	}

	a.thinking(to, "start")
	raw, _, err := a.generate(ctx, gen, to, req)
	a.thinking(to, "stop")
	if err != nil {
		// A provider failure must not leave the sender staring at a message that
		// looks delivered but never answered.
		a.whisperPass(gen, from, to)
		return
	}
	act := parseAction(raw)
	line := cleanSpeech(act.Text)
	if act.Tool != ToolWhisper || line == "" {
		a.whisperPass(gen, from, to)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	// The phase may have moved while the model was thinking: a whisper that
	// lands in the voting round would be both illegal and confusing.
	if a.finished || gen != a.phaseGen {
		return
	}
	if ph := a.m.Current(a.g); ph != nil && ph.Type == domain.PhaseDiscussion {
		a.whisperLocked(to, from, ph, line)
	}
}

// whisperPass tells the sender that the seat chose not to answer. Silence on
// screen looks like a lost message, so the closed loop has to be visible.
func (a *Actor) whisperPass(gen int, from, to domain.Seat) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || gen != a.phaseGen {
		return
	}
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     from.ID,
		Visibility: SeatVisibility(from.ID),
		Text:       fmt.Sprintf("（%s 没有接这条私聊）", a.speakerLocked(to)),
	})
	a.persistLocked()
}

// whisperTargetsLocked lists the seats a private line may be addressed to.
func (a *Actor) whisperTargetsLocked(seat domain.Seat) []domain.Seat {
	var out []domain.Seat
	for _, s := range a.g.Seats {
		if s.ID == seat.ID || s.Kind == domain.SeatSpectator || s.RoleID == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
