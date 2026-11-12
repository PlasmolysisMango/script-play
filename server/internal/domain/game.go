package domain

import "time"

// Seat kinds.
const (
	SeatHuman     = "human"
	SeatAgent     = "agent"
	SeatSpectator = "spectator"
)

// Event visibility scopes.
const (
	// VisPublic is delivered to every connected client of the room.
	VisPublic = "public"
	// VisSeatPrefix restricts an event to one seat: "seat:<seatID>".
	VisSeatPrefix = "seat:"
	// VisPairPrefix restricts an event to two seats: "pair:<seatA>,<seatB>".
	// A whisper is the only user today — the sender and the listener, nobody
	// else, not even the host.
	VisPairPrefix = "pair:"
	// VisDM restricts an event to the host seat.
	VisDM = "dm"
)

// Room statuses.
const (
	RoomLobby    = "lobby"
	RoomRunning  = "running"
	RoomFinished = "finished"
)

// Account roles. An empty role means a normal account, so users stored before
// the admin console existed keep working without a migration.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// User is a lightweight local account.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName"`
	PasswordHash string    `json:"passwordHash"`
	Role         string    `json:"role,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	// LastLoginAt is best effort: it is only updated when the session is
	// issued, which is what an admin needs to spot dormant accounts.
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
}

// IsAdmin reports whether the account may use the admin console. Importing and
// editing scripts is restricted to admins, so this is the single check every
// restricted route must use.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Room is a lobby that hosts exactly one game at a time.
type Room struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	HostUserID     string `json:"hostUserId"`
	ScriptID       string `json:"scriptId"`
	Status         string `json:"status"`
	MaxSeats       int    `json:"maxSeats"`
	AllowSpectator bool   `json:"allowSpectator"`
	// SpeechSeconds is the per-turn limit chosen when the room was created:
	// 0 means "use the server default", -1 means "no limit at all", any other
	// value is the number of seconds a seat gets for one turn.
	SpeechSeconds int `json:"speechSeconds,omitempty"`
	// WaitForHost keeps the speech floor open past the deadline and leaves the
	// phase to the host instead of skipping the seat.
	WaitForHost  bool      `json:"waitForHost,omitempty"`
	JoinPassword string    `json:"joinPassword,omitempty"`
	GameID       string    `json:"gameId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Seat binds a connection identity to a role. Speaker identity always comes
// from the seat, never from text inference.
type Seat struct {
	ID          string    `json:"id"`
	RoomID      string    `json:"roomId"`
	RoleID      string    `json:"roleId,omitempty"`
	Kind        string    `json:"kind"`
	UserID      string    `json:"userId,omitempty"`
	DisplayName string    `json:"displayName"`
	PersonaID   string    `json:"personaId"`
	ModelID     string    `json:"modelId,omitempty"`
	IsHost      bool      `json:"isHost"`
	Connected   bool      `json:"connected"`
	Ready       bool      `json:"ready"`
	JoinedAt    time.Time `json:"joinedAt"`
}

// ClueState tracks who found a clue and whether it went public.
type ClueState struct {
	ClueID       string    `json:"clueId"`
	FoundBySeat  string    `json:"foundBySeat,omitempty"`
	RevealedBy   string    `json:"revealedBy,omitempty"`
	Revealed     bool      `json:"revealed"`
	FoundAt      time.Time `json:"foundAt,omitempty"`
	RevealedAt   time.Time `json:"revealedAt,omitempty"`
	PrivateOwner string    `json:"privateOwner,omitempty"`
	// Unlocked is set once a keyword-triggered private clue has fired. A
	// private clue with keywords stays sealed (invisible even to its owner)
	// until somebody says one of them out loud.
	Unlocked bool `json:"unlocked,omitempty"`
}

