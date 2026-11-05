package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"script-play/internal/domain"
	"script-play/internal/llm"
	"script-play/internal/persona"
)

// GameSink persists a consistent snapshot of the game.
type GameSink interface {
	PutGame(*domain.Game)
}

// Broadcaster pushes an event to connected clients. Implementations must apply
// per-seat visibility before writing anything to a socket.
type Broadcaster interface {
	Publish(domain.Event)
}

// ClientMessage is one inbound websocket command.
type ClientMessage struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	ClueID string `json:"clueId,omitempty"`
	RoleID string `json:"roleId,omitempty"`
	// SeatIDs carries the host's layout for a host_assigned phase.
	SeatIDs []string `json:"seatIds,omitempty"`
	// ReplyTo names the seat a speech answers. The engine moves that seat to the
	// front of the discussion queue so it gets to answer first (or decline).
	ReplyTo string `json:"replyTo,omitempty"`
}

// ActorConfig tunes timing.
type ActorConfig struct {
	FloorTimeout time.Duration
	AgentTimeout time.Duration
	// WaitForHost keeps the speech floor open after the deadline instead of
	// skipping the seat; the host must end the phase. Rooms opted into this.
	WaitForHost bool
	// Unlimited drops the deadline entirely: speech, search and vote waits park
	// on the message channel until the seat acts or the host skips the phase.
	Unlimited bool
	// AutoAdvance keeps the old behaviour where finishing the actions of a phase
	// steps straight into the next one. Rooms are host-driven by default: a phase
	// ends with a "waiting for host" gate the host must release.
	AutoAdvance bool
}

func (c ActorConfig) floor() time.Duration {
	if c.FloorTimeout <= 0 {
		return 90 * time.Second
	}
	return c.FloorTimeout
}

func (c ActorConfig) agent() time.Duration {
	if c.AgentTimeout <= 0 {
		return 120 * time.Second
	}
	return c.AgentTimeout
}

// deadline arms the per-turn timer, or returns a nil channel when the room runs
// without a limit (a nil channel simply never fires in a select).
func (c ActorConfig) deadline() (*time.Timer, <-chan time.Time) {
	if c.Unlimited {
		return nil, nil
	}
	t := time.NewTimer(c.floor())
	return t, t.C
}

// Actor owns one game. Every mutation happens under mu; LLM calls happen
// outside the lock and only ever see a Visible snapshot.
type Actor struct {
	mu   sync.Mutex
	g    *domain.Game
	sc   *domain.Script
	m    *Machine
	sink GameSink
	hub  Broadcaster
	reg  *llm.Registry
	pb   *PromptBuilder
	cfg  ActorConfig

	// base is the actor's own lifetime context, captured in Start. Phase loops
	// must not run on the connection that pressed "next phase": that request
	// context dies as soon as the message is handled, which turns every waiter
	// into an immediate cancel and the walk into a hot loop.
	base     context.Context
	personas map[string]persona.Persona
	waiters  map[string]chan ClientMessage
	// waitCancel releases the single pending waiter when the phase moves on,
	// so a waiter with no timer cannot outlive its phase.
	waitCancel context.CancelFunc

	speech   map[string]int
	searched map[string]int
	floor    *Floor
	// talkQueue is the free-discussion speaking queue. Only the head of the
	// queue holds the floor, so two seats never talk at the same time.
	talkQueue []SpeechSlot
	// talkOpen marks a discussion phase that already played its scripted round
	// and now takes open-floor requests. The phase ends when the host advances.
	talkOpen bool
	// talkWake releases the idle free-talk loop when a request is queued.
	talkWake chan struct{}
	// talkBudget caps how many extra lines one agent may volunteer per phase,
	// and talkAt throttles self-queueing so a table of agents cannot ping-pong.
	talkBudget map[string]int
	talkAt     map[string]time.Time
	// seatSeen is each agent's read cursor into the event log. Without it the
	// model re-reads the rolling transcript and cannot tell a fresh accusation
	// from one it already answered.
	seatSeen map[string]int
	// recapBusy keeps at most one compression in flight. Each phase asks for a
	// recap; without the flag a fast table would stack them on a 2 vCPU box.
	recapBusy bool
	// order is this phase's turn order and orderPos is where the walk stands.
	// host_assigned leaves both empty until the host lays the round out.
	order    []domain.Seat
	orderPos int
	// awaitingOrder is true while the table waits for that layout, so the host
	// view can show the re-ordering panel instead of an idle table.
	awaitingOrder bool
	// phaseDone marks a phase whose actions are finished but which the host has
	// not released yet: no waiter is running, the table is just waiting.
	phaseDone bool
	phaseGen  int
	started   bool
	finished  bool

	// done is closed once the game ends, so the room manager can free the
	// concurrency slot instead of keeping a finished actor forever.
	done chan struct{}
}

// NewActor wires a game to its script, providers and transports.
func NewActor(g *domain.Game, sc *domain.Script, reg *llm.Registry, sink GameSink, hub Broadcaster, cfg ActorConfig) *Actor {
	a := &Actor{
		g:        g,
		sc:       sc,
		m:        NewMachine(sc),
		sink:     sink,
		hub:      hub,
		reg:      reg,
		pb:       NewPromptBuilder(),
		cfg:      cfg,
		personas: map[string]persona.Persona{},
		waiters:  map[string]chan ClientMessage{},
		speech:   map[string]int{},
		searched: map[string]int{},
		// buffered so queueing never blocks the command that queued.
		talkWake:   make(chan struct{}, 1),
		talkBudget: map[string]int{},
		talkAt:     map[string]time.Time{},
		seatSeen:   map[string]int{},
		done:       make(chan struct{}),
	}
	for _, s := range g.Seats {
		a.personas[s.ID] = persona.Get(s.PersonaID)
	}
	return a
}

// GameID returns the game id.
func (a *Actor) GameID() string { return a.g.ID }

// Done is closed when the game reaches its final phase or is stopped. The
// channel exists so the room manager can drop the actor and free its slot.
func (a *Actor) Done() <-chan struct{} { return a.done }

// Status returns the game status.
func (a *Actor) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.g.Status
}

// AddSeat inserts a seat into the live game. The manager calls this for a
// spectator joining a running game: the actor owns the state, so writing to
// the store snapshot would be overwritten by the next event.
func (a *Actor) AddSeat(seat domain.Seat) (domain.Seat, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.g.Status != domain.RoomLobby && seat.Kind != domain.SeatSpectator {
		return domain.Seat{}, ErrNotAllowed{Reason: "对局已经开始，只能以观战身份进入"}
	}
	if a.g.SeatByID(seat.ID) != nil {
		return domain.Seat{}, ErrNotAllowed{Reason: "座位已存在"}
	}
	a.g.Seats = append(a.g.Seats, seat)
	a.personas[seat.ID] = persona.Get(seat.PersonaID)
	a.appendLocked(domain.Event{
		Type: domain.EvSystem,
		Text: fmt.Sprintf("%s 进入观战", a.speakerLocked(seat)),
	})
	a.persistLocked()
	return seat, nil
}

