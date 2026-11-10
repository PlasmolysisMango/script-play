package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
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
	// To is the seat a whisper is addressed to.
	To string `json:"to,omitempty"`
	// CardID names the card a card-game command plays, gives away or picks.
	CardID string `json:"cardId,omitempty"`
	// CardIDs carries the two discarded cards of 陷入僵局.
	CardIDs []string `json:"cardIds,omitempty"`
	// Color is the colour 线索中断 names for the next seat.
	Color string `json:"color,omitempty"`
	// Guesses maps seatID -> roleID: the "在座玩家分别是谁" lines of the
	// printed score card.
	Guesses map[string]string `json:"guesses,omitempty"`
	// TargetID names the seat a skill points at.
	TargetID string `json:"targetId,omitempty"`
	// PhaseID pins a host command to the phase it was issued in, so a click that
	// arrives after the table moved on cannot spend the next round's budget.
	PhaseID string `json:"phaseId,omitempty"`
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
	// Presence tells whether a seat still holds a live socket. A nil value (tests,
	// replay, headless runs) means "assume everybody is here".
	Presence func(seatID string) bool
	// DiscussIdle ends an open discussion nobody has spoken in for this long.
	// Zero disables the timer, which is what the old behaviour amounted to: the
	// phase sat there until the host clicked, however long that took.
	DiscussIdle time.Duration
	// ReadingMin is the least time a reading/opening scene stays on screen,
	// so a host mashing "next" cannot skip the role books entirely.
	ReadingMin time.Duration
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

// presenceGrace is how long a disconnected seat keeps its turn before the
// engine plays its hand for it. Short enough to rescue a stuck table, long
// enough to survive a page reload.
const presenceGrace = 20 * time.Second

