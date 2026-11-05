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
	EvSystem     = "system"
	EvPhase      = "phase"
	EvDM         = "dm"
	EvSpeech     = "speech"
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
	NextSeq    int                  `json:"nextSeq"`
	Events     []Event              `json:"events"`
	StartedAt  *time.Time           `json:"startedAt,omitempty"`
	EndedAt    *time.Time           `json:"endedAt,omitempty"`
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
	}
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