// Start kicks off phase one. It is safe to call twice.
func (a *Actor) Start(ctx context.Context) {
	a.mu.Lock()
	if a.started || a.finished {
		a.mu.Unlock()
		return
	}
	a.started = true
	a.base = ctx
	a.g.Status = domain.RoomRunning
	now := time.Now().UTC()
	a.g.StartedAt = &now
	a.persistLocked()
	a.mu.Unlock()
	a.beginPhase(ctx)
}

// ---- state access ----

// SearchOption is one clue this seat may pick during the search phase.
//
// It carries only the hint printed on the back of the physical card ("背后的
// 培养仓"). The title and the content both describe the evidence itself, so
// showing either of them here would hand the answer to the searching seat
// before it ever picked the card.
type SearchOption struct {
	ID   string `json:"id"`
	Hint string `json:"hint"`
}

// SeatState is what one connection is allowed to see.
type SeatState struct {
	Visible   Visible           `json:"visible"`
	Phases    []PhaseSummary    `json:"phases"`
	Floor     *Floor            `json:"floor,omitempty"`
	Status    string            `json:"status"`
	VoteCount int               `json:"voteCount"`
	Votes     map[string]int    `json:"votes,omitempty"`
	Personas  map[string]string `json:"personas,omitempty"`
	// PhaseDone tells the client the actions are finished and the host has not
	// pressed "next phase" yet, so the table shows a waiting banner instead of
	// keys nobody can press.
	PhaseDone bool `json:"phaseDone"`
	// Search lists the clues this seat can still pick in a search phase, so the
	// client never has to guess clue ids. It stays empty in random mode, where
	// Draw carries the remaining draws instead.
	Search []SearchOption `json:"search,omitempty"`
	Draw   int            `json:"draw,omitempty"`
	// Order is the seat ids in this phase's turn order, repeated in the order the
	// engine will walk them; the table uses it to see who is coming next.
	Order []string `json:"order,omitempty"`
	// TalkOpen is true while the discussion floor is open to requests. Queue is
	// who is waiting for it, head first; only the head may speak.
	TalkOpen bool         `json:"talkOpen,omitempty"`
	Queue    []SpeechSlot `json:"queue,omitempty"`
	// Prompt is the one thing the table is blocked on right now. Only the host
	// ever receives one.
	Prompt *TurnPrompt `json:"prompt,omitempty"`
}

// TurnOption is one player the host may place into the running order.
type TurnOption struct {
	SeatID  string `json:"seatId"`
	Role    string `json:"role"`
	Display string `json:"display"`
	Kind    string `json:"kind,omitempty"`
}

// TurnPrompt tells a client what the engine is waiting for. Right now that is
// only a host-assigned running order.
type TurnPrompt struct {
	Kind    string       `json:"kind"`
	PhaseID string       `json:"phaseId"`
	Text    string       `json:"text,omitempty"`
	Seats   []TurnOption `json:"seats,omitempty"`
}

// StateForSeat renders the filtered state of one seat.
func (a *Actor) StateForSeat(seatID string) (SeatState, bool) {
	a.mu.Lock()
	seat := a.g.SeatByID(seatID)
	if seat == nil {
		a.mu.Unlock()
		return SeatState{}, false
	}
	st := a.stateLocked(seat, false)
	a.mu.Unlock()
	return st, true
}

func (a *Actor) stateLocked(seat *domain.Seat, includeTruth bool) SeatState {
	v := VisibleFor(a.g, a.sc, seat, VisibleOptions{IncludeTruth: includeTruth, MaxTranscript: 200})
	st := SeatState{
		Visible:   v,
		Phases:    a.m.Summary(a.g),
		Status:    a.g.Status,
		PhaseDone: a.phaseDone,
	}
	if a.floor != nil {
		f := *a.floor
		st.Floor = &f
	}
	if includeTruth || (seat != nil && (seat.Kind == domain.SeatSpectator || seat.IsHost)) {
		// spectators and the host get the tally so they can follow the story
		st.Votes = tally(a.g.Votes)
	}
	st.VoteCount = len(a.g.Votes)
	if seat != nil && seat.Kind != domain.SeatSpectator {
		if ph := a.m.Current(a.g); ph != nil && ph.Type == domain.PhaseSearch {
			if ph.SearchMode == domain.SearchRandom {
				st.Draw = remainingPicks(a.searched[seat.ID], ph.PerPlayerPick)
			} else {
				st.Search = a.searchOptionsLocked(*seat, ph)
			}
		}
	}
	for _, s := range a.order {
		st.Order = append(st.Order, s.ID)
	}
	st.TalkOpen = a.talkOpen
	for _, slot := range a.talkQueue {
		st.Queue = append(st.Queue, slot)
	}
	if a.awaitingOrder && seat != nil && seat.IsHost {
		st.Prompt = a.orderPromptLocked()
	}
	return st
}

// remainingPicks is how many turns one seat still owes this phase.
func remainingPicks(done, limit int) int {
	if limit <= 0 {
		limit = 1
	}
	if done >= limit {
		return 0
	}
	return limit - done
}

// ---- inbound commands ----

// ErrNotAllowed is returned when a seat tries an action outside its phase.
type ErrNotAllowed struct{ Reason string }

func (e ErrNotAllowed) Error() string { return e.Reason }

// Handle processes one client command for a seat.
func (a *Actor) Handle(ctx context.Context, seatID string, msg ClientMessage) error {
	a.mu.Lock()
	seat := a.g.SeatByID(seatID)
	if seat == nil {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "座位不存在"}
	}
	waitCh := a.waiters[seatID]
	phase := a.m.Current(a.g)
	a.mu.Unlock()

	switch msg.Type {
	case "ping":
		return nil
	case "host.skip":
		if !seat.IsHost {
			return ErrNotAllowed{Reason: "只有主持人可以跳过"}
		}
		a.requestAdvance(ctx)
		return nil
	case "speak.request":
		// Ask for the floor in an open discussion instead of typing first.
		return a.requestFloor(*seat)
	}

	if waitCh != nil {
		select {
		case waitCh <- msg:
			return nil
		default:
		}
	}

	// Free-form handling outside a pending prompt (or after the wait expired).
	switch msg.Type {
	case "speak.text":
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			return ErrNotAllowed{Reason: "发言内容为空"}
		}
		if phase != nil && phase.Type == domain.PhaseOpening {
			return ErrNotAllowed{Reason: "开场阶段由主持人推进"}
		}
		if a.holdTalk(*seat, text, msg.ReplyTo) {
			return nil
		}
		a.appendSpeechTo(*seat, text, msg.ReplyTo)
		return nil
	case "speak.pass":
		// Declining without holding the floor is a no-op: the seat simply has
		// nothing queued, and an error frame here would be noise.
		return nil
	case "clue.reveal":
		return a.revealClue(*seat, msg.ClueID)
	default:
		return ErrNotAllowed{Reason: "当前阶段不接受该操作"}
	}
}