// goneWatcher returns a channel that fires once the seat has been without a
// socket for presenceGrace. Without it a browser that closed on its turn kept
// the whole table waiting on a prompt nobody would ever answer.
func (a *Actor) goneWatcher(seatID string) (<-chan struct{}, func()) {
	if a.cfg.Presence == nil {
		return nil, func() {}
	}
	out := make(chan struct{}, 1)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		var gone time.Duration
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if a.cfg.Presence(seatID) {
					gone = 0
					continue
				}
				gone += 5 * time.Second
				if gone >= presenceGrace {
					select {
					case out <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()
	return out, func() { close(stop) }
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
	// voteWake releases the vote waiter when any ballot lands, so a seat that
	// votes while the walk is prompting somebody else does not wait out its
	// timer before the walk notices.
	voteWake chan struct{}

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
	// whisperBusy keeps one private answer per seat in flight: a whisper to an
	// AI costs a model call, and a human who sends two lines in a row should not
	// fork two of them.
	whisperBusy map[string]bool
	// order is this phase's turn order and orderPos is where the walk stands.
	// host_assigned leaves both empty until the host lays the round out.
	order    []domain.Seat
	orderPos int
	// searchQuota is this round's per-seat draw quota when the script hands them
	// out by rank (K12 PickByRank). Empty means the flat phase limit applies.
	searchQuota map[string]int
	// searchExtra is the acting bonus the host has granted this phase, per seat.
	// It adds to the printed quota without touching the script.
	searchExtra map[string]int
	// hostExtraLeft is how much of Phase.HostExtraPicks the host may still grant.
	hostExtraLeft int
	// searchOpen stays true only while the current search loop is still handing
	// out turns. Once the walk closes, a late grant must not buy a pick nobody
	// will ever be prompted for.
	searchOpen bool
	// pointsOpen flips once a SettleFavor phase has published the balances; until
	// then Meta.HidePoints keeps other seats' numbers off the table.
	pointsOpen bool
	// awaitingOrder is true while the table waits for that layout, so the host
	// view can show the re-ordering panel instead of an idle table.
	awaitingOrder bool
	// phaseDone marks a phase whose actions are finished but which the host has
	// not released yet: no waiter is running, the table is just waiting.
	phaseDone bool
	// voteClosed seals the ballot before the tally so a late frame cannot alter
	// a result that was already printed.
	voteClosed bool
	phaseGen   int
	started    bool
	finished   bool
	// phaseAt is when the current phase opened; reading/opening use it to
	// enforce their minimum stay.
	phaseAt time.Time

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
		voteWake: make(chan struct{}, 1),
		speech:   map[string]int{},
		searched: map[string]int{},
		// buffered so queueing never blocks the command that queued.
		talkWake:    make(chan struct{}, 1),
		talkBudget:  map[string]int{},
		talkAt:      map[string]time.Time{},
		seatSeen:    map[string]int{},
		whisperBusy: map[string]bool{},
		done:        make(chan struct{}),
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
	if a.hasPointPoolLocked() && len(a.g.Points) == 0 {
		// Every player seat starts with the printed pile of bones. Spectators never
		// hold any: they are not in the round.
		a.g.Points = map[string]int{}
		for _, s := range a.g.Seats {
			if s.Kind != domain.SeatSpectator {
				a.g.Points[s.ID] = a.startPointsLocked(s)
			}
		}
	}
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
	// Granted marks a 「可获得曲径通幽 N」 card: it is not in the round's deck,
	// it waits for the seat that found the card naming it (clue.grantsClue).
	Granted bool `json:"granted,omitempty"`
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
	// Voted tells this seat it has already cast a ballot, including an abstention,
	// so the client can stop offering the buttons and say who the table waits on.
	Voted bool `json:"voted"`
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
	// Hand is this seat's cards in a card-game script (empty elsewhere).
	Hand []CardView `json:"hand,omitempty"`
	// Table is the public half of the card table plus this seat's legal moves.
	Table *TableView `json:"table,omitempty"`
	// ScoreDone tells the score phase this seat already filled its sheet.
	ScoreDone bool `json:"scoreDone,omitempty"`
	// Points is this seat's remaining action currency (K6: 仙骨) and PointsLabel
	// names it; both stay empty in scripts without one.
	Points      int    `json:"points,omitempty"`
	PointsLabel string `json:"pointsLabel,omitempty"`
	// TablePoints is every player seat's balance. The physical game keeps the
	// bones face up, so spending is public information.
	TablePoints map[string]int `json:"tablePoints,omitempty"`
	// Skill is this seat's printed special ability and whether it may fire now.
	Skill *SkillView `json:"skill,omitempty"`
}

// SkillView is the seat's own read of its one-shot ability.
type SkillView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Cost      int    `json:"cost,omitempty"`
	AllCost   bool   `json:"allCost,omitempty"`
	ExactCost int    `json:"exactCost,omitempty"`
	Target    string `json:"target,omitempty"`
	Used      bool   `json:"used"`
	// Ready is true only when this seat could fire it right now; Why says what is
	// missing when it cannot.
	Ready bool   `json:"ready"`
	Why   string `json:"why,omitempty"`
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
	if seat != nil && seat.IsHost {
		// Fill the host-only budget before the value is copied into the seat
		// state; setting it on the local afterwards would never reach the wire.
		v.HostExtraLeft = a.hostExtraLeft
	}
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
	if seat != nil {
		_, st.Voted = a.g.Votes[seat.ID]
	}
	if seat != nil && seat.Kind != domain.SeatSpectator {
		if ph := a.m.Current(a.g); ph != nil && ph.Type == domain.PhaseSearch {
			allowed := len(ph.OnlyRoleIDs) == 0 || slices.Contains(ph.OnlyRoleIDs, seat.RoleID)
			if allowed {
				// 深入调查的那一张不占本轮次数：哪怕 2/4/6 张已经抽完，只要底卡在手、
				// 物品齐备，它就得一直出现在面板上，否则玩家抽到底卡后再也找不到入口。
				st.Search = append(st.Search, a.grantedOptionsLocked(*seat, ph)...)
				left := a.picksLeftLocked(*seat, ph)
				switch {
				case left <= 0:
					// 次数用完：不再给本轮抽取，只剩上面那几张点名的卡。
				case ph.SearchMode == domain.SearchRandom:
					st.Draw = left
				default:
					st.Search = append(a.searchOptionsLocked(*seat, ph), st.Search...)
				}
			}
		}
	}
	if a.hasPointPoolLocked() {
		st.PointsLabel = a.pointsLabel()
		st.TablePoints = map[string]int{}
		open := !a.sc.Meta.HidePoints || a.pointsOpen
		for _, s := range a.g.Seats {
			if s.Kind == domain.SeatSpectator {
				continue
			}
			// 未公布时只给本人（主持人另走 includeTruth 的全量视图）。
			if !open && (seat == nil || s.ID != seat.ID) {
				continue
			}
			st.TablePoints[s.ID] = a.g.Points[s.ID]
		}
		if seat != nil && seat.Kind != domain.SeatSpectator {
			st.Points = a.g.Points[seat.ID]
			st.Skill = a.skillViewLocked(*seat)
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
	if a.g.Cards != nil && len(a.g.Cards.Order) > 0 {
		st.Table = a.tableViewLocked(seat)
		if seat != nil && seat.Kind != domain.SeatSpectator {
			st.Hand = a.handViewsLocked(seat.ID)
			_, st.ScoreDone = a.g.Cards.Answers[seat.ID]
		}
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

	// Spectators watch; they never take the floor. Voting and whispering already
	// say so, and the host-only creator sits in a spectator seat: their commands
	// are the host's (skip/end/grant), never a player's line.
	if seat.Kind == domain.SeatSpectator && (msg.Type == "speak.text" || msg.Type == "speak.request") {
		return ErrNotAllowed{Reason: "观战者不能发言"}
	}

	switch msg.Type {
	case "ping":
		return nil
	case "host.skip":
		if !seat.IsHost {
			return ErrNotAllowed{Reason: "只有主持人可以跳过"}
		}
		return a.requestAdvance(ctx)
	case "host.end":
		// The host's way out of a table that cannot reach its own ending — a
		// play/score script with no reveal phase used to stay running (and hold
		// its concurrency slot) forever.
		if !seat.IsHost {
			return ErrNotAllowed{Reason: "只有主持人可以结束对局"}
		}
		a.mu.Lock()
		a.finishLocked("主持人结束了本局")
		a.mu.Unlock()
		return nil
	case "search.grant":
		// The printed acting bonus: the host may hand one extra draw to a seat.
		// It is resolved here because the host never holds a search waiter.
		if !seat.IsHost {
			return ErrNotAllowed{Reason: "只有主持人可以发放额外搜证"}
		}
		return a.grantSearchPick(*seat, msg.TargetID, msg.PhaseID)
	case "speak.request":
		// Ask for the floor in an open discussion instead of typing first.
		return a.requestFloor(*seat)
	case "whisper.send":
		// Handled before the waiter check: a private line is legal while this
		// seat owes a prompt (it may even hold the floor), and it must never be
		// swallowed by a phase's wait channel.
		return a.sendWhisper(*seat, msg.To, msg.Text)
	case "skill.use":
		// A skill may fire at any point of a search round, including while this seat
		// holds the floor, so it is resolved here rather than inside one waiter.
		return a.useSkill(*seat, msg.TargetID)
	case "vote.cast":
		// The ballot is open to the whole table, not just the seat the walk is
		// currently prompting: a player who clicks early must not be rejected and
		// then have to notice the next prompt to try again.
		return a.castVote(*seat, msg.RoleID)
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
		if a.holdTalk(*seat, text, msg.ReplyTo) {
			return nil
		}
		a.appendSpeechTo(*seat, text, msg.ReplyTo)
		return nil
	case "speak.pass":
		// Declining while queued gives the slot back. Outside a discussion
		// there is nothing to decline, and an error frame would just be noise.
		a.dropQueuedTalk(*seat)
		return nil
	case "search.pass":
		return ErrNotAllowed{Reason: "轮到你搜证时才能结束本轮"}
	case "search.clue":
		// A 「可获得曲径通幽 N」 card is a bonus on the parent clue: it does not
		// consume a pick and must stay claimable after this seat's turns are over,
		// because the item it needs often surfaces later in the same round.
		a.mu.Lock()
		claim := phase != nil && phase.Type == domain.PhaseSearch && a.grantedClaimableLocked(*seat, msg.ClueID)
		a.mu.Unlock()
		if !claim {
			return ErrNotAllowed{Reason: "轮到你搜证时才能取线索"}
		}
		return a.claimClue(*seat, msg.ClueID, phase)
	case "clue.reveal":
		return a.revealClue(*seat, msg.ClueID)
	case "card.play", "card.random", "card.pass", "card.stall", "card.give":
		// The cards are handled by the play waiter; reaching here means this
		// seat is not on the floor. Route it anyway so the error says why.
		_, err := a.handleCardCommand(*seat, msg)
		return err
	case "score.submit":
		return ErrNotAllowed{Reason: "现在不是填积分卡的时候"}
	default:
		return ErrNotAllowed{Reason: "当前阶段不接受该操作"}
	}
}

// requestFloor puts a seat at the end of the discussion queue.
func (a *Actor) requestFloor(seat domain.Seat) error {
	a.mu.Lock()
	if !a.talkOpen && !a.discussionLocked() {
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

// discussionLocked reports whether the current phase is a discussion. The
// scripted round still counts: a request typed before the open floor starts is
// queued rather than rejected, which is what left the table waiting on a floor
// nobody could take.
func (a *Actor) discussionLocked() bool {
	ph := a.m.Current(a.g)
	return !a.finished && ph != nil && ph.Type == domain.PhaseDiscussion
}

// dropQueuedTalk removes a seat from the speaking queue when it changes its
// mind, so a withdrawn request cannot keep the floor waiting.
func (a *Actor) dropQueuedTalk(seat domain.Seat) {
	a.mu.Lock()
	idx := a.talkIndexOfLocked(seat.ID)
	if idx < 0 {
		a.mu.Unlock()
		return
	}
	a.talkQueue = append(a.talkQueue[:idx], a.talkQueue[idx+1:]...)
	a.mu.Unlock()
	a.declined(seat, SpeechSlot{SeatID: seat.ID, Kind: SlotRequest})
}

// holdTalk queues a line typed while somebody else holds the floor. It reports
// whether the text was queued, so the caller knows not to speak immediately.
func (a *Actor) holdTalk(seat domain.Seat, text, replyTo string) bool {
	a.mu.Lock()
	if !a.talkOpen && !a.discussionLocked() {
		a.mu.Unlock()
		return false
	}
	if a.floor != nil && a.floor.SeatID == seat.ID {
		a.mu.Unlock()
		return false
	}
	busy := a.floor != nil || !a.talkOpen
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
	if msg.Type == "search.clue" {
		// 「可获得曲径通幽 N」是印在底卡上的附赠，不占本轮次数，所以它不必
		// 挤在本人的搜证回合里：物品可能要到别人抽完牌才凑齐。
		a.mu.Lock()
		ph := a.m.Current(a.g)
		claim := ph != nil && ph.Type == domain.PhaseSearch && a.grantedClaimableLocked(seat, msg.ClueID)
		a.mu.Unlock()
		if !claim {
			return false
		}
		if err := a.claimClue(seat, msg.ClueID, ph); err != nil {
			a.notify(seat, err.Error())
		}
		return true
	}
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
	a.searchQuota = nil
	a.searchExtra = map[string]int{}
	a.hostExtraLeft = ph.HostExtraPicks
	a.searchOpen = ph.Type == domain.PhaseSearch
	a.floor = nil
	a.phaseDone = false
	a.voteClosed = false
	a.resetTalkLocked()
	if ph.Type == domain.PhaseVote {
		// 每个投票阶段是独立的一轮：不清掉上一轮，主持人的计票面板会带着
		// 旧票数开场，看起来像这一轮已经投完了。
		a.g.Votes = map[string]string{}
		// Drop any wake left over from the previous ballot; waiters of this round
		// need a channel that starts empty.
		a.voteWake = make(chan struct{}, 1)
	}
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
	a.phaseAt = time.Now()
	// 「××专属线索」不等搜证：进入阶段就交到主人手里，牌堆里根本没有它。
	a.dealPrivateCluesLocked(ph)
	// 恩宠结算段（K12「水落石出」）：先把分数摆到桌面，再算废后与冷宫。
	if ph.SettleFavor {
		a.settleFavorLocked(ph)
	}
	a.persistLocked()
	a.mu.Unlock()

	// Fold what the previous phases said into a recap before the table speaks
	// again. It runs off the critical path: the phase never waits for it.
	a.startRecap(gen)

	switch ph.Type {
	case domain.PhaseReading, domain.PhaseOpening:
		// Reading and opening are host-paced scenes: the table reads the role
		// books or plays an interactive opening until the host moves on. They
		// have no actions to collect, so there is no waiter to close the phase.
		// Auto-advance is the exception for unattended rooms, which would
		// otherwise park on the first scene forever.
		if a.cfg.AutoAdvance {
			go a.completePhase(ctx, gen)
		}
	case domain.PhasePlay:
		go a.runPlay(ctx, gen)
	case domain.PhaseScore:
		go a.runScore(ctx, gen)
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
	// A search the host cuts short still distributes its leftovers; otherwise a
	// skipped round would leave cards the printed manual says somebody receives.
	a.searchOpen = false
	a.assignLeftoversLocked(a.m.Current(a.g))
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

func (a *Actor) requestAdvance(ctx context.Context) error {
	a.mu.Lock()
	gen := a.phaseGen
	// Reading and opening keep a short floor so a host cannot skip the role
	// book before anybody reads it. The script's timeoutSec is a suggested
	// reading time, not a lock: honouring it here made a 900-second scene
	// ignore the host's "next" for fifteen minutes.
	if ph := a.m.Current(a.g); ph != nil && (ph.Type == domain.PhaseReading || ph.Type == domain.PhaseOpening) {
		if min := a.cfg.ReadingMin; min > 0 {
			if left := min - time.Since(a.phaseAt); left > 0 {
				a.mu.Unlock()
				return ErrNotAllowed{Reason: fmt.Sprintf("阅读阶段还剩 %d 秒，稍后再进入下一阶段", int(left.Seconds())+1)}
			}
		}
	}
	a.floor = nil
	a.mu.Unlock()
	a.advance(ctx, gen)
	return nil
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
	a.phaseDone = true
	a.voteClosed = true
	a.floor = nil
	a.talkOpen = false
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
	if len(ph.OnlyRoleIDs) > 0 {
		// A phase dealt to a subset keeps the seating order but drops everyone
		// else, so the table is not walked through empty turns.
		only := make([]domain.Seat, 0, len(ph.OnlyRoleIDs))
		for _, s := range seats {
			if slices.Contains(ph.OnlyRoleIDs, s.RoleID) {
				only = append(only, s)
			}
		}
		seats = only
	}
	a.mu.Unlock()

	switch ph.Order {
	case domain.OrderHostAssigned:
		return a.awaitOrder(ctx, gen, ph)
	case domain.OrderFixed:
		// 剧本把顺序印死了（K6 三轮搜证各一行名字），直接照着排，不需要主持人
		// 重新摆一遍。
		a.mu.Lock()
		fixed := make([]domain.Seat, 0, len(ph.OrderRoleIDs))
		for _, rid := range ph.OrderRoleIDs {
			for _, s := range seats {
				if s.RoleID == rid {
					fixed = append(fixed, s)
					break
				}
			}
		}
		a.order = fixed
		a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: "本阶段顺序（照剧本）：" + a.orderNamesLocked(fixed)})
		a.persistLocked()
		a.mu.Unlock()
		return true
	case domain.OrderRandom:
		shuffled := append([]domain.Seat(nil), seats...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		a.mu.Lock()
		a.order = shuffled
		a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: "本阶段顺序（随机）：" + a.orderNamesLocked(shuffled)})
		a.persistLocked()
		a.mu.Unlock()
		return true
	case domain.OrderFavor:
		// 恩宠值高的先走（K12 搜证顺序）。同分时保持座位序，重放才读得一致。
		ranked := append([]domain.Seat(nil), seats...)
		a.mu.Lock()
		sort.SliceStable(ranked, func(i, j int) bool {
			return a.g.Points[ranked[i].ID] > a.g.Points[ranked[j].ID]
		})
		a.order = ranked
		a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: "本阶段顺序（按恩宠值）：" + a.orderNamesLocked(ranked)})
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
		if len(ph.OnlyRoleIDs) > 0 {
			// The phase only deals to a subset (K5's three search rounds are for
			// the five wizards), so the fallback keeps that subset too.
			only := make([]domain.Seat, 0, len(a.order))
			for _, s := range a.order {
				if slices.Contains(ph.OnlyRoleIDs, s.RoleID) {
					only = append(only, s)
				}
			}
			a.order = only
		}
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
		Text:       fmt.Sprintf("本阶段顺序由你指定：把 %d 位可以行动的角色排好顺序后点「确定顺序」。", len(a.phasePlayersLocked(ph))),
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
	// A phase with onlyRoleIds is played by that subset alone, so the host orders
	// exactly those seats — asking for the whole table would leave the excluded
	// role with a turn it cannot use.
	players := a.phasePlayersLocked(ph)
	byID := make(map[string]domain.Seat, len(players))
	for _, s := range players {
		byID[s.ID] = s
	}
	if len(ids) != len(players) {
		return ErrNotAllowed{Reason: fmt.Sprintf("请把 %d 个能行动的角色全部排进顺序里", len(players))}
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
	var players []domain.Seat
	if ph := a.m.Current(a.g); ph != nil {
		players = a.phasePlayersLocked(ph)
	} else {
		players = a.g.PlayerSeats()
	}
	for _, s := range players {
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

// phasePlayersLocked narrows the table to the seats a phase deals to. A phase
// with onlyRoleIds keeps the seating order but drops everyone else, so the walk
// never hands an empty turn to a role that may not act here.
func (a *Actor) phasePlayersLocked(ph *domain.Phase) []domain.Seat {
	players := a.g.PlayerSeats()
	if ph == nil || len(ph.OnlyRoleIDs) == 0 {
		return players
	}
	only := make([]domain.Seat, 0, len(ph.OnlyRoleIDs))
	for _, s := range players {
		if slices.Contains(ph.OnlyRoleIDs, s.RoleID) {
			only = append(only, s)
		}
	}
	return only
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
	a.mu.Unlock()

	// The order belongs to the phase, not to the seat: every turn goes through
	// nextSeat so a host layout or a shuffle applies to search exactly as it
	// does to discussion.
	if !a.prepareOrder(ctx, gen, ph) {
		return
	}
	a.layoutQuotaLocked(ph)

	for {
		if !a.alive(gen) || ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		next := a.nextSearcherLocked(ph)
		a.mu.Unlock()
		if next == nil {
			break
		}
		if next.Kind == domain.SeatAgent {
			if !a.agentSearch(*next, ph) {
				// Nothing left this seat may take: retire it for the phase, or the
				// walk would keep handing it the same empty turn.
				a.mu.Lock()
				a.searched[next.ID] = a.searchLimitLocked(*next, ph)
				a.mu.Unlock()
			}
		} else {
			a.awaitSearch(ctx, gen, *next, ph)
		}
	}
	a.mu.Lock()
	if gen == a.phaseGen {
		// The host may have cut the round while the last seat was finishing. In
		// that case the next phase already owns searchOpen; do not close it here.
		a.searchOpen = false
		a.assignLeftoversLocked(ph)
	}
	a.mu.Unlock()
	a.completePhase(ctx, gen)
}

// layoutQuotaLocked turns PickByRank into a per-seat map once the running order
// is known: the first seat in the order draws PickByRank[0], the next
// PickByRank[1], and anyone past the end takes the last figure. Callers hold mu.
func (a *Actor) layoutQuotaLocked(ph *domain.Phase) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.searchQuota = nil
	if len(ph.PickByRank) == 0 || len(a.order) == 0 {
		return
	}
	quota := make(map[string]int, len(a.order))
	last := ph.PickByRank[len(ph.PickByRank)-1]
	for i, s := range a.order {
		if i < len(ph.PickByRank) {
			quota[s.ID] = ph.PickByRank[i]
		} else {
			quota[s.ID] = last
		}
	}
	a.searchQuota = quota
}

// grantSearchPick spends one of the phase's host extra picks on a seat. The
// bonus is a table rule, not a script rule: the host decides who earned it, and
// the seat simply owes one more draw.
func (a *Actor) grantSearchPick(host domain.Seat, targetID, phaseID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ph := a.m.Current(a.g)
	if ph == nil || ph.Type != domain.PhaseSearch || a.phaseDone || !a.searchOpen {
		return ErrNotAllowed{Reason: "现在不是搜证阶段"}
	}
	if phaseID != "" && phaseID != ph.ID {
		return ErrNotAllowed{Reason: "阶段已切换"}
	}
	if ph.HostExtraPicks <= 0 {
		return ErrNotAllowed{Reason: "本阶段没有额外搜证名额"}
	}
	if a.hostExtraLeft <= 0 {
		return ErrNotAllowed{Reason: "本阶段的额外搜证名额已经发完"}
	}
	target := a.g.SeatByID(targetID)
	if target == nil || target.Kind == domain.SeatSpectator || target.IsHost {
		return ErrNotAllowed{Reason: "请选择一名在场玩家"}
	}
	if len(ph.OnlyRoleIDs) > 0 && !slices.Contains(ph.OnlyRoleIDs, target.RoleID) {
		return ErrNotAllowed{Reason: "该角色本阶段不能搜证"}
	}
	// The target must already own a turn in this round. Granting to a seat the
	// printed order skips would spend the budget on a pick nobody can use.
	inOrder := false
	for _, s := range a.order {
		if s.ID == target.ID {
			inOrder = true
			break
		}
	}
	if !inOrder {
		return ErrNotAllowed{Reason: "该玩家不在本阶段搜证顺序里"}
	}
	if len(a.availableCluesLocked(*target, ph)) == 0 {
		return ErrNotAllowed{Reason: "该玩家现在没有可以搜的线索"}
	}
	if cost := a.drawCost(ph); cost > 0 && a.g.Points[target.ID] < cost {
		return ErrNotAllowed{Reason: "该玩家的行动点不够再搜一张"}
	}
	a.hostExtraLeft--
	if a.searchExtra == nil {
		a.searchExtra = map[string]int{}
	}
	a.searchExtra[target.ID]++
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		PhaseID: ph.ID,
		Text:    fmt.Sprintf("主持人判定 %s 获得额外一次搜证机会", a.speakerLocked(*target)),
	})
	a.persistLocked()
	return nil
}

// assignLeftoversLocked hands every unclaimed card in a curated search pool to
// Phase.LeftoverRoleID when the round closes. It runs both when the round ends
// naturally and when the host cuts it short, so a skipped search cannot swallow
// a clue the printed manual says the narrator receives.
func (a *Actor) assignLeftoversLocked(ph *domain.Phase) {
	if ph == nil || a.m.Current(a.g) != ph || ph.Type != domain.PhaseSearch || ph.LeftoverRoleID == "" || len(ph.CluePool) == 0 {
		return
	}
	var owner *domain.Seat
	for i := range a.g.Seats {
		if a.g.Seats[i].RoleID == ph.LeftoverRoleID && a.g.Seats[i].Kind != domain.SeatSpectator {
			owner = &a.g.Seats[i]
			break
		}
	}
	now := time.Now().UTC()
	for _, id := range ph.CluePool {
		c := a.m.TokenClue(id)
		if c == nil || c.OwnerRoleID != "" {
			// 有主人的私人物品不进剩卡堆：把它塞给旁白等于把别人的隐私线索送人。
			continue
		}
		st := a.g.ClueState[id]
		if st.FoundBySeat != "" || st.Revealed {
			continue
		}
		st.ClueID = id
		st.FoundAt = now
		// An underfilled room may not have the printed recipient. The cards are
		// still game material, so they go to the table instead of disappearing.
		public := owner == nil || ph.LeftoverReveal || ph.RevealOnFind || c.MustReveal
		if owner != nil {
			st.FoundBySeat = owner.ID
		}
		if public {
			st.Revealed = true
			if owner != nil {
				st.RevealedBy = owner.ID
			}
			st.RevealedAt = now
		}
		a.g.ClueState[id] = st
		if public {
			forWhat := ""
			seatID, roleID := "", ""
			if owner != nil {
				forWhat = "，归" + a.speakerLocked(*owner)
				seatID, roleID = owner.ID, owner.RoleID
			}
			a.appendLocked(domain.Event{
				Type:    domain.EvClueRevealed,
				PhaseID: ph.ID,
				SeatID:  seatID,
				RoleID:  roleID,
				ClueID:  id,
				Text:    fmt.Sprintf("搜证结束，剩余线索【%s】公开%s：%s", c.Title, forWhat, c.Content),
				Meta:    map[string]string{"title": c.Title, "content": c.Content},
			})
			a.unlockKeywordsLocked(c.Title + " " + c.Content)
			continue
		}
		if c.GrantsItem != "" && a.g.GrantItem(owner.ID, c.GrantsItem) {
			a.appendLocked(domain.Event{
				Type:       domain.EvSystem,
				PhaseID:    ph.ID,
				SeatID:     owner.ID,
				Visibility: SeatVisibility(owner.ID),
				Text:       fmt.Sprintf("剩余线索带来了物品【%s】", c.GrantsItem),
			})
		}
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			PhaseID:    ph.ID,
			SeatID:     owner.ID,
			ClueID:     id,
			Visibility: SeatVisibility(owner.ID),
			Text:       fmt.Sprintf("搜证结束，剩余线索【%s】归你：%s", c.Title, c.Content),
			Meta:       map[string]string{"clueId": id, "title": c.Title},
		})
	}
	a.persistLocked()
}

// searchLimitLocked is how many cards one seat owes this round: the rank figure
// when the script hands them out by favour, otherwise the flat phase limit.
func (a *Actor) searchLimitLocked(seat domain.Seat, ph *domain.Phase) int {
	base := 1
	if q, ok := a.searchQuota[seat.ID]; ok {
		if q > 0 {
			base = q
		}
	} else if limit := ph.PickLimit(); limit > 0 {
		base = limit
	}
	return base + a.searchExtra[seat.ID]
}

// nextSearcherLocked walks the phase order and returns the next seat that may
// actually draw. Seats whose round quota is spent — or whose bones ran out, or
// were seized by 北照君's skill — are retired here instead of being handed a turn
// that can only fail. Callers hold mu.
func (a *Actor) nextSearcherLocked(ph *domain.Phase) *domain.Seat {
	for {
		next := a.searchSeatLocked()
		if next == nil || a.picksLeftLocked(*next, ph) > 0 {
			return next
		}
		a.searched[next.ID] = a.searchLimitLocked(*next, ph)
	}
}

// searchSeatLocked is nextSeat with the rank quota applied: a seat is done once
// it has taken its own printed figure, not once it has taken the flat one.
func (a *Actor) searchSeatLocked() *domain.Seat {
	if len(a.order) == 0 {
		return nil
	}
	n := len(a.order)
	for i := 0; i < n; i++ {
		idx := (a.orderPos + i) % n
		seat := &a.order[idx]
		limit := 1
		if ph := a.m.Current(a.g); ph != nil {
			limit = a.searchLimitLocked(*seat, ph)
		}
		if a.searched[seat.ID] < limit {
			a.orderPos = (idx + 1) % n
			return seat
		}
	}
	return nil
}

// picksLeftLocked is how many more cards one seat can take this round: the
// printed quota, capped by the bones it still holds. Callers hold mu.
func (a *Actor) picksLeftLocked(seat domain.Seat, ph *domain.Phase) int {
	left := remainingPicks(a.searched[seat.ID], a.searchLimitLocked(seat, ph))
	if cost := a.drawCost(ph); cost > 0 {
		if afford := a.g.Points[seat.ID] / cost; afford < left {
			left = afford
		}
	}
	return left
}

// drawCost is how many action points one draw spends. A script with a point
// pool costs one bone per card unless the phase says otherwise.
func (a *Actor) drawCost(ph *domain.Phase) int {
	if ph.DrawCost > 0 {
		return ph.DrawCost
	}
	if a.sc.Meta.StartPoints > 0 {
		return 1
	}
	return 0
}

// pointsLabel is the printed name of the currency (仙骨), or a neutral default.
func (a *Actor) pointsLabel() string {
	if s := strings.TrimSpace(a.sc.Meta.PointsLabel); s != "" {
		return s
	}
	return "行动点"
}

// hasPointPoolLocked reports whether this script hands out any points at all:
// either a flat Meta.StartPoints or a per-role figure (K12 每宫的圣宠值).
// Callers hold mu.
func (a *Actor) hasPointPoolLocked() bool {
	if a.sc.Meta.StartPoints > 0 {
		return true
	}
	for _, r := range a.sc.Roles {
		if r.Points > 0 {
			return true
		}
	}
	return false
}

// startPointsLocked is what one seat begins with: the role's own figure wins
// over the flat Meta.StartPoints.
func (a *Actor) startPointsLocked(seat domain.Seat) int {
	for _, r := range a.sc.Roles {
		if r.ID == seat.RoleID && r.Points > 0 {
			return r.Points
		}
	}
	return a.sc.Meta.StartPoints
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
	left := a.picksLeftLocked(seat, ph)
	// A seat can run out of cards it is allowed to take while it still owes
	// picks: its own frisking clue goes back to the pile, a deep-dive card is
	// still missing its item. Retire the seat here, the same way the agent walk
	// does, instead of handing it a button that can only fail.
	if left > 0 && len(a.availableCluesLocked(seat, ph)) == 0 {
		a.searched[seat.ID] = a.searchLimitLocked(seat, ph)
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			SeatID:     seat.ID,
			Visibility: SeatVisibility(seat.ID),
			PhaseID:    ph.ID,
			Text:       "剩下的线索都不能由你搜（本人的搜身线索、缺物品或缺少前置线索），本轮搜证到此为止",
		})
		a.persistLocked()
		a.mu.Unlock()
		return false
	}
	ch := make(chan ClientMessage, 1)
	a.waiters[seat.ID] = ch
	prompt := fmt.Sprintf("轮到你搜证，可搜 %d 次：%s", left, strings.Join(a.searchableTitlesLocked(seat, ph), "、"))
	if plan := a.pendingDrawsLocked(seat, ph); plan != "" {
		prompt = fmt.Sprintf("轮到你搜证，本轮还要抽：%s", plan)
	}
	if ph.SearchMode == domain.SearchRandom {
		// Random mode hides the list on purpose: knowing the titles would turn
		// "draw" back into "choose".
		prompt = fmt.Sprintf("轮到你搜证，还剩 %d 次随机抽取", left)
		if plan := a.pendingDrawsLocked(seat, ph); plan != "" {
			prompt = fmt.Sprintf("轮到你搜证，本轮还要抽：%s", plan)
		}
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
		if a.floor != nil && a.floor.SeatID == seat.ID && a.floor.PhaseID == ph.ID {
			a.floor = nil
		}
		a.mu.Unlock()
	}()

	timer, expired := a.cfg.deadline()
	if timer != nil {
		defer timer.Stop()
	}
	gone, stopGone := a.goneWatcher(seat.ID)
	defer stopGone()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-gone:
			a.mu.Lock()
			if gen == a.phaseGen {
				a.searched[seat.ID]++
				a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, Visibility: SeatVisibility(seat.ID), Text: "搜证时已离线，自动跳过"})
				a.persistLocked()
			}
			a.mu.Unlock()
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
					if a.retireExhaustedSearch(seat, ph) {
						return false
					}
					a.notify(seat, err.Error())
					if a.picksLeftLocked(seat, ph) <= 0 {
						// 仙骨正好在刚才那一手花完了（或被人抽干）：这个回合已经没有可做的事。
						return true
					}
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				if a.retireExhaustedSearch(seat, ph) {
					return false
				}
				if left := a.keepTurn(seat, ph, "搜证"); left > 0 {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				if a.holdForGranted(seat, ph) {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				return true
			case "search.draw":
				// Random mode: the seat does not name a clue, it just draws one.
				if err := a.drawSearch(seat, ph); err != nil {
					if a.retireExhaustedSearch(seat, ph) {
						return false
					}
					a.notify(seat, err.Error())
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				if a.retireExhaustedSearch(seat, ph) {
					return false
				}
				if left := a.keepTurn(seat, ph, "随机抽取"); left > 0 {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				if a.holdForGranted(seat, ph) {
					if timer != nil {
						timer.Reset(a.cfg.floor())
					}
					continue
				}
				return true
			case "search.pass":
				// 保留仙骨另作他用：主动结束本轮，剩下的额度作废。
				a.mu.Lock()
				if a.m.Current(a.g) == ph {
					a.searched[seat.ID] = a.searchLimitLocked(seat, ph)
					a.appendLocked(domain.Event{
						Type:       domain.EvSystem,
						SeatID:     seat.ID,
						Visibility: SeatVisibility(seat.ID),
						PhaseID:    ph.ID,
						Text:       fmt.Sprintf("你结束了本轮搜证，还剩 %d 根%s", a.g.Points[seat.ID], a.pointsLabel()),
					})
					a.persistLocked()
				}
				a.mu.Unlock()
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
	left := remainingPicks(a.searched[seat.ID], a.searchLimitLocked(seat, ph))
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

// holdForGranted keeps the floor after the quota is spent as long as a
// 「可获得曲径通幽 N」 card is still waiting to be named. That card is a bonus
// printed on the parent clue, so it must stay claimable on the seat's very
// last draw instead of silently expiring with the turn.
// retireExhaustedSearch ends a turn whose owner still owes picks but has
// nothing left to take: the pool ran dry mid-round. Without this the waiter
// answers every click with "本轮线索已被抽完" and never yields the floor.
func (a *Actor) retireExhaustedSearch(seat domain.Seat, ph *domain.Phase) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m.Current(a.g) != ph || a.picksLeftLocked(seat, ph) <= 0 {
		return false
	}
	if len(a.availableCluesLocked(seat, ph)) > 0 {
		return false
	}
	a.searched[seat.ID] = a.searchLimitLocked(seat, ph)
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     seat.ID,
		Visibility: SeatVisibility(seat.ID),
		PhaseID:    ph.ID,
		Text:       "本轮线索已被抽完，本轮搜证结束",
	})
	a.persistLocked()
	return true
}

func (a *Actor) holdForGranted(seat domain.Seat, ph *domain.Phase) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m.Current(a.g) != ph || len(a.grantedOptionsLocked(seat, ph)) == 0 {
		return false
	}
	a.floor = &Floor{SeatID: seat.ID, PhaseID: ph.ID, ExpiresAt: time.Now().Add(a.cfg.floor())}
	a.persistLocked()
	return true
}

