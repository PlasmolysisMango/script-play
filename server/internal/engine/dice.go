package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"script-play/internal/domain"
)

// Dice-question phases (K13 群星落幕时) run in three beats per round:
//
//	rolling   every player rolls; the highest raw roll asks the lowest one
//	asking    the asker picks one of its private question cards
//	answering the target answers, and its keywords decide what must be said
//
// The engine owns the dice and the turn-taking. AI seats roll, ask and answer
// on their own; a human acts through dice.roll / dice.ask / dice.answer (typing
// into the normal speech box is accepted during the ask and answer stages).
const (
	diceStageRolling   = "rolling"
	diceStageAsking    = "asking"
	diceStageAnswering = "answering"
	diceStageDone      = "done"
)

// DiceRoll is one public roll. Bonus is the printed same-point bonus; it is
// only applied when at least two players tied on the raw value.
type DiceRoll struct {
	SeatID  string `json:"seatId"`
	RoleID  string `json:"roleId,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	Value   int    `json:"value"`
	Bonus   int    `json:"bonus,omitempty"`
}

// DiceView is the per-seat view of the dice table. Choices and Required are
// private: only the asker receives its question cards, only the target receives
// the answer fragments its keywords call for.
type DiceView struct {
	Round  int        `json:"round"`
	Rounds int        `json:"rounds"`
	Sides  int        `json:"sides"`
	Stage  string     `json:"stage"`
	Rolls  []DiceRoll `json:"rolls,omitempty"`

	AskerSeatID  string `json:"askerSeatId,omitempty"`
	TargetSeatID string `json:"targetSeatId,omitempty"`
	Question     string `json:"question,omitempty"`

	Choices  []string `json:"choices,omitempty"`
	Required []string `json:"required,omitempty"`

	NeedRoll   bool `json:"needRoll,omitempty"`
	NeedAsk    bool `json:"needAsk,omitempty"`
	NeedAnswer bool `json:"needAnswer,omitempty"`
}

type diceState struct {
	round  int
	stage  string
	rolls  []DiceRoll
	asker  string
	target string

	question string
	choices  []string
	required []string
}

// runDice drives a dice phase from the first roll to the final answer. It is a
// phase goroutine: every wait re-checks the generation so a host advance stops
// it without leaving a timer behind.
func (a *Actor) runDice(ctx context.Context, gen int) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if a.finished || gen != a.phaseGen || ph == nil || ph.Type != domain.PhaseDice {
		a.mu.Unlock()
		return
	}
	rules := ph.Dice
	a.mu.Unlock()
	if rules == nil || rules.Rounds <= 0 {
		a.completePhase(ctx, gen)
		return
	}

	for round := 1; round <= rules.Rounds; round++ {
		if !a.diceStartRound(gen, round, rules) {
			return
		}
		if !a.diceAwait(ctx, gen, func(d *diceState) bool {
			return d.stage != diceStageRolling || a.diceAllRolledLocked(d)
		}) {
			return
		}
		asker, target, ok := a.diceResolve(gen)
		if !ok {
			a.completePhase(ctx, gen)
			return
		}

		if asker.Kind == domain.SeatAgent {
			a.diceAgentAsk(asker)
		} else if !a.diceAwait(ctx, gen, func(d *diceState) bool {
			return d.stage != diceStageAsking
		}) {
			return
		}
		if !a.alive(gen) || ctx.Err() != nil {
			return
		}

		a.mu.Lock()
		stage := ""
		if a.dice != nil {
			stage = a.dice.stage
		}
		a.mu.Unlock()
		if stage == diceStageAnswering {
			if target.Kind == domain.SeatAgent {
				a.diceAgentAnswer(ctx, gen, target)
			} else if !a.diceAwait(ctx, gen, func(d *diceState) bool {
				return d.stage == diceStageDone
			}) {
				return
			}
		}

		if !a.alive(gen) || ctx.Err() != nil {
			return
		}
		if !a.cfg.AutoAdvance && round < rules.Rounds {
			select {
			case <-ctx.Done():
				return
			case <-time.After(900 * time.Millisecond):
			}
		}
	}
	a.completePhase(ctx, gen)
}

// diceStartRound resets the table for one round and lets every AI seat roll at
// once. Humans roll through dice.roll.
func (a *Actor) diceStartRound(gen, round int, rules *domain.DiceRules) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	ph := a.m.Current(a.g)
	if a.finished || gen != a.phaseGen || ph == nil || ph.Type != domain.PhaseDice {
		return false
	}
	a.dice = &diceState{round: round, stage: diceStageRolling}
	for _, s := range a.dicePlayersLocked() {
		if s.Kind == domain.SeatAgent {
			a.diceRollLocked(ph, &s)
		}
	}
	a.appendDiceLocked("")
	a.persistLocked()
	a.wakeDice()
	return true
}

// diceRoll is the human command. The server owns the die: a client cannot
// choose its own number.
func (a *Actor) diceRoll(seat domain.Seat) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ph := a.m.Current(a.g)
	if a.finished || ph == nil || ph.Type != domain.PhaseDice || a.dice == nil {
		return ErrNotAllowed{Reason: "现在不是掷骰子环节"}
	}
	if seat.Kind == domain.SeatSpectator {
		return ErrNotAllowed{Reason: "观战者不能掷骰子"}
	}
	if a.dice.stage != diceStageRolling {
		return ErrNotAllowed{Reason: "本轮已经掷完了，等提问和回答结束再掷"}
	}
	for _, r := range a.dice.rolls {
		if r.SeatID == seat.ID {
			return ErrNotAllowed{Reason: "你本轮已经掷过了"}
		}
	}
	a.diceRollLocked(ph, &seat)
	a.appendDiceLocked("")
	a.persistLocked()
	a.wakeDice()
	return nil
}

// diceRollLocked appends one die for a player. Callers hold mu.
func (a *Actor) diceRollLocked(ph *domain.Phase, seat *domain.Seat) {
	sides := 6
	if ph.Dice != nil && ph.Dice.Sides > 0 {
		sides = ph.Dice.Sides
	}
	a.dice.rolls = append(a.dice.rolls, DiceRoll{
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(*seat),
		Value:   rand.Intn(sides) + 1,
	})
}

func (a *Actor) diceAllRolledLocked(d *diceState) bool {
	for _, s := range a.dicePlayersLocked() {
		found := false
		for _, r := range d.rolls {
			if r.SeatID == s.ID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// diceResolve applies the same-point bonuses and names the asker and target.
// The printed rule compares raw rolls first; the bonus only settles a tie, so
// a 5+3 does not beat a clean 6.
func (a *Actor) diceResolve(gen int) (domain.Seat, domain.Seat, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ph := a.m.Current(a.g)
	if a.finished || gen != a.phaseGen || ph == nil || ph.Type != domain.PhaseDice || a.dice == nil {
		return domain.Seat{}, domain.Seat{}, false
	}
	d := a.dice
	if len(d.rolls) < 2 {
		d.stage = diceStageDone
		a.appendDiceLocked("玩家人数不足，本轮跳过。")
		a.persistLocked()
		a.wakeDice()
		return domain.Seat{}, domain.Seat{}, false
	}

	counts := map[int]int{}
	for _, r := range d.rolls {
		counts[r.Value]++
	}
	for i := range d.rolls {
		if counts[d.rolls[i].Value] > 1 {
			d.rolls[i].Bonus = ph.Dice.TieBreak[d.rolls[i].RoleID]
		}
	}

	high := d.rolls[0]
	low := d.rolls[0]
	for _, r := range d.rolls[1:] {
		if r.Value > high.Value || (r.Value == high.Value && r.Bonus > high.Bonus) {
			high = r
		}
		if r.Value < low.Value || (r.Value == low.Value && r.Bonus < low.Bonus) {
			low = r
		}
	}
	if high.SeatID == low.SeatID {
		d.stage = diceStageDone
		a.appendDiceLocked("掷骰没有分出提问双方，本轮跳过。")
		a.persistLocked()
		a.wakeDice()
		return domain.Seat{}, domain.Seat{}, false
	}

	d.asker = high.SeatID
	d.target = low.SeatID
	d.stage = diceStageAsking
	d.question, d.required = "", nil
	d.choices = nil
	if ph.Dice != nil {
		d.choices = append([]string(nil), ph.Dice.Questions[high.RoleID]...)
	}
	if len(d.choices) == 0 {
		d.choices = []string{"请说说你对这件事的看法。"}
	}
	askerSeat := a.g.SeatByID(d.asker)
	targetSeat := a.g.SeatByID(d.target)
	names := make([]string, 0, len(d.rolls))
	for _, r := range d.rolls {
		val := fmt.Sprintf("%s %d 点", r.Speaker, r.Value)
		if r.Bonus > 0 {
			val += fmt.Sprintf("（同点 +%d）", r.Bonus)
		}
		names = append(names, val)
	}
	a.appendDiceLocked(fmt.Sprintf("第 %d/%d 轮：%s。%s 向 %s 提问。",
		d.round, ph.Dice.Rounds, strings.Join(names, "，"), high.Speaker, low.Speaker))
	a.persistLocked()
	a.wakeDice()
	return *askerSeat, *targetSeat, true
}

// diceAgentAsk picks a question card for an AI asker. It is a table decision,
// not a model call: the cards are printed on the role sheet.
func (a *Actor) diceAgentAsk(asker domain.Seat) {
	a.mu.Lock()
	if a.dice == nil || a.dice.stage != diceStageAsking || a.dice.asker != asker.ID || len(a.dice.choices) == 0 {
		a.mu.Unlock()
		return
	}
	question := a.dice.choices[rand.Intn(len(a.dice.choices))]
	a.mu.Unlock()
	a.diceSetQuestion(asker, question)
}

// diceAsk is the human command for the asking stage. Typing a question into
// the normal speech box lands here as well.
func (a *Actor) diceAsk(seat domain.Seat, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrNotAllowed{Reason: "问题不能为空"}
	}
	a.mu.Lock()
	if a.dice == nil || a.dice.stage != diceStageAsking {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "现在不是提问环节"}
	}
	if a.dice.asker != seat.ID {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "本轮不是你提问"}
	}
	a.mu.Unlock()
	a.diceSetQuestion(seat, text)
	return nil
}

func (a *Actor) diceSetQuestion(asker domain.Seat, question string) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if a.finished || a.dice == nil || ph == nil || ph.Type != domain.PhaseDice ||
		a.dice.stage != diceStageAsking || a.dice.asker != asker.ID {
		a.mu.Unlock()
		return
	}
	gen := a.phaseGen
	a.dice.question = question
	a.dice.required = diceRequiredLocked(ph.Dice, a.g.SeatByID(a.dice.target), question)
	a.dice.stage = diceStageAnswering
	a.appendDiceLocked("")
	a.persistLocked()
	a.mu.Unlock()
	if a.alive(gen) {
		a.appendSpeechTo(asker, question, "")
	}
	a.wakeDice()
}

// diceRequiredLocked finds the target's keywords inside the question and
// returns their mandatory answer fragments.
func diceRequiredLocked(rules *domain.DiceRules, target *domain.Seat, question string) []string {
	if rules == nil || target == nil {
		return nil
	}
	words := rules.Keywords[target.RoleID]
	if len(words) == 0 {
		return nil
	}
	var out []string
	for word, answer := range words {
		if word != "" && strings.Contains(question, word) {
			out = append(out, answer)
		}
	}
	sort.Strings(out)
	return out
}

// diceAnswer is the human answer command. The required fragments are shown to
// the target; the engine does not reject an answer for paraphrasing them, but
// an AI target is told to say them verbatim.
func (a *Actor) diceAnswer(seat domain.Seat, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrNotAllowed{Reason: "回答不能为空"}
	}
	a.mu.Lock()
	if a.dice == nil || a.dice.stage != diceStageAnswering {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "现在不是回答环节"}
	}
	if a.dice.target != seat.ID {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "本轮不是你回答"}
	}
	gen := a.phaseGen
	a.dice.stage = diceStageDone
	a.appendDiceLocked("")
	a.persistLocked()
	a.mu.Unlock()
	if a.alive(gen) {
		a.appendSpeechTo(seat, text, "")
	}
	a.wakeDice()
	return nil
}

// diceSpeak accepts the normal speech box during the ask and answer stages, so
// a player does not have to discover a separate command.
func (a *Actor) diceSpeak(seat domain.Seat, text string) error {
	a.mu.Lock()
	stage, asker, target := "", "", ""
	if a.dice != nil {
		stage, asker, target = a.dice.stage, a.dice.asker, a.dice.target
	}
	a.mu.Unlock()
	switch {
	case stage == diceStageAsking && asker == seat.ID:
		return a.diceAsk(seat, text)
	case stage == diceStageAnswering && target == seat.ID:
		return a.diceAnswer(seat, text)
	case stage == diceStageAnswering:
		return ErrNotAllowed{Reason: "请先让被提问的人回答"}
	case stage == diceStageRolling:
		return ErrNotAllowed{Reason: "先掷骰子，分出提问双方再发言"}
	default:
		return ErrNotAllowed{Reason: "现在不是发言时间"}
	}
}

// diceAgentAnswer asks the target's model for an in-character answer. The
// prompt carries the question and any mandatory fragments; the answer goes to
// the public transcript as a normal speech line.
func (a *Actor) diceAgentAnswer(ctx context.Context, gen int, seat domain.Seat) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if a.finished || gen != a.phaseGen || ph == nil || ph.Type != domain.PhaseDice ||
		a.dice == nil || a.dice.stage != diceStageAnswering || a.dice.target != seat.ID {
		a.mu.Unlock()
		return
	}
	question := a.dice.question
	required := append([]string(nil), a.dice.required...)
	asker := a.g.SeatByID(a.dice.asker)
	askerName := "对方"
	if asker != nil {
		askerName = a.speakerLocked(*asker)
	}
	v := VisibleFor(a.g, a.sc, &seat, VisibleOptions{MaxTranscript: 40})
	fresh := a.observeLocked(seat, 12)
	p := a.personas[seat.ID]
	pressure := a.pressureLocked(seat, ph)
	a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, Kind: "dice"}
	a.persistLocked()
	a.mu.Unlock()

	a.thinking(seat, "start")
	req := a.pb.BuildAction(PromptInput{
		Visible:     v,
		Persona:     p,
		Pressure:    pressure,
		Instruction: diceAnswerInstruction(askerName, question, required),
		Fresh:       fresh,
	})
	text, _, err := a.generate(ctx, gen, seat, req, visibleCorpus(v)...)
	a.thinking(seat, "stop")

	a.mu.Lock()
	if a.floor != nil && a.floor.SeatID == seat.ID {
		a.floor = nil
	}
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	if err != nil {
		a.appendLocked(domain.Event{
			Type:    domain.EvSystem,
			PhaseID: a.dicePhaseIDLocked(),
			Text:    fmt.Sprintf("%s 没有回答（%v）", a.speakerLocked(seat), err),
		})
	}
	a.dice.stage = diceStageDone
	a.appendDiceLocked("")
	a.persistLocked()
	a.mu.Unlock()

	if text = cleanSpeech(parseAction(text).Text); text != "" && a.alive(gen) {
		a.appendSpeechTo(seat, text, "")
	}
	a.wakeDice()
}

func diceAnswerInstruction(askerName, question string, required []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s 掷骰胜出，向你提问：「%s」。请以你角色的身份当面回答，只输出回答正文，不要提问题卡、关键词或必答内容这些词，也不要念剧本。", askerName, question)
	if len(required) > 0 {
		b.WriteString(" 你的回答必须原样包含以下内容：")
		b.WriteString(strings.Join(required, "；"))
		b.WriteString("。")
	}
	b.WriteString(" 保留分寸，不要主动交代问题没有问到的秘密。")
	return b.String()
}

// diceViewLocked renders the private half of the table for one seat.
func (a *Actor) diceViewLocked(seat *domain.Seat) *DiceView {
	d := a.dice
	if d == nil {
		return nil
	}
	ph := a.m.Current(a.g)
	rounds, sides := 0, 6
	if ph != nil && ph.Dice != nil {
		rounds = ph.Dice.Rounds
		if ph.Dice.Sides > 0 {
			sides = ph.Dice.Sides
		}
	}
	v := &DiceView{
		Round:        d.round,
		Rounds:       rounds,
		Sides:        sides,
		Stage:        d.stage,
		Rolls:        append([]DiceRoll(nil), d.rolls...),
		AskerSeatID:  d.asker,
		TargetSeatID: d.target,
		Question:     d.question,
	}
	if seat == nil || seat.Kind == domain.SeatSpectator {
		return v
	}
	switch d.stage {
	case diceStageRolling:
		v.NeedRoll = true
		for _, r := range d.rolls {
			if r.SeatID == seat.ID {
				v.NeedRoll = false
				break
			}
		}
	case diceStageAsking:
		if d.asker == seat.ID {
			v.NeedAsk = true
			v.Choices = append([]string(nil), d.choices...)
		}
	case diceStageAnswering:
		if d.target == seat.ID {
			v.NeedAnswer = true
			v.Required = append([]string(nil), d.required...)
		}
	}
	return v
}

func (a *Actor) dicePlayersLocked() []domain.Seat {
	out := make([]domain.Seat, 0, len(a.g.Seats))
	for _, s := range a.g.Seats {
		if s.Kind != domain.SeatSpectator {
			out = append(out, s)
		}
	}
	return out
}

func (a *Actor) appendDiceLocked(text string) {
	a.appendLocked(domain.Event{
		Type:    domain.EvDice,
		PhaseID: a.dicePhaseIDLocked(),
		Text:    text,
	})
}

func (a *Actor) dicePhaseIDLocked() string {
	if ph := a.m.Current(a.g); ph != nil {
		return ph.ID
	}
	return ""
}

func (a *Actor) wakeDice() {
	if a.diceWake == nil {
		return
	}
	select {
	case a.diceWake <- struct{}{}:
	default:
	}
}

// diceAwait parks until ready holds. It re-checks the generation on every pass,
// so host.skip releases it without a dedicated cancel channel.
func (a *Actor) diceAwait(ctx context.Context, gen int, ready func(*diceState) bool) bool {
	for {
		a.mu.Lock()
		if a.finished || gen != a.phaseGen || a.dice == nil {
			a.mu.Unlock()
			return false
		}
		if ready(a.dice) {
			a.mu.Unlock()
			return true
		}
		wake := a.diceWake
		a.mu.Unlock()
		if wake == nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-wake:
		}
	}
}