// requestFloor puts a seat at the end of the discussion queue.
func (a *Actor) requestFloor(seat domain.Seat) error {
	a.mu.Lock()
	if !a.talkOpen {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "现在不是自由讨论，轮到你时再发言"}
	}
	if (a.floor != nil && a.floor.SeatID == seat.ID) || a.talkIndexOfLocked(seat.ID) >= 0 {
		a.mu.Unlock()
		return nil
	}
	a.talkQueue = append(a.talkQueue, SpeechSlot{SeatID: seat.ID, Kind: SlotRequest})
	pos := len(a.talkQueue)
	a.mu.Unlock()
	a.wakeTalk()
	a.notify(seat, fmt.Sprintf("已加入发言队列，你排在第 %d 位", pos))
	return nil
}

// holdTalk queues a line typed while somebody else holds the floor. It reports
// whether the text was queued, so the caller knows not to speak immediately.
func (a *Actor) holdTalk(seat domain.Seat, text, replyTo string) bool {
	a.mu.Lock()
	if !a.talkOpen {
		a.mu.Unlock()
		return false
	}
	if a.floor != nil && a.floor.SeatID == seat.ID {
		a.mu.Unlock()
		return false
	}
	busy := a.floor != nil
	queued := a.talkIndexOfLocked(seat.ID) >= 0
	if !busy && !queued {
		// Nobody holds the floor and this seat is not waiting: it is its turn
		// right now, so speak immediately instead of adding a pointless hop.
		a.mu.Unlock()
		return false
	}
	if queued {
		idx := a.talkIndexOfLocked(seat.ID)
		a.talkQueue[idx].Text = text
		if replyTo != "" {
			a.talkQueue[idx].ReplyTo = replyTo
			a.talkQueue[idx].Kind = SlotReply
		}
	} else {
		a.talkQueue = append(a.talkQueue, SpeechSlot{SeatID: seat.ID, Kind: SlotRequest, ReplyTo: replyTo, Text: text})
	}
	pos := a.talkIndexOfLocked(seat.ID) + 1
	a.mu.Unlock()
	a.wakeTalk()
	a.notify(seat, fmt.Sprintf("现在有人在说话，你的发言排在第 %d 位，轮到你时会自动发出", pos))
	return true
}

// sideCommand applies actions that stay legal while this seat owes a prompt.
// The waiters used to drop anything but their own command, so revealing a clue
// on your search turn looked like a dead button on the client.
func (a *Actor) sideCommand(seat domain.Seat, msg ClientMessage) bool {
	if msg.Type != "clue.reveal" {
		return false
	}
	if err := a.revealClue(seat, msg.ClueID); err != nil {
		a.notify(seat, err.Error())
	}
	return true
}

// ---- phase driving ----

func (a *Actor) beginPhase(ctx context.Context) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if ph == nil {
		a.finishLocked("剧本已结束")
		a.mu.Unlock()
		return
	}
	gen := a.phaseGen
	a.speech = map[string]int{}
	a.searched = map[string]int{}
	a.floor = nil
	a.phaseDone = false
	a.resetTalkLocked()
	// Just the name: the host's "进入下一阶段：…" line right above already
	// says what happened, so the divider only has to mark where.
	a.appendLocked(domain.Event{
		Type:    domain.EvPhase,
		PhaseID: ph.ID,
		Text:    ph.Name,
		Meta:    map[string]string{"phaseType": ph.Type},
	})
	if strings.TrimSpace(ph.DMScript) != "" {
		a.appendLocked(domain.Event{Type: domain.EvDM, Speaker: "主持人", PhaseID: ph.ID, Text: ph.DMScript})
	}
	a.persistLocked()
	a.mu.Unlock()

	// Fold what the previous phases said into a recap before the table speaks
	// again. It runs off the critical path: the phase never waits for it.
	a.startRecap(gen)

	switch ph.Type {
	case domain.PhaseReading, domain.PhaseOpening:
		// Nothing to collect: everyone reads the role book (reading) or the host
		// sets the scene (opening), then the host releases the phase.
		go a.completePhase(ctx, gen)
	case domain.PhaseSearch:
		go a.runSearch(ctx, gen)
	case domain.PhaseDiscussion:
		go a.runDiscussion(ctx, gen)
	case domain.PhaseVote:
		go a.runVote(ctx, gen)
	case domain.PhaseReveal:
		a.runReveal()
		go a.completePhase(ctx, gen)
	default:
		go a.completePhase(ctx, gen)
	}
}

// completePhase closes the action part of the current phase.
//
// Rooms are host-driven: the table parks here with a "waiting for host" event
// and only the host's "进入下一阶段" moves on, so a silent seat or a chatty
// table never rushes the story. AutoAdvance (tests, unattended demos) keeps the
// old behaviour of stepping straight into the next phase.
func (a *Actor) completePhase(ctx context.Context, gen int) {
	a.mu.Lock()
	if a.finished || gen != a.phaseGen || a.phaseDone {
		a.mu.Unlock()
		return
	}
	a.phaseDone = true
	a.floor = nil
	phaseID, name := "", ""
	if ph := a.m.Current(a.g); ph != nil {
		phaseID, name = ph.ID, ph.Name
	}
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		PhaseID: phaseID,
		Text:    fmt.Sprintf("本阶段已完成（%s），等待主持人进入下一阶段", name),
	})
	a.persistLocked()
	auto := a.cfg.AutoAdvance
	a.mu.Unlock()
	if auto {
		a.advance(ctx, gen)
	}
}

func (a *Actor) advance(ctx context.Context, gen int) {
	a.mu.Lock()
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	ph, done := a.m.Advance(a.g)
	a.phaseGen++
	a.phaseDone = false
	a.order = nil
	a.orderPos = 0
	a.awaitingOrder = false
	a.resetTalkLocked()
	a.cancelWaiterLocked()
	if done || ph == nil {
		a.finishLocked("所有阶段已完成")
		a.mu.Unlock()
		return
	}
	// The host's move belongs to the host's own line in the transcript, not to a
	// divider the reader has to interpret: everyone sees who advanced and where to.
	a.appendLocked(domain.Event{
		Type:    domain.EvDM,
		Speaker: "主持人",
		PhaseID: ph.ID,
		Text:    fmt.Sprintf("进入下一阶段：%s", ph.Name),
	})
	a.persistLocked()
	a.mu.Unlock()
	a.beginPhase(a.phaseCtx(ctx))
}

func (a *Actor) requestAdvance(ctx context.Context) {
	a.mu.Lock()
	gen := a.phaseGen
	a.floor = nil
	a.mu.Unlock()
	a.advance(ctx, gen)
}

// cancelWaiterLocked releases the pending waiter (if any). Callers hold mu:
// cancelling never blocks, so this is safe inside the critical section.
func (a *Actor) cancelWaiterLocked() {
	if a.waitCancel != nil {
		a.waitCancel()
		a.waitCancel = nil
	}
	// Waiters belong to the phase that is ending. Dropping the routes keeps a
	// command for the next phase from landing in a dead channel.
	a.waiters = map[string]chan ClientMessage{}
}