// availableCluesLocked lists the clues one seat may still take, mirroring the
// filter of claimClue so the UI only ever offers accepted choices. Callers hold
// mu.
func (a *Actor) availableCluesLocked(seat domain.Seat, ph *domain.Phase) []*domain.Clue {
	plan := ph.DrawPlan()
	if len(plan) > 0 && a.searched[seat.ID] >= len(plan) {
		// A typed round ends exactly when its plan is spent; without this the
		// last kind would keep offering cards after the quota was used up.
		return nil
	}
	want := drawKind(ph, a.searched[seat.ID])
	out := a.gatherCluesLocked(seat, ph, want, false)
	if len(out) == 0 {
		// 印的规则是「抽到自己的线索卡请放回卡堆内」。牌堆快见底时放回去也
		// 抽不到别的，坚持不放只会让末位玩家少拿线索；这时按卡类退让，
		// 把本人房间的卡也列上。自己的搜身线索不能退：那等于把本人身上的
		// 物证摊给本人看，印规则一律要求放回。
		for _, c := range a.gatherCluesLocked(seat, ph, want, true) {
			if patDownOf(c, seat) {
				continue
			}
			out = append(out, c)
		}
	}
	return out
}

// gatherCluesLocked collects the cards claimClue would accept right now. When
// allowOwn is set, the "your own room" guard is skipped — the caller only does
// that after finding nothing else left of the wanted kind.
func (a *Actor) gatherCluesLocked(seat domain.Seat, ph *domain.Phase, want string, allowOwn bool) []*domain.Clue {
	var out []*domain.Clue
	ownFloor := a.seatFloorLocked(seat)
	for _, cid := range a.searchPoolLocked(ph) {
		c := a.m.TokenClue(cid)
		if c == nil {
			continue
		}
		st := a.g.ClueState[cid]
		if st.FoundBySeat != "" || st.Revealed {
			continue
		}
		if want != "" && c.Kind != want {
			continue
		}
		if c.OwnerRoleID != "" && c.OwnerRoleID != seat.RoleID {
			continue
		}
		if a.clueBlockedLocked(seat, c) != "" && !(allowOwn && (c.SubjectRoleID == seat.RoleID || sameFloor(c, ownFloor))) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// dealPrivateCluesLocked hands the phase's 「专属线索」 to their owners the
// moment it opens. The card never sits in a pool, so no random draw can leak
// it, and the owner gets the same transcript line a search would produce.
// Callers hold mu.
func (a *Actor) dealPrivateCluesLocked(ph *domain.Phase) {
	if len(ph.PrivateClues) == 0 {
		return
	}
	now := time.Now().UTC()
	for _, id := range ph.PrivateClues {
		c := a.m.TokenClue(id)
		if c == nil || c.OwnerRoleID == "" {
			continue
		}
		st := a.g.ClueState[id]
		if st.FoundBySeat != "" || st.Revealed {
			continue
		}
		var owner *domain.Seat
		for i := range a.g.Seats {
			if a.g.Seats[i].RoleID == c.OwnerRoleID && a.g.Seats[i].Kind != domain.SeatSpectator {
				owner = &a.g.Seats[i]
				break
			}
		}
		if owner == nil {
			continue
		}
		st.ClueID = id
		st.FoundBySeat = owner.ID
		st.PrivateOwner = c.OwnerRoleID
		st.FoundAt = now
		a.g.ClueState[id] = st
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			PhaseID:    ph.ID,
			SeatID:     owner.ID,
			ClueID:     id,
			Visibility: SeatVisibility(owner.ID),
			Text:       fmt.Sprintf("你收到了专属线索【%s】：%s", c.Title, c.Content),
			Meta:       map[string]string{"clueId": id, "title": c.Title},
		})
	}
}

