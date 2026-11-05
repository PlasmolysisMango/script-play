// Package domain holds the data model shared by the engine, storage and API.
//
// The script schema mirrors docs/script.schema.json. Everything an AI seat may
// never observe (Truth, other seats' Private blocks, unrevealed clues) lives in
// this package but is only reachable through engine.VisibleFor.
package domain

import "time"

// Phase types supported by the state machine.
const (
	// PhaseReading is the opening beat every script starts with: each seat
	// reads its own role book, nothing is collected, the host moves on.
	PhaseReading    = "reading"
	PhaseOpening    = "opening"
	PhaseSearch     = "search"
	PhaseDiscussion = "discussion"
	PhaseVote       = "vote"
	PhaseReveal     = "reveal"
)

// Turn order policies. Any phase that hands the table one turn at a time uses
// one of these; the vote phase is simultaneous and ignores it.
const (
	// OrderRoundRobin walks the seats in join order, host first.
	OrderRoundRobin = "round_robin"
	// OrderRandom shuffles the seats once when the phase starts and records the
	// resulting order in the log, so a replay still reads in the played order.
	OrderRandom = "random"
	// OrderHostAssigned leaves the order to the host, who lays out the whole
	// round on one screen before anybody acts.
	OrderHostAssigned = "host_assigned"
)

// Search modes: how a seat obtains its clues in a search phase.
const (
	// SearchPick shows the seat the list of clues it may still take.
	SearchPick = "pick"
	// SearchRandom hides the list: the seat presses "draw" and the server deals
	// one clue uniformly among the ones still available to it.
	SearchRandom = "random"
)

// Script is one published script version.
type Script struct {
	SchemaVersion int     `json:"schemaVersion"`
	Meta          Meta    `json:"meta"`
	Truth         Truth   `json:"truth"`
	Phases        []Phase `json:"phases"`
	Roles         []Role  `json:"roles"`
	Clues         []Clue  `json:"clues"`
	Facts         []Fact  `json:"facts,omitempty"`
}

// Meta describes a script for the lobby listing.
type Meta struct {
	Title       string     `json:"title"`
	Author      string     `json:"author,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Difficulty  string     `json:"difficulty,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	PlayerCount CountRange `json:"playerCount"`
	DurationMin int        `json:"durationMin,omitempty"`
}

// CountRange is the allowed number of player seats.
type CountRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Truth is the solution of the case. It must never reach an agent prompt.
type Truth struct {
	KillerRoleID string   `json:"killerRoleId"`
	Method       string   `json:"method,omitempty"`
	Motive       string   `json:"motive,omitempty"`
	Timeline     []string `json:"timeline,omitempty"`
	Summary      string   `json:"summary,omitempty"`
}

// Phase is one step of the game flow.
type Phase struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Name            string   `json:"name"`
	Round           int      `json:"round,omitempty"`
	DMScript        string   `json:"dmScript,omitempty"`
	CluePool        []string `json:"cluePool,omitempty"`
	PerPlayerPick   int      `json:"perPlayerPick,omitempty"`
	SpeechPerPlayer int      `json:"speechPerPlayer,omitempty"`
	// Order is how search/discussion hand out turns; SearchMode is how a search
	// turn hands out clues (pick or random). Vote ignores both.
	Order      string `json:"order,omitempty"`
	SearchMode string `json:"searchMode,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// Role is a playable character.
type Role struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	PersonaID string      `json:"personaId,omitempty"`
	Public    RolePublic  `json:"public"`
	Private   RolePrivate `json:"private"`
}

// RolePublic is visible to every seat from the start.
type RolePublic struct {
	Intro      string `json:"intro,omitempty"`
	Appearance string `json:"appearance,omitempty"`
}

// RolePrivate is only ever visible to the seat that plays the role.
type RolePrivate struct {
	Script       string   `json:"script"`
	Secrets      []Secret `json:"secrets,omitempty"`
	Goals        []string `json:"goals,omitempty"`
	KnownFactIDs []string `json:"knownFactIds,omitempty"`
}

// Secret is a hidden fact the role may choose to reveal (or not).
type Secret struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	RevealCost float64 `json:"revealCost"`
}

// Clue is a searchable piece of evidence.
type Clue struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
	// Back is the text printed on the back of the physical clue card. The table
	// reads it while searching, so it names a place ("背后的培养仓") and never
	// summarises what is there — that is what Content is for, and Content only
	// becomes visible once the seat owns the clue.
	Back        string    `json:"back,omitempty"`
	Content     string    `json:"content"`
	OwnerRoleID string    `json:"ownerRoleId,omitempty"`
	IsPublic    bool      `json:"isPublic"`
	ImageURL    string    `json:"imageUrl,omitempty"`
	Links       ClueLinks `json:"links,omitempty"`
}

// ClueLinks connects a clue to roles, secrets and facts.
type ClueLinks struct {
	Roles   []string `json:"roles,omitempty"`
	Secrets []string `json:"secrets,omitempty"`
	Facts   []string `json:"facts,omitempty"`
}

// Fact is a neutral, citable statement used by prompts and leak detection.
type Fact struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Tokenize returns a lookup table for the ids defined by the script.
func (s *Script) Tokenize() ScriptIndex {
	idx := ScriptIndex{
		Roles:   map[string]*Role{},
		Clues:   map[string]*Clue{},
		Facts:   map[string]*Fact{},
		Secrets: map[string]*Secret{},
	}
	for i := range s.Roles {
		idx.Roles[s.Roles[i].ID] = &s.Roles[i]
		for j := range s.Roles[i].Private.Secrets {
			sec := &s.Roles[i].Private.Secrets[j]
			idx.Secrets[sec.ID] = sec
		}
	}
	for i := range s.Clues {
		idx.Clues[s.Clues[i].ID] = &s.Clues[i]
	}
	for i := range s.Facts {
		idx.Facts[s.Facts[i].ID] = &s.Facts[i]
	}
	return idx
}

// ScriptIndex is an id -> object lookup built per game.
type ScriptIndex struct {
	Roles   map[string]*Role
	Clues   map[string]*Clue
	Facts   map[string]*Fact
	Secrets map[string]*Secret
}

// RoleByID returns the role or nil.
func (i ScriptIndex) RoleByID(id string) *Role { return i.Roles[id] }

// ClueByID returns the clue or nil.
func (i ScriptIndex) ClueByID(id string) *Clue { return i.Clues[id] }

// PhaseByID returns the phase or nil.
func PhaseByID(s *Script, id string) *Phase {
	for i := range s.Phases {
		if s.Phases[i].ID == id {
			return &s.Phases[i]
		}
	}
	return nil
}

// ScriptRecord is a stored script together with its editorial metadata.
type ScriptRecord struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"ownerUserId"`
	Status      string    `json:"status"` // draft | published
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Source      string    `json:"source"` // json | sample
	Body        *Script   `json:"body"`
	// RawSource keeps the text a draft was created from, when there is one.
	RawSource string   `json:"rawSource,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}