func (a *Actor) finishLocked(reason string) {
	if a.finished {
		return
	}
	a.finished = true
	a.cancelWaiterLocked()
	a.g.Status = domain.RoomFinished
	now := time.Now().UTC()
	a.g.EndedAt = &now
	a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "本局结束：" + reason})
	a.persistLocked()
	close(a.done)
}

// ---- turn order ----

// prepareOrder fixes this phase's turn order before anyone acts.
//
// round_robin and random resolve it on the spot (random is logged, so a replay
// still reads in the order that was actually played). host_assigned parks the
// phase until the host publishes a layout: the table would otherwise have no
// way to know who is first.
func (a *Actor) prepareOrder(ctx context.Context, gen int, ph *domain.Phase) bool {
	a.mu.Lock()
	a.order = nil
	a.orderPos = 0
	a.awaitingOrder = false
	seats := NewScheduler(a.g.Seats).Order()
	a.mu.Unlock()

	switch ph.Order {
	case domain.OrderHostAssigned:
		return a.awaitOrder(ctx, gen, ph)
	case domain.OrderRandom:
		shuffled := append([]domain.Seat(nil), seats...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		a.mu.Lock()
		a.order = shuffled
		a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: "本阶段顺序（随机）：" + a.orderNamesLocked(shuffled)})
		a.persistLocked()
		a.mu.Unlock()
		return true
	default:
		a.mu.Lock()
		a.order = seats
		a.mu.Unlock()
		return true
	}
}

// awaitOrder waits for the host to lay out the whole round. It returns false
// when the host skipped the phase instead of ordering it.
func (a *Actor) awaitOrder(ctx context.Context, gen int, ph *domain.Phase) bool {
	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return false
	}
	host := a.hostSeatLocked()
	if host == nil {
		// A room without a host seat has nobody to order the round: fall back to
		// seating order rather than stalling forever.
		a.order = NewScheduler(a.g.Seats).Order()
		a.mu.Unlock()
		return true
	}
	ch := make(chan ClientMessage, 1)
	a.waiters[host.ID] = ch
	a.awaitingOrder = true
	// The layout has no deadline of its own, so the phase needs a wake-up path:
	// cancelWaiterLocked fires this when the host skips the phase.
	wctx, stopWait := context.WithCancel(ctx)
	defer stopWait()
	a.waitCancel = stopWait
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     host.ID,
		Visibility: domain.VisDM,
		PhaseID:    ph.ID,
		Text:       "本阶段顺序由你指定：排好所有玩家的顺序后点「确定顺序」。",
	})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if cur, ok := a.waiters[host.ID]; ok && cur == ch {
			delete(a.waiters, host.ID)
		}
		a.awaitingOrder = false
		a.mu.Unlock()
	}()

	for {
		select {
		case <-wctx.Done():
			return false
		case msg := <-ch:
			switch msg.Type {
			case "host.skip":
				return false
			case "order.set":
				if err := a.applyOrder(ph, msg.SeatIDs); err != nil {
					a.notify(*host, err.Error())
					continue
				}
				return true
			default:
				if msg.Type == "speak.text" {
					if text := strings.TrimSpace(msg.Text); text != "" {
						a.appendSpeech(*host, text)
					}
					continue
				}
				a.sideCommand(*host, msg)
			}
		}
	}
}

// applyOrder stores the host's layout after checking it is a full permutation
// of the player seats: a short list would strand a seat with no turn at all.
func (a *Actor) applyOrder(ph *domain.Phase, ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m.Current(a.g) != ph {
		return ErrNotAllowed{Reason: "阶段已切换"}
	}
	players := a.g.PlayerSeats()
	byID := make(map[string]domain.Seat, len(players))
	for _, s := range players {
		byID[s.ID] = s
	}
	if len(ids) != len(players) {
		return ErrNotAllowed{Reason: fmt.Sprintf("请把 %d 个玩家全部排进顺序里", len(players))}
	}
	seen := map[string]bool{}
	order := make([]domain.Seat, 0, len(ids))
	for _, id := range ids {
		seat, ok := byID[id]
		if !ok {
			return ErrNotAllowed{Reason: "顺序里有不属于本局的座位"}
		}
		if seen[id] {
			return ErrNotAllowed{Reason: "同一个玩家不能排两次"}
		}
		seen[id] = true
		order = append(order, seat)
	}
	a.order = order
	a.orderPos = 0
	a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: "本阶段顺序（主持人指定）：" + a.orderNamesLocked(order)})
	a.persistLocked()
	return nil
}

// nextSeat hands out the next turn by walking the phase order and skipping the
// seats whose quota is spent, so a host-assigned order repeats until everyone
// is done. Callers hold mu.
func (a *Actor) nextSeat(counts map[string]int, limit int) *domain.Seat {
	if limit <= 0 {
		limit = 1
	}
	if len(a.order) == 0 {
		return nil
	}
	n := len(a.order)
	for i := 0; i < n; i++ {
		idx := (a.orderPos + i) % n
		if counts[a.order[idx].ID] < limit {
			seat := a.order[idx]
			a.orderPos = (idx + 1) % n
			return &seat
		}
	}
	return nil
}

// orderPromptLocked is the host-side panel: every player once, in any order.
func (a *Actor) orderPromptLocked() *TurnPrompt {
	p := &TurnPrompt{Kind: "order"}
	if ph := a.m.Current(a.g); ph != nil {
		p.PhaseID = ph.ID
	}
	p.Text = "按你想要的顺序点选玩家；不确定就选「随机」或「按入座」，再点「确定顺序」。"
	for _, s := range a.g.PlayerSeats() {
		p.Seats = append(p.Seats, TurnOption{SeatID: s.ID, Role: a.speakerLocked(s), Display: s.DisplayName, Kind: s.Kind})
	}
	return p
}

// orderNamesLocked renders "甲 → 乙 → 丙" for the log.
func (a *Actor) orderNamesLocked(order []domain.Seat) string {
	names := make([]string, 0, len(order))
	for _, s := range order {
		names = append(names, a.speakerLocked(s))
	}
	return strings.Join(names, " → ")
}

func (a *Actor) hostSeatLocked() *domain.Seat {
	for i := range a.g.Seats {
		if a.g.Seats[i].IsHost {
			return &a.g.Seats[i]
		}
	}
	return nil
}

// ---- search phase ----