// grantedCluesLocked names the out-of-pool cards one seat may claim because it
// already found a clue that prints them ("获得这张线索，可开启曲径通幽 N").
// The parent may have been found rounds ago, so the set is derived from the
// whole clue log rather than from the current phase. Callers hold mu.
func (a *Actor) grantedCluesLocked(seat domain.Seat) map[string]bool {
	out := map[string]bool{}
	for id, st := range a.g.ClueState {
		if st.FoundBySeat != seat.ID {
			continue
		}
		c := a.m.TokenClue(id)
		if c == nil {
			continue
		}
		for _, g := range c.GrantsClue {
			out[g] = true
		}
	}
	return out
}

// patDownOf reports whether the card is this seat's own body-search clue. The
// printed rule 「抽到自己的线索卡请放回卡堆内」 always binds here: relaxing it
// would deal the seat its own evidence. Other cards that merely belong to the
// seat (its room) may still be taken once nothing else is left in the deck.
func patDownOf(c *domain.Clue, seat domain.Seat) bool {
	if c.SubjectRoleID == "" || c.SubjectRoleID != seat.RoleID {
		return false
	}
	// K 系列的牌堆 kind 是卡册名（纤毫毕现／临渊结网），搜身信息只落在
	// location/title 上；只看 kind 会把自己的搜身卡当成普通房间卡退让出去。
	return strings.Contains(c.Kind, "搜身") ||
		strings.Contains(c.Location, "搜身") ||
		strings.Contains(c.Title, "搜身")
}

