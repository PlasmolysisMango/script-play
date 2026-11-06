package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"script-play/internal/domain"
)

// Speech slot kinds. The kind tells the UI why a seat is queued, and decides
// whether declining is allowed to be silent.
const (
	SlotTurn    = "turn"    // the scripted round: every seat states its case once
	SlotReply   = "reply"   // somebody answered this seat, so it answers first
	SlotRequest = "request" // the seat asked for the floor on its own
)

// SpeechSlot is one entry in the discussion speaking queue. Only the head of
// the queue holds the floor, which is what keeps two seats from talking over
// each other.
type SpeechSlot struct {
	SeatID  string `json:"seatId"`
	Kind    string `json:"kind"`
	ReplyTo string `json:"replyTo,omitempty"`
	Note    string `json:"note,omitempty"`
	// Text is what a human typed while somebody else held the floor. It is
	// played as soon as this slot reaches the head, so queuing never loses a
	// half-written argument.
	Text string `json:"-"`
}

// talkLimit is how many volunteered lines one agent may add on top of the
// scripted round. Without a cap five agents reply to each other forever.
func (a *Actor) talkLimitLocked(ph *domain.Phase) int {
	limit := ph.SpeechPerPlayer
	if limit <= 0 {
		limit = 1
	}
	return limit + 2
}

// resetTalkLocked clears the open-floor state. Every phase starts with an empty
// queue: a request left over from the previous round must not speak in this one.
func (a *Actor) resetTalkLocked() {
	a.talkQueue = nil
	a.talkOpen = false
	a.talkBudget = map[string]int{}
	a.talkAt = map[string]time.Time{}
	// A free-talk loop parked on talkWake outlives its phase on purpose (it
	// checks alive on every pass), so wake it once to make it notice the move.
	a.wakeTalk()
}

// enqueueTalkLocked appends a slot, skipping seats that already wait. front
// puts the seat at the head, which is how "reply first" is implemented.
func (a *Actor) enqueueTalkLocked(slot SpeechSlot, front bool) bool {
	if slot.SeatID == "" || !a.talkOpen {
		return false
	}
	if idx := a.talkIndexOfLocked(slot.SeatID); idx >= 0 {
		// Already queued: keep the words of a human who typed twice.
		if slot.Text != "" {
			a.talkQueue[idx].Text = slot.Text
		}
		if slot.ReplyTo != "" {
			a.talkQueue[idx].ReplyTo = slot.ReplyTo
			a.talkQueue[idx].Kind = SlotReply
		}
		return false
	}
	if front {
		a.talkQueue = append([]SpeechSlot{slot}, a.talkQueue...)
		return true
	}
	a.talkQueue = append(a.talkQueue, slot)
	return true
}

func (a *Actor) talkIndexOfLocked(seatID string) int {
	for i := range a.talkQueue {
		if a.talkQueue[i].SeatID == seatID {
			return i
		}
	}
	return -1
}

// takeTalkSlot pops the head of the queue.
func (a *Actor) takeTalkSlot() (SpeechSlot, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.talkQueue) == 0 {
		return SpeechSlot{}, false
	}
	slot := a.talkQueue[0]
	copy(a.talkQueue, a.talkQueue[1:])
	a.talkQueue = a.talkQueue[:len(a.talkQueue)-1]
	return slot, true
}

// wakeTalk releases the parked free-talk loop. The channel is buffered, so a
// burst of requests collapses into one wake-up.
func (a *Actor) wakeTalk() {
	if a.talkWake == nil {
		return
	}
	select {
	case a.talkWake <- struct{}{}:
	default:
	}
}

// queueWantsLocked lets agents volunteer after somebody spoke. The decision is
// heuristic and cheap on purpose: it runs on every line, so it must not call a
// model. A seat that hears its own role name always wants to answer; the rest
// is a talkativeness roll with a cooldown and a per-phase budget.
func (a *Actor) queueWantsLocked(speakerID, text string) {
	if !a.talkOpen {
		return
	}
	ph := a.m.Current(a.g)
	if ph == nil || ph.Type != domain.PhaseDiscussion {
		return
	}
	limit := a.talkLimitLocked(ph)
	now := time.Now()
	for i := range a.g.Seats {
		s := &a.g.Seats[i]
		if s.Kind != domain.SeatAgent || s.ID == speakerID {
			continue
		}
		if a.talkBudget[s.ID] >= limit || a.talkIndexOfLocked(s.ID) >= 0 {
			continue
		}
		if now.Sub(a.talkAt[s.ID]) < 4*time.Second {
			continue
		}
		mentioned := strings.Contains(text, a.speakerLocked(*s))
		want := mentioned || a.personas[s.ID].Talkativeness*0.4 > rand.Float64()
		if !want {
			continue
		}
		slot := SpeechSlot{SeatID: s.ID, Kind: SlotRequest}
		if mentioned {
			slot.Kind = SlotReply
			slot.ReplyTo = speakerID
			slot.Note = "被提到"
		}
		a.talkQueue = append(a.talkQueue, slot)
		a.talkAt[s.ID] = now
	}
}