func (a *Actor) runSearch(ctx context.Context, gen int) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if ph == nil || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	limit := ph.PerPlayerPick
	if limit <= 0 {
		limit = 1
	}
	a.mu.Unlock()

	// The order belongs to the phase, not to the seat: every turn goes through
	// nextSeat so a host layout or a shuffle applies to search exactly as it
	// does to discussion.
	if !a.prepareOrder(ctx, gen, ph) {
		return
	}

	for {
		if !a.alive(gen) || ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		next := a.nextSeat(a.searched, limit)
		a.mu.Unlock()
		if next == nil {
			break
		}
		if next.Kind == domain.SeatAgent {
			if !a.agentSearch(*next, ph) {
				// Nothing left this seat may take: retire it for the phase, or the
				// walk would keep handing it the same empty turn.
				a.mu.Lock()
				a.searched[next.ID] = limit
				a.mu.Unlock()
			}
		} else {
			a.awaitSearch(ctx, gen, *next, ph)
		}
	}
	a.completePhase(ctx, gen)
}

func (a *Actor) alive(gen int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.finished && gen == a.phaseGen
}

// phaseCtx is the context the next phase's loops belong to. Advancing is always
// triggered from a client command, and a websocket request context is gone the
// moment the frame is handled; inheriting it would leave the new phase with a
// dead ctx and its turn walk spinning on cancelled waits.
func (a *Actor) phaseCtx(fallback context.Context) context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.base != nil {
		return a.base
	}
	return fallback
}

func (a *Actor) awaitSearch(ctx context.Context, gen int, seat domain.Seat, ph *domain.Phase) bool {
	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return false
	}
	ch := make(chan ClientMessage, 1)
	a.waiters[seat.ID] = ch
	left := ph.PerPlayerPick - a.searched[seat.ID]
	prompt := fmt.Sprintf("轮到你搜证，可搜 %d 次：%s", left, strings.Join(a.searchableTitlesLocked(seat, ph), "、"))
	if ph.SearchMode == domain.SearchRandom {
		// Random mode hides the list on purpose: knowing the titles would turn
		// "draw" back into "choose".
		prompt = fmt.Sprintf("轮到你搜证，还剩 %d 次随机抽取", left)
	}
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     seat.ID,
		Visibility: SeatVisibility(seat.ID),
		PhaseID:    ph.ID,
		Text:       prompt,
	})
	// The floor also covers search turns: the table needs to see whose pick it
	// is, and the client gates "随机抽取" on it.
	a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, ExpiresAt: time.Now().Add(a.cfg.floor())}
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		// Only drop our own route: the phase may already have registered the next
		// waiter for this seat.
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
		case <-ctx.Done():
			return false
		case <-expired:
			a.mu.Lock()
			if gen == a.phaseGen {
				// A skipped turn spends the quota: otherwise the walk would hand the
				// same silent seat the floor forever.
				a.searched[seat.ID]++
				a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, Visibility: SeatVisibility(seat.ID), Text: "搜证超时，自动跳过"})
				a.persistLocked()
			}
			a.mu.Unlock()
			return false
		case msg := <-ch:
			if msg.Type == "host.skip" {
				return false
			}
			// Chat is always open: a line typed while the seat owes a search
			// action is speech, not a command to swallow.
			if msg.Type == "speak.text" {
				if text := strings.TrimSpace(msg.Text); text != "" {
					a.appendSpeech(seat, text)
				}
				continue
			}
			switch msg.Type {
			case "search.clue":
				if err := a.applySearch(seat, msg.ClueID, ph); err != nil {
					a.notify(seat, err.Error())
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				if left := a.keepTurn(seat, ph, "搜证"); left > 0 {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				return true
			case "search.draw":
				// Random mode: the seat does not name a clue, it just draws one.
				if err := a.drawSearch(seat, ph); err != nil {
					a.notify(seat, err.Error())
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				if left := a.keepTurn(seat, ph, "随机抽取"); left > 0 {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				return true
			default:
				a.sideCommand(seat, msg)
				continue
			}
		}
	}
}

// keepTurn keeps the floor with a seat that still owes actions this phase, so
// "每人搜 2 次" is two clicks in a row rather than two passes around the table.
// It returns the remaining picks; zero means the order may move on.
func (a *Actor) keepTurn(seat domain.Seat, ph *domain.Phase, what string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m.Current(a.g) != ph {
		return 0
	}
	left := remainingPicks(a.searched[seat.ID], ph.PerPlayerPick)
	if left > 0 {
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			SeatID:     seat.ID,
			Visibility: SeatVisibility(seat.ID),
			PhaseID:    ph.ID,
			Text:       fmt.Sprintf("还剩 %d 次%s", left, what),
		})
		// The next action gets a fresh clock, exactly like the first one.
		a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, ExpiresAt: time.Now().Add(a.cfg.floor())}
		a.persistLocked()
	}
	return left
}

// availableCluesLocked lists the clues one seat may still take, mirroring the
// filter of claimClue so the UI only ever offers accepted choices. Callers hold
// mu.
func (a *Actor) availableCluesLocked(seat domain.Seat, ph *domain.Phase) []*domain.Clue {
	var out []*domain.Clue
	for _, cid := range ph.CluePool {
		c := a.m.TokenClue(cid)
		if c == nil {
			continue
		}
		st := a.g.ClueState[cid]
		if st.FoundBySeat != "" || st.Revealed {
			continue
		}
		if c.OwnerRoleID != "" && c.OwnerRoleID != seat.RoleID {
			continue
		}
		out = append(out, c)
	}
	return out
}

// searchOptionsLocked renders the pick list for one seat: only the hint on the
// back of the card, never the title or the content.
func (a *Actor) searchOptionsLocked(seat domain.Seat, ph *domain.Phase) []SearchOption {
	var out []SearchOption
	for i, c := range a.availableCluesLocked(seat, ph) {
		out = append(out, SearchOption{ID: c.ID, Hint: clueHint(c, i+1)})
	}
	return out
}

// clueHint is what the back of the physical card says. The back names a place
// ("背后的培养仓") without describing the finding; a script that has no back
// text still gets a usable label from the room plus an ordinal, so the list is
// never empty and never leaks what is on the card.
func clueHint(c *domain.Clue, n int) string {
	if s := strings.TrimSpace(c.Back); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.Location); s != "" {
		return fmt.Sprintf("%s·线索%d", s, n)
	}
	return fmt.Sprintf("线索%d", n)
}

func (a *Actor) searchableTitlesLocked(seat domain.Seat, ph *domain.Phase) []string {
	var out []string
	for _, o := range a.searchOptionsLocked(seat, ph) {
		out = append(out, o.Hint)
	}
	if len(out) == 0 {
		out = append(out, "（本轮线索已被搜完）")
	}
	return out
}

func (a *Actor) applySearch(seat domain.Seat, clueID string, ph *domain.Phase) error {
	if ph.SearchMode == domain.SearchRandom {
		return ErrNotAllowed{Reason: "本阶段是随机抽取，请点「随机抽取」"}
	}
	return a.claimClue(seat, clueID, ph)
}

// drawSearch deals one clue uniformly among the ones this seat may still take.
// Random mode exists so the table cannot steer which evidence surfaces.
func (a *Actor) drawSearch(seat domain.Seat, ph *domain.Phase) error {
	a.mu.Lock()
	if a.m.Current(a.g) != ph {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "阶段已切换"}
	}
	options := a.availableCluesLocked(seat, ph)
	if len(options) == 0 {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "本轮线索已被抽完"}
	}
	pick := options[rand.Intn(len(options))]
	a.mu.Unlock()
	return a.claimClue(seat, pick.ID, ph)
}