// grantedClaimableLocked reports whether clueID is a 「可获得曲径通幽 N」 card
// whose parent card this seat holds and which nobody has taken yet. Such a card
// is a bonus printed on the parent clue: it never consumes a pick, so it stays
// claimable for the whole phase even after this seat's turns are over.
// Callers hold mu.
func (a *Actor) grantedClaimableLocked(seat domain.Seat, clueID string) bool {
	if clueID == "" || !a.grantedCluesLocked(seat)[clueID] {
		return false
	}
	st := a.g.ClueState[clueID]
	if st.FoundBySeat != "" || st.Revealed {
		return false
	}
	c := a.m.TokenClue(clueID)
	if c == nil {
		return false
	}
	if c.OwnerRoleID != "" && c.OwnerRoleID != seat.RoleID {
		return false
	}
	if a.clueBlockedLocked(seat, c) == "" {
		return true
	}
	// 「缺少开启线索的物品…达成共识后可共同开启」：物品在别人手上也行，
	// 但只在确实有那个人时才算可领，否则按钮会亮着却点不动。
	return len(c.RequiresItems) > 0 && a.itemPartnerLocked(seat, c) != nil
}

// grantedOptionsLocked lists the 「可获得曲径通幽 N」 cards one seat may name
// right now: the parent card is in hand, the named card is still unfound, and
// either the seat or a co-op partner holds the printed items. This is the only
// path to a deep-dive card — random draws never deal them.
func (a *Actor) grantedOptionsLocked(seat domain.Seat, ph *domain.Phase) []SearchOption {
	granted := a.grantedCluesLocked(seat)
	var out []SearchOption
	for i := range a.sc.Clues {
		c := &a.sc.Clues[i]
		if !granted[c.ID] {
			continue
		}
		st := a.g.ClueState[c.ID]
		if st.FoundBySeat != "" || st.Revealed {
			continue
		}
		if c.OwnerRoleID != "" && c.OwnerRoleID != seat.RoleID {
			continue
		}
		if reason := a.clueBlockedLocked(seat, c); reason != "" {
			if len(c.RequiresItems) == 0 || a.itemPartnerLocked(seat, c) == nil {
				continue
			}
		}
		out = append(out, SearchOption{ID: c.ID, Hint: grantedHint(c), Granted: true})
	}
	return out
}

// itemPartnerLocked finds another seat holding every item a granted card asks
// for. The printed rule is 「缺少开启线索的物品…达成共识后可共同开启」: the
// card stays with the finder, the item does not have to change hands.
func (a *Actor) itemPartnerLocked(seat domain.Seat, c *domain.Clue) *domain.Seat {
	if len(c.RequiresItems) == 0 {
		return nil
	}
	for i := range a.g.Seats {
		s := &a.g.Seats[i]
		if s.ID == seat.ID || s.Kind == domain.SeatSpectator {
			continue
		}
		if a.g.HasItems(s.ID, c.RequiresItems) {
			return s
		}
	}
	return nil
}

// grantedHint is the back of a 曲径通幽 card: the pile name and its number.
func grantedHint(c *domain.Clue) string {
	if s := strings.TrimSpace(c.Back); s != "" {
		return s
	}
	if c.Number != "" {
		return "曲径通幽 " + c.Number
	}
	return c.Title
}

// searchPoolLocked is the set of cards this round may deal at all. A curated
// pool wins; a phase that only lists draws takes its cards from the whole deck
// by kind, which is how the printed books do it ("从轮船线索里随机抽 3 张").
func (a *Actor) searchPoolLocked(ph *domain.Phase) []string {
	if len(ph.CluePool) > 0 {
		return ph.CluePool
	}
	plan := ph.DrawPlan()
	if len(plan) == 0 {
		return nil
	}
	kinds := map[string]bool{}
	for _, k := range plan {
		kinds[k] = true
	}
	var out []string
	for _, c := range a.m.sc.Clues {
		if len(kinds) == 0 || kinds[""] || kinds[c.Kind] {
			out = append(out, c.ID)
		}
	}
	return out
}

// drawKind is the kind of the next card one seat owes, or "" when this phase
// does not split its deck. Reading it off the plan means the mix cannot drift:
// the first pick is whatever the first rule says, no matter what the seat wants.
func drawKind(ph *domain.Phase, drawn int) string {
	plan := ph.DrawPlan()
	if drawn < 0 || drawn >= len(plan) {
		return ""
	}
	return plan[drawn]
}

// pendingDrawsLocked renders what is left of a seat's typed plan, e.g.
// "随身物品 1 张、轮船线索 2 张", so the prompt tells the table what the next
// clicks will be instead of only how many are left.
func (a *Actor) pendingDrawsLocked(seat domain.Seat, ph *domain.Phase) string {
	plan := ph.DrawPlan()
	done := a.searched[seat.ID]
	if len(plan) == 0 || done >= len(plan) {
		return ""
	}
	var kinds []string
	counts := map[string]int{}
	for _, k := range plan[done:] {
		if counts[k] == 0 {
			kinds = append(kinds, k)
		}
		counts[k]++
	}
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		name := k
		if name == "" {
			name = "任意线索"
		}
		parts = append(parts, fmt.Sprintf("%s %d 张", name, counts[k]))
	}
	return strings.Join(parts, "、")
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
		// 随机抽取的轮次里唯一能点名的，是底卡写着「可获得曲径通幽 N」的那张：
		// 印的规则就是「抽到这张卡的人可获取一张指定的深入调查线索」。
		a.mu.Lock()
		granted := a.grantedCluesLocked(seat)[clueID]
		a.mu.Unlock()
		if !granted {
			return ErrNotAllowed{Reason: "本阶段是随机抽取，请点「随机抽取」"}
		}
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
	for _, id := range a.searchPoolLocked(ph) {
		if id == clueID {
			inPool = true
			break
		}
	}
	// 底卡指定的深入调查卡不在本阶段牌堆里，但它的主人可以点名要（同一阶段
	// 或之后任何一轮都行）。这条通道只给「已经抽到写它名字的那张卡」的人。
	granted := a.grantedCluesLocked(seat)[clueID]
	if !inPool && !granted {
		return ErrNotAllowed{Reason: "本轮无法搜到该线索"}
	}
	if len(ph.OnlyRoleIDs) > 0 && !slices.Contains(ph.OnlyRoleIDs, seat.RoleID) {
		// The turn order already skips these seats; this is the belt to that
		// suspenders, so a stray request cannot take another role's card.
		return ErrNotAllowed{Reason: "本阶段只有指定角色可以搜证"}
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
	var partner *domain.Seat
	if reason := a.clueBlockedLocked(seat, c); reason != "" {
		if granted {
			// 「缺少开启线索的物品…达成共识后可共同开启」：物品在桌边任何人
			// 手上都行，不用搬家，但要记下是谁一起开的。
			partner = a.itemPartnerLocked(seat, c)
		}
		if partner == nil {
			// 「抽到自己的卡请放回卡堆内」只在还有别的卡可抽时成立。牌堆见底时
			// 放回去也抽不到，只能把这张给他，否则末位座位会少拿一张。这里必须
			// 问「本轮还有没有任何一张能抽的卡」，不能只问同一种卡：同楼层的卡
			// 按定义永远凑不出非本楼层的同类，按同一种卡判断等于无条件放行。
			relaxable := sameFloor(c, a.seatFloorLocked(seat)) || (c.SubjectRoleID == seat.RoleID && !patDownOf(c, seat))
			if !(relaxable && len(a.gatherCluesLocked(seat, ph, "", false)) == 0) {
				return ErrNotAllowed{Reason: reason}
			}
		}
	}
	if !granted {
		// 底卡指定的那张深入调查是附赠：不占本轮 2/4/6 张的名额，也不花行动点。
		if want := drawKind(ph, a.searched[seat.ID]); want != "" && c.Kind != want {
			return ErrNotAllowed{Reason: fmt.Sprintf("本轮这一步要先抽%s", want)}
		}
		if a.searched[seat.ID] >= a.searchLimitLocked(seat, ph) {
			return ErrNotAllowed{Reason: "本轮搜证次数已用完"}
		}
		cost := a.drawCost(ph)
		if cost > 0 && a.g.Points[seat.ID] < cost {
			return ErrNotAllowed{Reason: fmt.Sprintf("你的%s只剩 %d 根，不够抽卡了", a.pointsLabel(), a.g.Points[seat.ID])}
		}
		a.searched[seat.ID]++
		if cost > 0 {
			// 抽卡本身就要花仙骨（K6：每抽 1 张花 1 根），这也是技能挑时间的原因。
			a.g.Points[seat.ID] -= cost
		}
		if c.ExtraDraw && a.searched[seat.ID] > 0 {
			// 「获得这张线索的巫师，可以抽取一张额外的线索」：把这一次退回去，
			// 等于本轮多给一张，且仍受阶段卡池与本人房间的约束。
			a.searched[seat.ID]--
		}
	}
	st.ClueID = clueID
	st.FoundBySeat = seat.ID
	if c.OwnerRoleID != "" {
		st.PrivateOwner = c.OwnerRoleID
	}
	now := time.Now().UTC()
	st.FoundAt = now
	// revealOnFind opens the whole phase; mustReveal opens one card inside an
	// otherwise private round ("本线索必须公开"). Either way the find is read
	// aloud at once and the seat never holds it alone.
	public := ph.RevealOnFind || c.MustReveal
	if public {
		st.Revealed = true
		st.RevealedBy = seat.ID
		st.RevealedAt = now
	}
	a.g.ClueState[clueID] = st
	if public {
		a.appendLocked(domain.Event{
			Type:    domain.EvClueRevealed,
			PhaseID: ph.ID,
			SeatID:  seat.ID,
			RoleID:  seat.RoleID,
			ClueID:  clueID,
			Text:    fmt.Sprintf("%s 搜到并公开了线索【%s】：%s", a.speakerLocked(seat), c.Title, c.Content),
			Meta:    map[string]string{"title": c.Title, "content": c.Content},
		})
	} else {
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
	}
	if c.GrantsItem != "" && a.g.GrantItem(seat.ID, c.GrantsItem) {
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			PhaseID:    ph.ID,
			SeatID:     seat.ID,
			Visibility: SeatVisibility(seat.ID),
			Text:       fmt.Sprintf("你获得了物品【%s】", c.GrantsItem),
		})
	}
	if partner != nil {
		// 共同开启不改变卡的主人：卡仍然归抽到底卡的人，合作者只知道内容。
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			PhaseID:    ph.ID,
			SeatID:     seat.ID,
			Visibility: SeatVisibility(seat.ID),
			Text:       fmt.Sprintf("你与 %s 共同开启了曲径通幽【%s】", a.speakerLocked(*partner), c.Title),
		})
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			PhaseID:    ph.ID,
			SeatID:     partner.ID,
			Visibility: SeatVisibility(partner.ID),
			Text:       fmt.Sprintf("你与 %s 共同开启了曲径通幽【%s】：%s", a.speakerLocked(seat), c.Title, c.Content),
			Meta:       map[string]string{"clueId": clueID, "title": c.Title},
		})
	}
	if public {
		a.unlockKeywordsLocked(c.Title + " " + c.Content)
	}
	a.persistLocked()
	return nil
}