// Event is an append-only, visibility-scoped game log entry.
type Event struct {
	Seq        int               `json:"seq"`
	GameID     string            `json:"gameId"`
	At         time.Time         `json:"at"`
	Type       string            `json:"type"`
	PhaseID    string            `json:"phaseId,omitempty"`
	SeatID     string            `json:"seatId,omitempty"`
	RoleID     string            `json:"roleId,omitempty"`
	Speaker    string            `json:"speaker,omitempty"`
	Visibility string            `json:"visibility"`
	Text       string            `json:"text,omitempty"`
	ClueID     string            `json:"clueId,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// Event types.
const (
	EvSystem = "system"
	EvPhase  = "phase"
	EvDM     = "dm"
	EvSpeech = "speech"
	// EvWhisper is a private line between two seats. It is stored like any other
	// line (the replay needs it) but never reaches the table, the recap, or the
	// keyword gate.
	EvWhisper    = "whisper"
	EvAgentDelta = "agent.delta"
	// EvThinking marks an AI seat that is composing an answer. It is transient
	// like EvAgentDelta (published, never stored) and Meta["state"] is either
	// "start" or "stop", so the table can show "某某正在思考…" with an animation.
	EvThinking     = "agent.thinking"
	EvFloor        = "floor"
	EvClueFound    = "clue.found"
	EvClueRevealed = "clue.revealed"
	EvVote         = "vote"
	EvVoteResult   = "vote.result"
	EvReveal       = "reveal"
	EvLeak         = "leak_incident"
	EvSeat         = "seat.updated"
)

// VoteAbsentPrefix marks a ballot aimed at somebody who is not seated, e.g.
// the NPC culprit of K18 谁与争疯. The rest of the string is the typed name.
// It is stored in the same seatID -> target maps as a role id, so a tally and
// a replay can count it without a second kind of ballot.
const VoteAbsentPrefix = "absent:"

// VoteRound is one sealed ballot. PhaseID ties it to the printed question, so
// a multi-case script keeps its per-round result instead of only the last one.
type VoteRound struct {
	PhaseID string            `json:"phaseId"`
	Round   int               `json:"round,omitempty"`
	Votes   map[string]string `json:"votes"` // seatID -> roleID ("" = abstain)
	Counts  map[string]int    `json:"counts"`
	At      time.Time         `json:"at"`
}

// Game holds the authoritative state of one match. Truth lives in Script, not
// here, so nothing in the game log can leak the solution accidentally.
type Game struct {
	ID         string               `json:"id"`
	RoomID     string               `json:"roomId"`
	ScriptID   string               `json:"scriptId"`
	Status     string               `json:"status"` // lobby | running | finished
	PhaseIndex int                  `json:"phaseIndex"`
	Round      int                  `json:"round"`
	Seats      []Seat               `json:"seats"`
	ClueState  map[string]ClueState `json:"clueState"`
	Votes      map[string]string    `json:"votes"` // seatID -> roleID
	// VoteRounds keeps every ballot, in order. Votes alone is the live round:
	// resetting it per phase is what made a seven-round script look like only
	// its last five players had voted.
	VoteRounds []VoteRound `json:"voteRounds,omitempty"`
	NextSeq    int         `json:"nextSeq"`
	Events     []Event     `json:"events"`
	// Items is the per-seat inventory: seatID -> item names. Items only gate
	// which clue a seat may search; they never travel into a prompt as prose.
	Items map[string][]string `json:"items,omitempty"`
	// Points is the live action-currency balance per seat (K6's 仙骨). It lives
	// on the game rather than the actor so a restart keeps the table's bones.
	Points map[string]int `json:"points,omitempty"`
	// SkillsUsed records the seats whose one-shot special ability already fired.
	SkillsUsed map[string]bool `json:"skillsUsed,omitempty"`
	// Recap is the compressed retelling of everything public up to UptoSeq.
	// It is what keeps a long argument from crowding the role book out of the
	// prompt; nil means nothing has been compressed yet.
	Recap     *Recap     `json:"recap,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// Cards is the live card table of a card-game script: hands, pile and the
	// score sheets. Nil for every classic script.
	Cards *CardState `json:"cards,omitempty"`
}

// Recap is a compressed history of the public log, written once per phase.
//
// UptoSeq is the anchor: every event at or before it is already folded into
// Text, so the prompt may drop those lines instead of paying for them twice.
// Compression is only ever applied to public material — a recap is readable by
// every seat, exactly like the log it replaces.
type Recap struct {
	Text    string    `json:"text"`
	UptoSeq int       `json:"uptoSeq"`
	PhaseID string    `json:"phaseId,omitempty"`
	At      time.Time `json:"at"`
}

// NewGame builds an empty game for a room.
func NewGame(id, roomID, scriptID string, seats []Seat) *Game {
	return &Game{
		ID:        id,
		RoomID:    roomID,
		ScriptID:  scriptID,
		Status:    RoomLobby,
		Seats:     seats,
		ClueState: map[string]ClueState{},
		Votes:     map[string]string{},
		Items:     map[string][]string{},
	}
}

// GrantItem puts an item in a seat's inventory. It reports whether the seat
// did not have it already, so the caller can log the pickup exactly once.
func (g *Game) GrantItem(seatID, item string) bool {
	if seatID == "" || item == "" {
		return false
	}
	for _, got := range g.Items[seatID] {
		if got == item {
			return false
		}
	}
	if g.Items == nil {
		g.Items = map[string][]string{}
	}
	g.Items[seatID] = append(g.Items[seatID], item)
	return true
}

// HasItems reports whether the seat holds every named item.
func (g *Game) HasItems(seatID string, items []string) bool {
	if len(items) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, it := range g.Items[seatID] {
		have[it] = true
	}
	for _, want := range items {
		if want != "" && !have[want] {
			return false
		}
	}
	return true
}

// SeatByID returns a pointer to the live seat.
func (g *Game) SeatByID(id string) *Seat {
	for i := range g.Seats {
		if g.Seats[i].ID == id {
			return &g.Seats[i]
		}
	}
	return nil
}

// SeatByRole returns the seat playing a role.
func (g *Game) SeatByRole(roleID string) *Seat {
	for i := range g.Seats {
		if g.Seats[i].RoleID == roleID {
			return &g.Seats[i]
		}
	}
	return nil
}

// PlayerSeats returns human and agent seats (no spectators).
func (g *Game) PlayerSeats() []Seat {
	out := make([]Seat, 0, len(g.Seats))
	for _, s := range g.Seats {
		if s.Kind != SeatSpectator {
			out = append(out, s)
		}
	}
	return out
}

// AppendEvent adds an event, assigns the sequence number and appends to the log.
func (g *Game) AppendEvent(ev Event) Event {
	g.NextSeq++
	ev.Seq = g.NextSeq
	ev.GameID = g.ID
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Visibility == "" {
		ev.Visibility = VisPublic
	}
	g.Events = append(g.Events, ev)
	return ev
}

// RawMaterial is one uploaded original — a zip of the whole script, a docx, a
// pile of photos. Nothing is parsed: the file is stored untouched under
// data/inbox so a developer can convert it into a standard script JSON offline.
type RawMaterial struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	MIME       string    `json:"mime,omitempty"`
	Size       int64     `json:"size,omitempty"`
	UploadedBy string    `json:"uploadedBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}