// claimClue records one clue as found by this seat. Every search path (pick,
// draw, agent) ends here so the guards can never drift apart.
func (a *Actor) claimClue(seat domain.Seat, clueID string, ph *domain.Phase) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m.Current(a.g) != ph {
		return ErrNotAllowed{Reason: "阶段已切换"}
	}
	inPool := false
	for _, id := range ph.CluePool {
		if id == clueID {
			inPool = true
			break
		}
	}
	if !inPool {
		return ErrNotAllowed{Reason: "本轮无法搜到该线索"}
	}
	c := a.m.TokenClue(clueID)
	if c == nil {
		return ErrNotAllowed{Reason: "线索不存在"}
	}
	st := a.g.ClueState[clueID]
	if st.FoundBySeat != "" || st.Revealed {
		return ErrNotAllowed{Reason: "该线索已被搜走"}
	}
	if c.OwnerRoleID != "" && c.OwnerRoleID != seat.RoleID {
		return ErrNotAllowed{Reason: "该线索属于其他人的私人物品"}
	}
	if a.searched[seat.ID] >= ph.PerPlayerPick {
		return ErrNotAllowed{Reason: "本轮搜证次数已用完"}
	}
	a.searched[seat.ID]++
	st.ClueID = clueID
	st.FoundBySeat = seat.ID
	if c.OwnerRoleID != "" {
		st.PrivateOwner = c.OwnerRoleID
	}
	st.FoundAt = time.Now().UTC()
	a.g.ClueState[clueID] = st
	a.appendLocked(domain.Event{
		Type:       domain.EvClueFound,
		PhaseID:    ph.ID,
		SeatID:     seat.ID,
		RoleID:     seat.RoleID,
		ClueID:     clueID,
		Visibility: SeatVisibility(seat.ID),
		Text:       fmt.Sprintf("你搜到线索【%s】：%s", c.Title, c.Content),
		Meta:       map[string]string{"title": c.Title},
	})
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		PhaseID: ph.ID,
		SeatID:  seat.ID,
		Text:    fmt.Sprintf("%s 搜到一条线索（内容未公开）", a.speakerLocked(seat)),
	})
	a.persistLocked()
	return nil
}

func (a *Actor) agentSearch(seat domain.Seat, ph *domain.Phase) bool {
	a.mu.Lock()
	candidates := a.availableCluesLocked(seat, ph)
	if len(candidates) == 0 {
		a.mu.Unlock()
		return false
	}
	p := a.personas[seat.ID]
	var pick *domain.Clue
	if ph.SearchMode == domain.SearchRandom {
		// Random mode is random for the agents too: otherwise a sly seat would
		// still be hand-picking evidence out of the same pool.
		pick = candidates[rand.Intn(len(candidates))]
	} else {
		// A sly agent prefers clues that let it steer suspicion; a candid one
		// prefers neutral clues. Index choice is deterministic per seat+phase.
		pick = candidates[int(p.Deceitfulness*float64(len(candidates)))%len(candidates)]
	}
	a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, Kind: SlotTurn}
	a.persistLocked()
	a.mu.Unlock()

	a.thinking(seat, "start")
	defer a.thinking(seat, "stop")
	// A small pause keeps the marker visible: the pick itself is instant.
	time.Sleep(400 * time.Millisecond)

	if err := a.claimClue(seat, pick.ID, ph); err != nil {
		return false
	}
	a.mu.Lock()
	if a.floor != nil && a.floor.SeatID == seat.ID {
		a.floor = nil
	}
	a.mu.Unlock()

	// The find stays private. Evidence is normally opened up by talking about it
	// during the discussion, so the table only learns what a seat chooses to say;
	// an explicit reveal is the seat's own call, never a side effect of searching.
	return true
}

func (a *Actor) revealClue(seat domain.Seat, clueID string) error {
	a.mu.Lock()
	c := a.m.TokenClue(clueID)
	if c == nil {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "线索不存在"}
	}
	st := a.g.ClueState[clueID]
	if st.Revealed {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "该线索已经公开"}
	}
	owns := st.FoundBySeat == seat.ID || (c.OwnerRoleID != "" && c.OwnerRoleID == seat.RoleID)
	if !owns {
		a.mu.Unlock()
		return ErrNotAllowed{Reason: "你只能公开自己掌握的线索"}
	}
	st.Revealed = true
	st.RevealedBy = seat.ID
	st.RevealedAt = time.Now().UTC()
	a.g.ClueState[clueID] = st
	ph := a.m.Current(a.g)
	phaseID := ""
	if ph != nil {
		phaseID = ph.ID
	}
	a.appendLocked(domain.Event{
		Type:    domain.EvClueRevealed,
		PhaseID: phaseID,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		ClueID:  clueID,
		Text:    fmt.Sprintf("%s 公开了线索【%s】：%s", a.speakerLocked(seat), c.Title, c.Content),
		Meta:    map[string]string{"title": c.Title, "content": c.Content},
	})
	a.persistLocked()
	a.mu.Unlock()
	return nil
}

// ---- discussion phase ----

func (a *Actor) runDiscussion(ctx context.Context, gen int) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if ph == nil || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	limit := ph.SpeechPerPlayer
	if limit <= 0 {
		limit = 1
	}
	a.mu.Unlock()

	if !a.prepareOrder(ctx, gen, ph) {
		return
	}

	for {
		if !a.alive(gen) || ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		next := a.nextSeat(a.speech, limit)
		a.mu.Unlock()
		if next == nil {
			break
		}
		if next.Kind == domain.SeatAgent {
			a.agentSpeak(ctx, gen, *next, ph)
		} else {
			a.awaitSpeech(ctx, gen, *next, ph)
		}
	}
	// The scripted round is only the opening statements. A real table keeps
	// arguing until the host calls it, so the floor stays open instead of the
	// phase closing itself. Unattended demos keep the old auto-advance.
	if a.cfg.AutoAdvance {
		a.completePhase(ctx, gen)
		return
	}
	a.freeTalk(ctx, gen, ph)
}