// clueBlockedLocked explains why a clue is not searchable yet. The empty string
// means it may be taken. The same rule list backs both the pick list and the
// claim, so a client never sees an option the server would refuse.
func (a *Actor) clueBlockedLocked(seat domain.Seat, c *domain.Clue) string {
	if c.SubjectRoleID != "" && c.SubjectRoleID == seat.RoleID {
		return "这是你本人的搜身线索，放回原位后重新抽取"
	}
	if sameFloor(c, a.seatFloorLocked(seat)) {
		return "这是你所在楼层的线索，放回原位后重新抽取"
	}
	if !a.g.HasItems(seat.ID, c.RequiresItems) {
		return fmt.Sprintf("先拿到%s，才能翻这条线索", strings.Join(c.RequiresItems, "、"))
	}
	for _, cid := range c.UnlockedBy {
		st := a.g.ClueState[cid]
		if st.FoundBySeat == "" && !st.Revealed {
			return "这条线索还没有浮出来，先把场上的线索查完"
		}
	}
	return ""
}

// seatFloorLocked is the storey the seat's room sits on, or "" when the book
// does not use floors. Cards on that storey are stocked back into the deck.
func (a *Actor) seatFloorLocked(seat domain.Seat) string {
	for _, r := range a.m.sc.Roles {
		if r.ID == seat.RoleID {
			return r.Floor
		}
	}
	return ""
}

// sameFloor reports whether a card belongs to the storey the seat lives on.
func sameFloor(c *domain.Clue, floor string) bool {
	return floor != "" && c.Floor != "" && c.Floor == floor
}

// unlockKeywordsLocked opens every memory fragment whose keyword just entered
// the public air — a spoken line or a clue shown to the table. The fragment
// itself stays private to its role; only the trigger is public.
//
// A fragment printed as a chain (「当前两个阶段都已解锁」) lists its parents in
// UnlockedBy and waits for them; one printed as 「说到 A 和 B 这两个词」 sets
// KeywordsAll and waits until both have been said somewhere at the table.
func (a *Actor) unlockKeywordsLocked(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	phaseID := ""
	if ph := a.m.Current(a.g); ph != nil {
		phaseID = ph.ID
	}
	heard, heardBuilt := "", false
	for i := range a.sc.Clues {
		c := &a.sc.Clues[i]
		if c.OwnerRoleID == "" || len(c.Keywords) == 0 {
			continue
		}
		st := a.g.ClueState[c.ID]
		if st.Unlocked {
			continue
		}
		if !a.parentsUnlockedLocked(c) {
			continue
		}
		seen := text
		if c.KeywordsAll {
			if !heardBuilt {
				heard, heardBuilt = a.publicTextLocked(), true
			}
			seen = heard
		}
		hit := ""
		all := true
		var hits []string
		for _, kw := range c.Keywords {
			if kw == "" {
				continue
			}
			if !strings.Contains(seen, kw) {
				all = false
				continue
			}
			hits = append(hits, kw)
			if hit == "" {
				hit = kw
			}
		}
		if hit == "" || (c.KeywordsAll && !all) {
			continue
		}
		if c.KeywordsAll {
			hit = strings.Join(hits, "、")
		}
		st.ClueID = c.ID
		st.Unlocked = true
		a.g.ClueState[c.ID] = st
		owner := a.g.SeatByRole(c.OwnerRoleID)
		if owner == nil {
			continue
		}
		a.appendLocked(domain.Event{
			Type:       domain.EvClueFound,
			PhaseID:    phaseID,
			SeatID:     owner.ID,
			RoleID:     owner.RoleID,
			ClueID:     c.ID,
			Visibility: SeatVisibility(owner.ID),
			Text:       fmt.Sprintf("关键词「%s」触发了你的记忆碎片【%s】：%s", hit, c.Title, c.Content),
			Meta:       map[string]string{"title": c.Title, "keyword": hit},
		})
	}
	a.persistLocked()
}

// parentsUnlockedLocked reports whether every fragment this one waits for has
// already been opened. Clues without parents are always ready.
func (a *Actor) parentsUnlockedLocked(c *domain.Clue) bool {
	for _, cid := range c.UnlockedBy {
		if !a.g.ClueState[cid].Unlocked {
			return false
		}
	}
	return true
}