// freeTalk turns a discussion phase into an open floor.
//
// The scripted round is only the opening statement: after it the phase no
// longer ends by itself, the host decides. Meanwhile anybody may ask for the
// floor, an answered seat answers first, and declining is a normal move.
func (a *Actor) freeTalk(ctx context.Context, gen int, ph *domain.Phase) {
	wctx, stopWait := context.WithCancel(ctx)
	defer stopWait()

	a.mu.Lock()
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	a.talkOpen = true
	a.waitCancel = stopWait
	a.phaseDone = true
	a.floor = nil
	a.talkQueue = nil
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		PhaseID: ph.ID,
		Text:    "进入自由讨论：想发言随时开口，同一时刻只有一个人说话；主持人说「进入下一阶段」才结束本阶段。",
	})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		// A phase change already reset the queue; clearing it here would wipe
		// the new round's requests.
		if gen == a.phaseGen {
			a.talkOpen = false
			a.talkQueue = nil
			a.floor = nil
		}
		a.mu.Unlock()
	}()

	for {
		if !a.alive(gen) || wctx.Err() != nil {
			return
		}
		slot, ok := a.takeTalkSlot()
		if !ok {
			select {
			case <-wctx.Done():
				return
			case <-a.talkWake:
			}
			continue
		}
		a.runTalk(ctx, gen, ph, slot)
	}
}

func (a *Actor) runTalk(ctx context.Context, gen int, ph *domain.Phase, slot SpeechSlot) {
	a.mu.Lock()
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	seat := a.g.SeatByID(slot.SeatID)
	if seat == nil {
		a.mu.Unlock()
		return
	}
	s := *seat
	a.mu.Unlock()

	if s.Kind == domain.SeatAgent {
		// Three steps is enough for "reveal the clue, then press him", and
		// small enough that one slow model cannot stall the whole table.
		a.agentTurns(ctx, gen, s, ph, slot, 3)
		return
	}
	a.humanTalk(ctx, gen, s, ph, slot)
}

// humanTalk gives one seat the floor and waits for it. Typed text that was
// queued earlier is played straight away.
func (a *Actor) humanTalk(ctx context.Context, gen int, seat domain.Seat, ph *domain.Phase, slot SpeechSlot) bool {
	if text := strings.TrimSpace(slot.Text); text != "" {
		a.appendSpeechTo(seat, text, slot.ReplyTo)
		return true
	}

	wctx, stopWait := context.WithCancel(ctx)
	defer stopWait()

	a.mu.Lock()
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return false
	}
	ch := make(chan ClientMessage, 1)
	a.waiters[seat.ID] = ch
	a.waitCancel = stopWait
	a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, ReplyTo: slot.ReplyTo, Kind: slot.Kind}
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     seat.ID,
		Visibility: SeatVisibility(seat.ID),
		PhaseID:    ph.ID,
		Text:       a.floorPromptLocked(seat, slot),
	})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if cur, ok := a.waiters[seat.ID]; ok && cur == ch {
			delete(a.waiters, seat.ID)
		}
		if a.floor != nil && a.floor.SeatID == seat.ID {
			a.floor = nil
		}
		a.mu.Unlock()
	}()

	timer, expired := a.cfg.deadline()
	if timer != nil {
		defer timer.Stop()
	}
	for {
		select {
		case <-wctx.Done():
			return false
		case <-expired:
			a.mu.Lock()
			if gen == a.phaseGen {
				a.appendLocked(domain.Event{
					Type:    domain.EvSystem,
					PhaseID: ph.ID,
					SeatID:  seat.ID,
					Text:    fmt.Sprintf("%s 想了一会儿，把发言机会让给了别人", a.speakerLocked(seat)),
				})
				a.persistLocked()
			}
			a.mu.Unlock()
			return false
		case msg := <-ch:
			switch msg.Type {
			case "host.skip":
				return false
			case "speak.pass":
				a.declined(seat, slot)
				return false
			case "speak.text":
				text := strings.TrimSpace(msg.Text)
				if text == "" {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				a.appendSpeechTo(seat, text, msg.ReplyTo)
				return true
			default:
				a.sideCommand(seat, msg)
			}
		}
	}
}