func (a *Actor) awaitSpeech(ctx context.Context, gen int, seat domain.Seat, ph *domain.Phase) bool {
	wctx, stopWait := context.WithCancel(ctx)
	defer stopWait()

	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return false
	}
	ch := make(chan ClientMessage, 1)
	a.waiters[seat.ID] = ch
	a.waitCancel = stopWait
	seconds := int(a.cfg.floor().Seconds())
	limit := fmt.Sprintf("限时 %d 秒", seconds)
	if a.cfg.Unlimited {
		limit = "不限时"
	}
	f := Floor{SeatID: seat.ID, PhaseID: ph.ID}
	if !a.cfg.Unlimited {
		f.ExpiresAt = time.Now().Add(a.cfg.floor())
	}
	a.floor = &f
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     seat.ID,
		Visibility: SeatVisibility(seat.ID),
		PhaseID:    ph.ID,
		Text:       fmt.Sprintf("轮到你发言（%s）", limit),
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
	// expired stays nil when the room runs unlimited or asked to wait for the
	// host: the select then parks on the message channel until the phase is
	// advanced, and cancelWaiterLocked releases it if the host moves on.
	for {
		select {
		case <-wctx.Done():
			return false
		case <-expired:
			if a.cfg.WaitForHost {
				a.mu.Lock()
				if gen == a.phaseGen && !a.finished {
					a.appendLocked(domain.Event{
						Type:    domain.EvSystem,
						PhaseID: ph.ID,
						SeatID:  seat.ID,
						Text:    fmt.Sprintf("%s 超过 %d 秒未发言，等待主持人推进", a.speakerLocked(seat), seconds),
					})
					// The seat still owns the floor, but nobody is on a clock anymore.
					a.floor = nil
					a.persistLocked()
				}
				a.mu.Unlock()
				expired = nil
				continue
			}
			a.mu.Lock()
			if gen == a.phaseGen {
				a.speech[seat.ID]++
				a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, SeatID: seat.ID, Text: fmt.Sprintf("%s 未在时限内发言，跳过", a.speakerLocked(seat))})
				a.persistLocked()
			}
			a.mu.Unlock()
			return false
		case msg := <-ch:
			switch msg.Type {
			case "host.skip":
				return false
			case "speak.text":
				text := strings.TrimSpace(msg.Text)
				if text == "" {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				a.mu.Lock()
				a.speech[seat.ID]++
				a.mu.Unlock()
				a.appendSpeech(seat, text)
				return true
			default:
				a.sideCommand(seat, msg)
			}
		}
	}
}

// agentSpeak is the scripted round's turn. It is one step only: every seat gets
// exactly one opening statement before the floor opens to requests, so a long
// chain here would just delay the table.
func (a *Actor) agentSpeak(ctx context.Context, gen int, seat domain.Seat, ph *domain.Phase) {
	a.agentTurns(ctx, gen, seat, ph, SpeechSlot{SeatID: seat.ID, Kind: SlotTurn}, 1)
}

func (a *Actor) pressureLocked(seat domain.Seat, ph *domain.Phase) persona.RevealPressure {
	pr := persona.RevealPressure{RoundNormalised: float64(maxInt(ph.Round, 1)-1) / 3}
	for _, ev := range a.g.Events {
		if ev.Type != domain.EvSpeech || ev.SeatID == seat.ID {
			continue
		}
		if strings.Contains(ev.Text, a.speakerLocked(seat)) {
			pr.Asked = true
		}
	}
	return pr
}

// generate runs one LLM turn with a leak guard and one repair attempt.
func (a *Actor) generate(ctx context.Context, gen int, seat domain.Seat, req llm.Request) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, a.cfg.agent())
	defer cancel()

	guard := NewLeakGuard(a.sc, &seat)
	// JSON turns (votes, structured answers) are machine-readable: streaming
	// them would dump raw JSON into the transcript.
	var sink func(string) error
	if !req.JSON {
		sink = func(delta string) error {
			a.publishDelta(gen, seat, delta)
			return nil
		}
	}
	text, provider, err := a.reg.Complete(cctx, req, sink)
	if err != nil {
		return "", provider, err
	}
	text = stripSelfPrefix(strings.TrimSpace(text), a.speakerLocked(seat))
	// A tool call carries the spoken words inside a field. Checking the whole
	// envelope would flag `{"tool":"speak"}` itself as a leak.
	check := text
	if req.JSON {
		check = parseAction(text).Text
	}
	if hit, leaked := guard.Check(check); leaked {
		a.logLeak(seat, hit, text)
		req.Messages = []llm.Message{{Role: "user", Text: req.Messages[0].Text +
			"\n\n【纠正】你刚才的回答引用了你不该知道的细节。请只使用【可见信息】重新说一次，不要涉及任何你不知道的内容，也不要提到“秘密”“可见信息”等词。"}}
		retry, provider2, err2 := a.reg.Complete(cctx, req, nil)
		if err2 == nil {
			retry = strings.TrimSpace(retry)
			if _, stillLeak := guard.Check(retry); !stillLeak {
				return retry, provider2, nil
			}
		}
		return guard.Sanitize(text, hit), provider, nil
	}
	return text, provider, nil
}

func (a *Actor) logLeak(seat domain.Seat, hit, text string) {
	a.mu.Lock()
	a.appendLocked(domain.Event{
		Type:       domain.EvLeak,
		SeatID:     seat.ID,
		RoleID:     seat.RoleID,
		Visibility: domain.VisDM,
		Text:       fmt.Sprintf("%s 的回答疑似泄露：%s", a.speakerLocked(seat), hit),
		Meta:       map[string]string{"hit": hit, "text": text},
	})
	a.persistLocked()
	a.mu.Unlock()
}

// ---- vote phase ----

func (a *Actor) runVote(ctx context.Context, gen int) {
	a.mu.Lock()
	ph := a.m.Current(a.g)
	if ph == nil || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	order := NewScheduler(a.g.Seats).Order()
	a.mu.Unlock()

	for _, seat := range order {
		if !a.alive(gen) {
			return
		}
		if seat.Kind == domain.SeatAgent {
			a.agentVote(ctx, gen, seat)
		} else {
			a.awaitVote(ctx, gen, seat)
		}
	}
	a.tallyVotes()
	a.completePhase(ctx, gen)
}

func (a *Actor) awaitVote(ctx context.Context, gen int, seat domain.Seat) bool {
	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return false
	}
	ch := make(chan ClientMessage, 1)
	a.waiters[seat.ID] = ch
	a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, Visibility: SeatVisibility(seat.ID), Text: "投票阶段：请选择你认为是凶手的角色"})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		// Only drop our own route: the phase may already have registered the next
		// waiter for this seat.
		if cur, ok := a.waiters[seat.ID]; ok && cur == ch {
			delete(a.waiters, seat.ID)
		}
		a.mu.Unlock()
	}()

	timer, expired := a.cfg.deadline()
	if timer != nil {
		defer timer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case <-expired:
			a.recordVote(seat, "")
			return false
		case msg := <-ch:
			if msg.Type == "speak.text" {
				if text := strings.TrimSpace(msg.Text); text != "" {
					a.appendSpeech(seat, text)
				}
				continue
			}
			if msg.Type != "vote.cast" {
				a.sideCommand(seat, msg)
				continue
			}
			a.recordVote(seat, msg.RoleID)
			return true
		}
	}
}