// publicTextLocked is everything the table has heard so far: spoken lines and
// the text of clues that were read aloud. It is what a 「说到这两个词」 trigger
// matches against, so the two words need not land in the same sentence.
func (a *Actor) publicTextLocked() string {
	var b strings.Builder
	for _, ev := range a.g.Events {
		if ev.Visibility != domain.VisPublic {
			continue
		}
		switch ev.Type {
		case domain.EvSpeech, domain.EvClueRevealed:
			b.WriteString(ev.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
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
	if a.floor != nil && a.floor.SeatID == seat.ID && a.floor.PhaseID == ph.ID {
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
	a.unlockKeywordsLocked(c.Title + " " + c.Content)
	a.persistLocked()
	a.mu.Unlock()
	return nil
}

// ---- special skills ----

// skillViewLocked tells one seat what its printed ability can do right now.
func (a *Actor) skillViewLocked(seat domain.Seat) *SkillView {
	role := a.m.idx.RoleByID(seat.RoleID)
	if role == nil || role.Skill == nil {
		return nil
	}
	sk := role.Skill
	view := &SkillView{
		ID:        sk.ID,
		Name:      sk.Name,
		Text:      sk.Text,
		Cost:      sk.Cost,
		AllCost:   sk.AllCost,
		ExactCost: sk.ExactCost,
		Target:    sk.Target,
		Used:      a.g.SkillsUsed[seat.ID],
	}
	ph := a.m.Current(a.g)
	mine := a.g.Points[seat.ID]
	switch {
	case view.Used:
		view.Why = "已经用过了"
	case ph == nil || ph.Type != domain.PhaseSearch:
		view.Why = "只能在搜证环节发动"
	case sk.ExactCost > 0 && mine != sk.ExactCost:
		view.Why = fmt.Sprintf("需要正好 %d 根%s（现在 %d 根）", sk.ExactCost, a.pointsLabel(), mine)
	case sk.AllCost && mine <= 0:
		view.Why = "已经没有仙骨可以烧了"
	case !sk.AllCost && mine < sk.Cost:
		view.Why = fmt.Sprintf("需要 %d 根%s（现在 %d 根）", sk.Cost, a.pointsLabel(), mine)
	default:
		view.Ready = true
	}
	return view
}

// useSkill fires the seat's one-shot special ability. Every use is announced:
// seizing draws, burning bones or opening a hand changes what the rest of the
// table can do, so none of it may happen silently.
func (a *Actor) useSkill(seat domain.Seat, targetID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ph := a.m.Current(a.g)
	if ph == nil || ph.Type != domain.PhaseSearch {
		return ErrNotAllowed{Reason: "特殊技能只能在搜证环节发动"}
	}
	role := a.m.idx.RoleByID(seat.RoleID)
	if role == nil || role.Skill == nil {
		return ErrNotAllowed{Reason: "你的角色没有特殊技能"}
	}
	sk := role.Skill
	if a.g.SkillsUsed[seat.ID] {
		return ErrNotAllowed{Reason: fmt.Sprintf("「%s」全程只能发动一次，你已经用过了", sk.Name)}
	}
	mine := a.g.Points[seat.ID]
	cost := sk.Cost
	switch {
	case sk.ExactCost > 0 && mine != sk.ExactCost:
		return ErrNotAllowed{Reason: fmt.Sprintf("「%s」只能在你正好有 %d 根%s时发动（现在 %d 根）", sk.Name, sk.ExactCost, a.pointsLabel(), mine)}
	case sk.ExactCost > 0:
		cost = sk.ExactCost
	case sk.AllCost:
		if mine <= 0 {
			return ErrNotAllowed{Reason: "你已经没有仙骨可以烧了"}
		}
		cost = mine
	case mine < cost:
		return ErrNotAllowed{Reason: fmt.Sprintf("发动「%s」要 %d 根%s，你只有 %d 根", sk.Name, cost, a.pointsLabel(), mine)}
	}
	target, err := a.skillTargetLocked(seat, sk, targetID)
	if err != nil {
		return err
	}

	text := ""
	switch sk.Effect {
	case domain.SkillStealDraws:
		rest := a.picksLeftLocked(*target, ph)
		if rest <= 0 {
			return ErrNotAllowed{Reason: fmt.Sprintf("%s 这一轮已经没有抽卡权可以掠夺了", a.speakerLocked(*target))}
		}
		a.searched[target.ID] = a.searchLimitLocked(*target, ph)
		a.searched[seat.ID] -= rest
		text = fmt.Sprintf("%s 发动「%s」，夺走了 %s 这一轮剩下的 %d 次抽卡权", a.speakerLocked(seat), sk.Name, a.speakerLocked(*target), rest)
	case domain.SkillBurnPoints:
		burned := a.g.Points[target.ID]
		a.g.Points[target.ID] = 0
		text = fmt.Sprintf("%s 发动「%s」，焚尽自身 %d 根%s，毁去了 %s 剩余的 %d 根%s", a.speakerLocked(seat), sk.Name, cost, a.pointsLabel(), a.speakerLocked(*target), burned, a.pointsLabel())
	case domain.SkillShowClues:
		a.skillShowCluesLocked(seat, *target, sk)
		scope := "只给自己看"
		if sk.Public {
			scope = "向全员展示"
		}
		count := "全部"
		if sk.Value > 0 {
			count = fmt.Sprintf("%d 张", sk.Value)
		}
		text = fmt.Sprintf("%s 发动「%s」，要求 %s %s线索卡（%s）", a.speakerLocked(seat), sk.Name, a.speakerLocked(*target), scope, count)
	case domain.SkillGrantPoints:
		a.g.Points[target.ID] += sk.Value
		text = fmt.Sprintf("%s 发动「%s」，%s 恢复 %d 根%s（现 %d 根）", a.speakerLocked(seat), sk.Name, a.speakerLocked(*target), sk.Value, a.pointsLabel(), a.g.Points[target.ID])
	default:
		return ErrNotAllowed{Reason: "这个技能需要按剧本自行结算"}
	}

	a.g.Points[seat.ID] -= cost
	if a.g.SkillsUsed == nil {
		a.g.SkillsUsed = map[string]bool{}
	}
	a.g.SkillsUsed[seat.ID] = true
	a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, SeatID: seat.ID, RoleID: seat.RoleID, Text: text})
	a.persistLocked()
	return nil
}

// skillTargetLocked resolves who a skill points at.
func (a *Actor) skillTargetLocked(seat domain.Seat, sk *domain.Skill, targetID string) (*domain.Seat, error) {
	if sk.TargetRoleID != "" {
		for i := range a.g.Seats {
			s := &a.g.Seats[i]
			if s.RoleID == sk.TargetRoleID && s.Kind != domain.SeatSpectator {
				return s, nil
			}
		}
		return nil, ErrNotAllowed{Reason: "技能目标角色不在房间里"}
	}
	switch sk.Target {
	case "next":
		for i, s := range a.order {
			if s.ID != seat.ID {
				continue
			}
			if i+1 < len(a.order) {
				t := a.order[i+1]
				return &t, nil
			}
			return nil, ErrNotAllowed{Reason: "你是本轮最后一位道友，后面没有人可以掠夺"}
		}
		return nil, ErrNotAllowed{Reason: "本轮顺序里还没有你，不能掠夺下一位"}
	case "other":
		if targetID == "" {
			return nil, ErrNotAllowed{Reason: "这个技能要点明一位道友"}
		}
		t := a.g.SeatByID(targetID)
		if t == nil || t.Kind == domain.SeatSpectator {
			return nil, ErrNotAllowed{Reason: "技能目标不在桌上"}
		}
		if t.ID == seat.ID {
			return nil, ErrNotAllowed{Reason: "这个技能不能对自己用"}
		}
		return t, nil
	}
	return nil, nil
}

// skillShowClues opens the target's hand: Value cards (0 means every card it
// holds) go to the caster alone, or to the whole table when Public is set.
func (a *Actor) skillShowCluesLocked(caster, target domain.Seat, sk *domain.Skill) {
	ph := a.m.Current(a.g)
	phaseID := ""
	if ph != nil {
		phaseID = ph.ID
	}
	var held []string
	for _, c := range a.sc.Clues {
		st := a.g.ClueState[c.ID]
		if st.FoundBySeat == target.ID && !st.Revealed {
			held = append(held, c.ID)
		}
	}
	limit := sk.Value
	if limit <= 0 || limit > len(held) {
		limit = len(held)
	}
	for _, id := range held[:limit] {
		c := a.m.TokenClue(id)
		if c == nil {
			continue
		}
		if sk.Public {
			st := a.g.ClueState[id]
			st.Revealed = true
			st.RevealedBy = target.ID
			st.RevealedAt = time.Now().UTC()
			a.g.ClueState[id] = st
			a.appendLocked(domain.Event{
				Type:    domain.EvClueRevealed,
				PhaseID: phaseID,
				SeatID:  target.ID,
				RoleID:  target.RoleID,
				ClueID:  id,
				Text:    fmt.Sprintf("被迫公开的线索【%s】：%s", c.Title, c.Content),
				Meta:    map[string]string{"title": c.Title, "content": c.Content},
			})
			a.unlockKeywordsLocked(c.Title + " " + c.Content)
			continue
		}
		a.appendLocked(domain.Event{
			Type:       domain.EvClueFound,
			PhaseID:    phaseID,
			SeatID:     target.ID,
			RoleID:     target.RoleID,
			ClueID:     id,
			Visibility: PairVisibility(caster.ID, target.ID),
			Text:       fmt.Sprintf("%s 向你展示了线索【%s】：%s", a.speakerLocked(target), c.Title, c.Content),
			Meta:       map[string]string{"title": c.Title, "content": c.Content},
		})
	}
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
		// Every scripted turn names the pass move, not only the free-talk
		// prompts: players kept waiting out the clock because nothing said the
		// turn could be handed on.
		Text: fmt.Sprintf("轮到你发言（%s）；不想说可以点「不发言」把机会让给别人", limit),
	})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if cur, ok := a.waiters[seat.ID]; ok && cur == ch {
			delete(a.waiters, seat.ID)
		}
		if a.floor != nil && a.floor.SeatID == seat.ID && a.floor.PhaseID == ph.ID {
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
	gone, stopGone := a.goneWatcher(seat.ID)
	defer stopGone()
	for {
		select {
		case <-wctx.Done():
			return false
		case <-gone:
			a.mu.Lock()
			if gen == a.phaseGen && !a.finished {
				a.speech[seat.ID]++
				a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, SeatID: seat.ID, Text: fmt.Sprintf("%s 已离线，跳过其发言", a.speakerLocked(seat))})
				a.persistLocked()
			}
			a.mu.Unlock()
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
			case "speak.pass":
				// The scripted round offers the same "结束发言" move as free
				// talk: declining spends this seat's turn instead of leaving
				// the table on a clock nobody can stop with the button.
				a.mu.Lock()
				if gen == a.phaseGen {
					a.speech[seat.ID]++
				}
				a.mu.Unlock()
				a.declined(seat, SpeechSlot{SeatID: seat.ID, Kind: SlotTurn})
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
func (a *Actor) generate(ctx context.Context, gen int, seat domain.Seat, req llm.Request, visible ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, a.cfg.agent())
	defer cancel()

	guard := NewLeakGuard(a.sc, &seat, visible...)
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

// settleFavorLocked publishes every balance and works out who wears the crown
// afterwards. K12「水落石出」: 如果皇后不是恩宠值最高的，则被废后、打入冷宫，
// 最高者继后；否则恩宠值最低的人被打入冷宫。 Callers hold mu.
func (a *Actor) settleFavorLocked(ph *domain.Phase) {
	a.pointsOpen = true
	type entry struct {
		seat domain.Seat
		pts  int
	}
	var list []entry
	for _, s := range a.g.Seats {
		if s.Kind == domain.SeatSpectator {
			continue
		}
		list = append(list, entry{s, a.g.Points[s.ID]})
	}
	if len(list) == 0 {
		return
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].pts > list[j].pts })
	board := make([]string, 0, len(list))
	for _, it := range list {
		board = append(board, fmt.Sprintf("%s %d", a.speakerLocked(it.seat), it.pts))
	}
	a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: "恩宠值公布：" + strings.Join(board, "，")})
	if ph.ThroneRoleID == "" {
		return
	}
	var throne *entry
	for i := range list {
		if list[i].seat.RoleID == ph.ThroneRoleID {
			throne = &list[i]
			break
		}
	}
	if throne == nil {
		return
	}
	top := list[0]
	lowest := list[len(list)-1]
	var verdict string
	switch {
	case top.seat.RoleID != ph.ThroneRoleID:
		verdict = fmt.Sprintf("结果：%s 的恩宠值最高，%s 被废后、打入冷宫，后位由 %s 继承。",
			a.speakerLocked(top.seat), a.speakerLocked(throne.seat), a.speakerLocked(top.seat))
	case len(list) > 1:
		verdict = fmt.Sprintf("结果：%s 仍是后位，恩宠值最低的 %s 被打入冷宫。",
			a.speakerLocked(throne.seat), a.speakerLocked(lowest.seat))
	default:
		verdict = fmt.Sprintf("结果：%s 仍是后位。", a.speakerLocked(throne.seat))
	}
	a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID, Text: verdict})
}

// votePromptFor is the printed question this vote asks.
func (a *Actor) votePromptFor(ph *domain.Phase) string {
	if ph != nil && strings.TrimSpace(ph.VotePrompt) != "" {
		return ph.VotePrompt
	}
	return "投票阶段：请选择你认为是凶手的角色"
}