func (a *Actor) floorPromptLocked(seat domain.Seat, slot SpeechSlot) string {
	switch slot.Kind {
	case SlotReply:
		who := slot.ReplyTo
		if s := a.g.SeatByID(slot.ReplyTo); s != nil {
			who = a.speakerLocked(*s)
		}
		if slot.Note == "被点名" {
			return fmt.Sprintf("有人点了你的名字（%s），轮到你发言；不想回应可以点「不回应」", who)
		}
		return fmt.Sprintf("%s 刚刚回应了你，轮到你先回答；不想回答可以点「不回应」", who)
	default:
		return "轮到你发言；不想说可以点「不发言」把机会让给别人"
	}
}

// declined records a seat that chose not to answer. Other seats may speak
// afterwards, which is exactly the point of the "不回应" move.
func (a *Actor) declined(seat domain.Seat, slot SpeechSlot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.g.Status != domain.RoomRunning {
		return
	}
	text := fmt.Sprintf("%s 暂时不发言", a.speakerLocked(seat))
	if slot.Kind == SlotReply {
		who := slot.ReplyTo
		if s := a.g.SeatByID(slot.ReplyTo); s != nil {
			who = a.speakerLocked(*s)
		}
		text = fmt.Sprintf("%s 表示不作回应（%s）", a.speakerLocked(seat), who)
	}
	a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, PhaseID: slotPhase(a), Text: text})
	a.persistLocked()
}

func slotPhase(a *Actor) string {
	if ph := a.m.Current(a.g); ph != nil {
		return ph.ID
	}
	return ""
}

// agentTurns is the bounded agent loop: one tick may chain a couple of tool
// calls ("reveal this clue, then press him on it") but it never runs free.
//
// The engine owns the termination, not the model: speaking and declining end
// the turn, and the per-phase budget caps everything else. A persona can ask to
// talk more often, it cannot ask to never stop.
func (a *Actor) agentTurns(ctx context.Context, gen int, seat domain.Seat, ph *domain.Phase, slot SpeechSlot, maxSteps int) {
	if maxSteps <= 0 {
		maxSteps = 1
	}
	for step := 0; step < maxSteps; step++ {
		if !a.alive(gen) {
			return
		}
		a.mu.Lock()
		if a.finished || gen != a.phaseGen {
			a.mu.Unlock()
			return
		}
		// Checked per step: a chain of reveals must not outrun the budget.
		if a.talkBudget[seat.ID] >= a.talkLimitLocked(ph) {
			a.mu.Unlock()
			return
		}
		a.talkBudget[seat.ID]++
		v := VisibleFor(a.g, a.sc, &seat, VisibleOptions{MaxTranscript: 40})
		fresh := a.observeLocked(seat, 12)
		p := a.personas[seat.ID]
		pressure := a.pressureLocked(seat, ph)
		own := a.ownClueIDsLocked(seat)
		targets := a.whisperTargetsLocked(seat)
		replyName := ""
		if s := a.g.SeatByID(slot.ReplyTo); s != nil {
			replyName = a.speakerLocked(*s)
		}
		a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, Kind: slot.Kind, ReplyTo: slot.ReplyTo}
		a.persistLocked()
		a.mu.Unlock()

		a.thinking(seat, "start")
		req := a.pb.BuildAction(PromptInput{
			Visible:     v,
			Persona:     p,
			Pressure:    pressure,
			Instruction: talkInstruction(ph, slot, replyName, step),
			Tools:       a.speechToolsLocked(seat, own, targets),
			Fresh:       fresh,
		})
		text, _, err := a.generate(ctx, gen, seat, req)
		a.thinking(seat, "stop")

		a.mu.Lock()
		if a.floor != nil && a.floor.SeatID == seat.ID {
			a.floor = nil
		}
		if gen == a.phaseGen && slot.Kind == SlotTurn {
			a.speech[seat.ID]++
		}
		a.mu.Unlock()

		if err != nil {
			a.mu.Lock()
			if gen == a.phaseGen {
				a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, SeatID: seat.ID, Text: fmt.Sprintf("%s 没有回应（%v）", a.speakerLocked(seat), err)})
				a.persistLocked()
			}
			a.mu.Unlock()
			return
		}

		if !a.applyActionStep(seat, ph, parseAction(text)) {
			return
		}
		// The action left the seat mid-thought: the next step continues it
		// instead of queueing behind somebody else.
		slot = SpeechSlot{SeatID: seat.ID, Kind: SlotRequest, ReplyTo: slot.ReplyTo}
	}
}