func (a *Actor) agentVote(ctx context.Context, gen int, seat domain.Seat) {
	a.mu.Lock()
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	v := VisibleFor(a.g, a.sc, &seat, VisibleOptions{MaxTranscript: 40})
	p := a.personas[seat.ID]
	a.mu.Unlock()

	a.thinking(seat, "start")
	defer a.thinking(seat, "stop")

	instruction := "投票阶段。请只输出一个 JSON 对象：{\"vote\":\"<角色名>\",\"reason\":\"<一句话理由>\"}。vote 必须是你怀疑的角色名，不能是自己。"
	req := a.pb.BuildVote(PromptInput{Visible: v, Persona: p, Instruction: instruction})
	text, _, err := a.generate(ctx, gen, seat, req)
	if err != nil {
		a.recordVote(seat, "")
		return
	}
	roleName := extractVoteRole(text)
	a.recordVoteByName(seat, roleName, v)
}

func extractVoteRole(text string) string {
	var payload struct {
		Vote string `json:"vote"`
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		_ = json.Unmarshal([]byte(text[start:end+1]), &payload)
	}
	if payload.Vote != "" {
		return payload.Vote
	}
	// Fallback: first quoted or bracketed token.
	return strings.TrimSpace(text)
}

func (a *Actor) recordVoteByName(seat domain.Seat, roleName string, v Visible) {
	roleID := ""
	a.mu.Lock()
	for _, ps := range v.Seats {
		if ps.RoleName != "" && strings.Contains(roleName, ps.RoleName) && ps.ID != seat.ID {
			roleID = ps.RoleID
			break
		}
	}
	if roleID == "" {
		for _, r := range a.sc.Roles {
			if r.ID != seat.RoleID && strings.Contains(roleName, r.Name) {
				roleID = r.ID
				break
			}
		}
	}
	a.mu.Unlock()
	a.recordVote(seat, roleID)
}

func (a *Actor) recordVote(seat domain.Seat, roleID string) {
	a.mu.Lock()
	a.g.Votes[seat.ID] = roleID
	text := "已投票"
	if roleID != "" {
		name := roleID
		if r := a.sc.Tokenize().RoleByID(roleID); r != nil {
			name = r.Name
		}
		text = fmt.Sprintf("%s 投给了 %s", a.speakerLocked(seat), name)
	} else {
		text = fmt.Sprintf("%s 弃权", a.speakerLocked(seat))
	}
	a.appendLocked(domain.Event{Type: domain.EvVote, SeatID: seat.ID, RoleID: roleID, Text: text})
	a.persistLocked()
	a.mu.Unlock()
}

func (a *Actor) tallyVotes() {
	a.mu.Lock()
	counts := tally(a.g.Votes)
	type kv struct {
		role string
		n    int
	}
	var list []kv
	for role, n := range counts {
		list = append(list, kv{role, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].role < list[j].role
	})
	parts := make([]string, 0, len(list))
	idx := a.sc.Tokenize()
	for _, it := range list {
		name := it.role
		if it.role == "" {
			name = "弃权"
		} else if r := idx.RoleByID(it.role); r != nil {
			name = r.Name
		}
		parts = append(parts, fmt.Sprintf("%s %d 票", name, it.n))
	}
	if len(parts) == 0 {
		parts = append(parts, "无人投票")
	}
	a.appendLocked(domain.Event{Type: domain.EvVoteResult, Text: "投票结束：" + strings.Join(parts, "，")})
	a.persistLocked()
	a.mu.Unlock()
}

// ---- reveal ----

func (a *Actor) runReveal() {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := a.sc.Tokenize()
	killer := a.sc.Truth.KillerRoleID
	killerName := killer
	if r := idx.RoleByID(killer); r != nil {
		killerName = r.Name
	}
	summary := a.sc.Truth.Summary
	if summary == "" {
		summary = a.sc.Truth.Method
	}
	text := fmt.Sprintf("真相揭晓：凶手是 %s。%s", killerName, summary)
	a.appendLocked(domain.Event{
		Type: domain.EvReveal,
		Text: text,
		Meta: map[string]string{"killerRoleId": killer},
	})
	a.persistLocked()
}

// ---- helpers ----

func (a *Actor) appendSpeech(seat domain.Seat, text string) {
	a.appendSpeechTo(seat, text, "")
}

// appendSpeechTo records a line and hooks it into the discussion floor: the
// answered seat is moved to the front of the queue (it should answer first, or
// decline), and the other agents decide whether they want to jump in.
func (a *Actor) appendSpeechTo(seat domain.Seat, text, replyTo string) {
	a.mu.Lock()
	ev := domain.Event{
		Type:    domain.EvSpeech,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Text:    text,
	}
	if replyTo != "" && replyTo != seat.ID {
		ev.Meta = map[string]string{"replyTo": replyTo}
		if s := a.g.SeatByID(replyTo); s != nil {
			ev.Meta["replyToName"] = a.speakerLocked(*s)
		}
	}
	a.appendLocked(ev)
	if replyTo != "" && replyTo != seat.ID {
		a.enqueueTalkLocked(SpeechSlot{SeatID: replyTo, Kind: SlotReply, ReplyTo: seat.ID}, true)
	}
	a.queueWantsLocked(seat.ID, text)
	a.persistLocked()
	a.mu.Unlock()
	a.wakeTalk()
}

func (a *Actor) speakerLocked(seat domain.Seat) string {
	if r := a.sc.Tokenize().RoleByID(seat.RoleID); r != nil {
		return r.Name
	}
	return seat.DisplayName
}

func (a *Actor) notify(seat domain.Seat, text string) {
	a.mu.Lock()
	a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, Visibility: SeatVisibility(seat.ID), Text: text})
	a.persistLocked()
	a.mu.Unlock()
}

func (a *Actor) appendLocked(ev domain.Event) domain.Event {
	ev = a.g.AppendEvent(ev)
	if a.hub != nil {
		a.hub.Publish(ev)
	}
	return ev
}

func (a *Actor) publishDelta(gen int, seat domain.Seat, delta string) {
	if delta == "" {
		return
	}
	a.mu.Lock()
	if a.finished || gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	ev := domain.Event{
		Type:    domain.EvAgentDelta,
		GameID:  a.g.ID,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Text:    delta,
	}
	if a.hub != nil {
		// Deltas are transient: they are published but never stored.
		a.hub.Publish(ev)
	}
	a.mu.Unlock()
}

func (a *Actor) persistLocked() {
	if a.sink == nil {
		return
	}
	a.sink.PutGame(a.snapshotLocked())
}

// snapshotLocked deep-copies the game so the store never aliases live state.
func (a *Actor) snapshotLocked() *domain.Game {
	b, err := json.Marshal(a.g)
	if err != nil {
		return a.g
	}
	var g domain.Game
	if err := json.Unmarshal(b, &g); err != nil {
		return a.g
	}
	return &g
}

// Snapshot returns a consistent copy of the game (host/debug use).
func (a *Actor) Snapshot() domain.Game {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.snapshotLocked()
}

// Close ends the game from outside the phase machine (an admin closing the
// room). It is idempotent: closing a finished game keeps the original reason.
func (a *Actor) Close(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "对局已被关闭"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finishLocked(reason)
}

func tally(votes map[string]string) map[string]int {
	out := map[string]int{}
	for _, role := range votes {
		out[role]++
	}
	return out
}