// applyVoteEffect moves points after a tally: the most-voted role takes the
// damage or the reward the phase prints (K12 定罪扣恩宠值 / 侍寝加恩宠值).
// Callers hold mu.
func (a *Actor) applyVoteEffectLocked(ph *domain.Phase) {
	if ph == nil || (ph.Penalty == 0 && ph.Reward == 0) || len(a.g.Points) == 0 {
		return
	}
	counts := tally(a.g.Votes)
	best, bestN := "", 0
	for role, n := range counts {
		if role == "" || n <= bestN {
			continue
		}
		best, bestN = role, n
	}
	if best == "" {
		return
	}
	var target *domain.Seat
	for i := range a.g.Seats {
		if a.g.Seats[i].RoleID == best {
			target = &a.g.Seats[i]
			break
		}
	}
	if target == nil {
		return
	}
	name := a.speakerLocked(*target)
	label := a.pointsLabel()
	switch {
	case ph.Penalty > 0:
		a.g.Points[target.ID] -= ph.Penalty
		if a.g.Points[target.ID] < 0 {
			a.g.Points[target.ID] = 0
		}
		a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID,
			Text: fmt.Sprintf("%s 被定罪（%d 票），扣除 %d 点%s，现为 %d", name, bestN, ph.Penalty, label, a.g.Points[target.ID])})
	case ph.Reward > 0:
		a.g.Points[target.ID] += ph.Reward
		a.appendLocked(domain.Event{Type: domain.EvSystem, PhaseID: ph.ID,
			Text: fmt.Sprintf("%s 得 %d 票，获得 %d 点%s，现为 %d", name, bestN, ph.Reward, label, a.g.Points[target.ID])})
	}
}

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
		if a.hasVoted(seat.ID) {
			// An early ballot was already accepted while another seat held the
			// prompt; do not ask this one twice.
			continue
		}
		if seat.Kind == domain.SeatAgent {
			a.agentVote(ctx, gen, seat)
		} else {
			a.awaitVote(ctx, gen, seat)
		}
	}
	a.mu.Lock()
	// Sealing the ballot before the tally keeps a frame that arrives during the
	// tally from changing a result that is already on screen.
	a.voteClosed = true
	a.mu.Unlock()
	a.tallyVotes()
	a.mu.Lock()
	a.applyVoteEffectLocked(ph)
	a.persistLocked()
	a.mu.Unlock()
	a.completePhase(ctx, gen)
}

func (a *Actor) awaitVote(ctx context.Context, gen int, seat domain.Seat) bool {
	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return false
	}
	if _, ok := a.g.Votes[seat.ID]; ok {
		// The ballot was already cast while an earlier seat held the prompt.
		a.mu.Unlock()
		return true
	}
	ch := make(chan ClientMessage, 1)
	wake := a.voteWake
	a.waiters[seat.ID] = ch
	a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, Visibility: SeatVisibility(seat.ID), Text: a.votePromptFor(a.m.Current(a.g))})
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
	gone, stopGone := a.goneWatcher(seat.ID)
	defer stopGone()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-gone:
			a.recordVote(seat, "")
			return false
		case <-expired:
			a.recordVote(seat, "")
			return false
		case <-wake:
			// Somebody cast a ballot (possibly this seat from the sideline).
			// Either way the walk re-checks instead of sleeping out the clock.
			if a.hasVoted(seat.ID) {
				return true
			}
			continue
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
	question := a.votePromptFor(a.m.Current(a.g))
	a.mu.Unlock()

	a.thinking(seat, "start")
	defer a.thinking(seat, "stop")

	instruction := question + "。请只输出一个 JSON 对象：{\"vote\":\"<角色名>\",\"reason\":\"<一句话理由>\"}。vote 必须是本题指向的角色名，不能是自己。"
	req := a.pb.BuildVote(PromptInput{Visible: v, Persona: p, Instruction: instruction})
	text, _, err := a.generate(ctx, gen, seat, req, visibleCorpus(v)...)
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

func (a *Actor) hasVoted(seatID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.g.Votes[seatID]
	return ok
}

// castVote accepts a ballot from any seated player at any point of the vote
// phase. The sequential walk still prompts each seat in turn, but it is only a
// nudge: the table can fill the ballot in whatever order people click.
func (a *Actor) castVote(seat domain.Seat, roleID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return ErrNotAllowed{Reason: "本局已结束"}
	}
	ph := a.m.Current(a.g)
	if ph == nil || ph.Type != domain.PhaseVote {
		return ErrNotAllowed{Reason: "现在不是投票阶段"}
	}
	if a.phaseDone || a.voteClosed {
		return ErrNotAllowed{Reason: "投票已结束"}
	}
	if seat.Kind == domain.SeatSpectator {
		return ErrNotAllowed{Reason: "观战者不能投票"}
	}
	if _, ok := a.g.Votes[seat.ID]; ok {
		return nil
	}
	if roleID != "" {
		if roleID == seat.RoleID {
			return ErrNotAllowed{Reason: "不能投给自己"}
		}
		if a.sc.Tokenize().RoleByID(roleID) == nil {
			return ErrNotAllowed{Reason: "投票目标不存在"}
		}
	}
	a.recordVoteLocked(seat, roleID)
	// A non-blocking nudge: the vote waiter may not exist yet, and the ballot is
	// already stored either way.
	select {
	case a.voteWake <- struct{}{}:
	default:
	}
	return nil
}

func (a *Actor) recordVote(seat domain.Seat, roleID string) {
	a.mu.Lock()
	a.recordVoteLocked(seat, roleID)
	a.mu.Unlock()
}

func (a *Actor) recordVoteLocked(seat domain.Seat, roleID string) {
	if _, ok := a.g.Votes[seat.ID]; ok {
		return
	}
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
	phaseID := ""
	if ph := a.m.Current(a.g); ph != nil {
		phaseID = ph.ID
	}
	a.appendLocked(domain.Event{Type: domain.EvVote, PhaseID: phaseID, SeatID: seat.ID, RoleID: roleID, Text: text})
	a.persistLocked()
}

func (a *Actor) tallyVotes() {
	a.mu.Lock()
	counts := tally(a.g.Votes)
	abstain := counts[""]
	delete(counts, "")
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
		if r := idx.RoleByID(it.role); r != nil {
			name = r.Name
		}
		parts = append(parts, fmt.Sprintf("%s %d 票", name, it.n))
	}
	text := ""
	switch {
	case len(parts) == 0 && abstain > 0:
		text = fmt.Sprintf("投票结束：全部弃权（%d 票）", abstain)
	case len(parts) == 0:
		text = "投票结束：无人投票"
	default:
		text = "投票结束：" + strings.Join(parts, "，")
		if abstain > 0 {
			// Abstention is not a candidate: mixing it into the ranked names
			// made the tally read as if somebody called 弃权 had been voted for.
			text += fmt.Sprintf("；弃权 %d 票", abstain)
		}
	}
	phaseID, round := "", 0
	if ph := a.m.Current(a.g); ph != nil {
		phaseID, round = ph.ID, ph.Round
	}
	votes := make(map[string]string, len(a.g.Votes))
	for seatID, roleID := range a.g.Votes {
		votes[seatID] = roleID
	}
	roundCounts := make(map[string]int, len(counts)+1)
	for role, n := range counts {
		roundCounts[role] = n
	}
	if abstain > 0 {
		roundCounts[""] = abstain
	}
	roundRecord := domain.VoteRound{
		PhaseID: phaseID,
		Round:   round,
		Votes:   votes,
		Counts:  roundCounts,
		At:      time.Now().UTC(),
	}
	if n := len(a.g.VoteRounds); n > 0 && a.g.VoteRounds[n-1].PhaseID == phaseID {
		a.g.VoteRounds[n-1] = roundRecord
	} else {
		a.g.VoteRounds = append(a.g.VoteRounds, roundRecord)
	}
	a.appendLocked(domain.Event{Type: domain.EvVoteResult, PhaseID: phaseID, Text: text})
	a.persistLocked()
	a.mu.Unlock()
}

// ---- reveal ----

func (a *Actor) runReveal() {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := a.sc.Tokenize()
	killers := a.sc.Truth.Killers()
	summary := a.sc.Truth.Summary
	if summary == "" {
		summary = a.sc.Truth.Method
	}
	// 空 killerRoleId 表示真凶不是在场玩家（K18 的食堂帮工阿飞）。此时真相
	// 只能由 summary 交代，不能再拼一句「凶手是 」。
	text := "真相揭晓：" + summary
	if len(killers) == 1 {
		killerName := killers[0]
		if r := idx.RoleByID(killers[0]); r != nil {
			killerName = r.Name
		}
		text = fmt.Sprintf("真相揭晓：凶手是 %s。%s", killerName, summary)
	} else if len(killers) > 1 {
		// 多案本（K27 女帝攻略的三桩命案各有一个真凶）：只念一个人的名字会让
		// 投对了另一案的玩家以为自己错了。
		names := make([]string, 0, len(killers))
		for _, id := range killers {
			name := id
			if r := idx.RoleByID(id); r != nil {
				name = r.Name
			}
			names = append(names, name)
		}
		text = fmt.Sprintf("真相揭晓：本案共有 %d 名凶手——%s。%s", len(names), strings.Join(names, "、"), summary)
	}
	meta := map[string]string{}
	if len(killers) > 0 {
		meta["killerRoleId"] = killers[0]
		meta["killerRoleIds"] = strings.Join(killers, ",")
	}
	a.appendLocked(domain.Event{
		Type: domain.EvReveal,
		Text: text,
		Meta: meta,
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
	a.unlockKeywordsLocked(text)
	if replyTo != "" && replyTo != seat.ID {
		a.enqueueTalkLocked(SpeechSlot{SeatID: replyTo, Kind: SlotReply, ReplyTo: seat.ID}, true)
	}
	a.queueWantsLocked(seat.ID, text)
	a.persistLocked()
	a.mu.Unlock()
	a.wakeTalk()
}

func (a *Actor) speakerLocked(seat domain.Seat) string {
	// A card game keeps the assignment secret: the transcript may say「3 号位」
	// but never「甄会导」until the reveal.
	if HiddenIdentities(a.g, a.sc) {
		return seat.DisplayName
	}
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
	stored := a.g.AppendEvent(ev)
	if a.hub != nil {
		// 卡牌本在翻真相前不广播 roleId：积分卡要让玩家自己认人，而
		// seats[] 里已经清过一次，事件里再带一份就把答案送出去了。存的副本保留，
		// 复盘阶段仍能还原。
		if HiddenIdentities(a.g, a.sc) {
			stored.RoleID = ""
		}
		a.hub.Publish(stored)
	}
	return stored
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
	// The store takes its own copy (PutGame clones), so marshalling the whole
	// game here as well only doubled the serialisation cost of every event.
	a.sink.PutGame(a.g)
}

// snapshotLocked deep-copies the game for callers that need a detached value.
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