// talkInstruction phrases this tick for the model. The wording differs because
// a scripted turn must produce a position, while an answered seat is allowed to
// say nothing at all.
func talkInstruction(ph *domain.Phase, slot SpeechSlot, replyName string, step int) string {
	if step > 0 {
		return "你可以继续补充一句，也可以就此结束这次发言。"
	}
	switch {
	case slot.Kind == SlotTurn:
		return fmt.Sprintf("%s 轮到你发言了。结合你已经知道的信息说出你的观点或回应刚才的质疑；没什么可说的可以选 pass。", ph.Name)
	case slot.Kind == SlotReply && replyName != "":
		return fmt.Sprintf("%s 刚刚回应了你。你可以正面回答，也可以表示不作回应，其他玩家随后也会继续发言。", replyName)
	case slot.Note == "被提到":
		return "刚才的话里提到了你。你可以回应，也可以表示不作回应。"
	default:
		return fmt.Sprintf("%s 的自由讨论正在进行，轮到你说话。结合你知道的信息表态；没有新内容就选 pass。", ph.Name)
	}
}

// applyActionStep executes one tool call and reports whether the seat may keep
// acting in the same tick. Speaking and declining are terminal: a seat that has
// said its line yields the floor, which is what keeps the table conversational.
func (a *Actor) applyActionStep(seat domain.Seat, ph *domain.Phase, act AgentAction) bool {
	switch act.Tool {
	case ToolPass:
		a.declined(seat, SpeechSlot{SeatID: seat.ID, Kind: SlotRequest})
		return false
	case ToolWhisper:
		// A whisper replaces the public line: the seat leaned in instead of
		// speaking up, which ends its turn like any other utterance.
		if to := strings.TrimSpace(act.To); to != "" {
			if err := a.sendWhisper(seat, to, cleanSpeech(act.Text)); err != nil {
				a.notify(seat, err.Error())
			}
		}
		return false
	case ToolReveal:
		revealed := false
		if id := strings.TrimSpace(act.ClueID); id != "" {
			if err := a.revealClue(seat, id); err != nil {
				a.notify(seat, err.Error())
			} else {
				revealed = true
			}
		}
		if text := cleanSpeech(act.Text); text != "" {
			a.appendSpeechTo(seat, text, act.ReplyTo)
			return false
		}
		// Revealing without a line leaves room for one follow-up sentence.
		return revealed
	default:
		text := cleanSpeech(act.Text)
		if text == "" {
			a.declined(seat, SpeechSlot{SeatID: seat.ID, Kind: SlotRequest})
			return false
		}
		a.appendSpeechTo(seat, text, act.ReplyTo)
		return false
	}
}

// observeLocked returns the events this seat has not seen yet and advances its
// cursor. The first call only plants the cursor: replaying the whole log into
// the opening prompt would drown the agent in history it already has.
func (a *Actor) observeLocked(seat domain.Seat, limit int) []TranscriptLine {
	cur, ok := a.seatSeen[seat.ID]
	if !ok {
		a.seatSeen[seat.ID] = a.g.NextSeq
		return nil
	}
	var out []TranscriptLine
	for _, ev := range a.g.Events {
		if ev.Seq <= cur || !EventVisibleTo(ev, &seat) {
			continue
		}
		switch ev.Type {
		case domain.EvSpeech, domain.EvWhisper, domain.EvDM, domain.EvSystem, domain.EvClueRevealed, domain.EvPhase:
		default:
			continue
		}
		out = append(out, TranscriptLine{
			Seq:     ev.Seq,
			Kind:    ev.Type,
			SeatID:  ev.SeatID,
			RoleID:  ev.RoleID,
			Speaker: ev.Speaker,
			Text:    ev.Text,
			At:      ev.At,
		})
	}
	a.seatSeen[seat.ID] = a.g.NextSeq
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// ownClueIDsLocked lists the clues a seat may reveal, so the tool schema can
// name real ids instead of asking the model to guess.
func (a *Actor) ownClueIDsLocked(seat domain.Seat) []string {
	var out []string
	for _, c := range a.sc.Clues {
		st := a.g.ClueState[c.ID]
		if st.Revealed {
			continue
		}
		if sealedClue(c, st) {
			continue
		}
		if st.FoundBySeat == seat.ID || (c.OwnerRoleID != "" && c.OwnerRoleID == seat.RoleID) {
			out = append(out, c.ID)
		}
	}
	return out
}
