package engine

import (
	"strings"
	"time"

	"script-play/internal/domain"
)

// TranscriptLine is one public line an agent or human may be shown.
type TranscriptLine struct {
	Seq     int       `json:"seq"`
	Kind    string    `json:"kind"` // dm | speech | system | reveal
	SeatID  string    `json:"seatId,omitempty"`
	RoleID  string    `json:"roleId,omitempty"`
	Speaker string    `json:"speaker,omitempty"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
}

// Visible is the complete information set for one seat. It is the only type
// that prompt building and websocket delivery may consume: if a field is not
// here, the seat cannot reach it.
type Visible struct {
	GameID     string       `json:"gameId"`
	RoomID     string       `json:"roomId"`
	Phase      domain.Phase `json:"phase"`
	PhaseIndex int          `json:"phaseIndex"`
	Round      int          `json:"round"`
	// Seat is the live seat used by the engine (model, persona, user). It is
	// never serialised so seat configuration cannot reach a client.
	Seat domain.Seat `json:"-"`
	// Self is the safe projection of the seat the viewer occupies.
	Self        PublicSeat          `json:"self"`
	OwnRole     *domain.Role        `json:"ownRole,omitempty"`
	PublicRoles []domain.RolePublic `json:"publicRoles"`
	OwnClues    []domain.Clue       `json:"ownClues"`
	PublicClues []domain.Clue       `json:"publicClues"`
	OwnFacts    []domain.Fact       `json:"ownFacts"`
	Transcript  []TranscriptLine    `json:"transcript"`
	// Recap is the compressed history of everything up to RecapUpto. Both are
	// prompt-only: the client still receives the full transcript, because a
	// human can scroll back and an agent cannot.
	Recap     string `json:"-"`
	RecapUpto int    `json:"-"`
	// Truth is only populated for human-facing views during the reveal phase.
	Truth *domain.Truth `json:"truth,omitempty"`
	// Seats is the public player list (no model ids, no persona numbers).
	Seats []PublicSeat `json:"seats"`
}

// PublicSeat is the safe projection of a seat.
type PublicSeat struct {
	ID          string `json:"id"`
	RoleID      string `json:"roleId,omitempty"`
	RoleName    string `json:"roleName,omitempty"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
	IsHost      bool   `json:"isHost"`
	Connected   bool   `json:"connected"`
	Ready       bool   `json:"ready"`
}

// VisibleOptions tunes what the caller needs.
type VisibleOptions struct {
	// IncludeTruth allows the revealed truth for human-facing views.
	IncludeTruth bool
	// MaxTranscript truncates the transcript (0 = unlimited) keeping the tail.
	MaxTranscript int
}

// VisibleFor builds the information set of one seat.
//
// Rules implemented here are the whole answer to "an agent must not know more
// than its own script plus what is public":
//   - role.Private only for the seat playing that role,
//   - a clue with OwnerRoleID only for that role until revealed,
//   - a clue with IsPublic true for everyone,
//   - any other clue only for the seat that found it, until revealed,
//   - truth never, except human reveal views,
//   - seat configuration (persona numbers, model ids) never.
func VisibleFor(g *domain.Game, sc *domain.Script, seat *domain.Seat, opt VisibleOptions) Visible {
	idx := sc.Tokenize()
	v := Visible{
		GameID:      g.ID,
		RoomID:      g.RoomID,
		PhaseIndex:  g.PhaseIndex,
		Round:       g.Round,
		PublicRoles: make([]domain.RolePublic, 0, len(sc.Roles)),
		Seats:       make([]PublicSeat, 0, len(g.Seats)),
		// Empty slices, never nil: `null` would trip every strict JSON client.
		OwnClues:    []domain.Clue{},
		PublicClues: []domain.Clue{},
		OwnFacts:    []domain.Fact{},
		Transcript:  []TranscriptLine{},
	}
	if seat != nil {
		v.Seat = *seat
		v.Self = PublicSeat{
			ID:          seat.ID,
			RoleID:      seat.RoleID,
			DisplayName: seat.DisplayName,
			Kind:        seat.Kind,
			IsHost:      seat.IsHost,
			Connected:   seat.Connected,
			Ready:       seat.Ready,
		}
	}
	if g.PhaseIndex >= 0 && g.PhaseIndex < len(sc.Phases) {
		v.Phase = sc.Phases[g.PhaseIndex]
		if v.Round == 0 {
			v.Round = v.Phase.Round
		}
	}

	for _, r := range sc.Roles {
		v.PublicRoles = append(v.PublicRoles, r.Public)
	}
	for _, s := range g.Seats {
		ps := PublicSeat{
			ID:          s.ID,
			RoleID:      s.RoleID,
			DisplayName: s.DisplayName,
			Kind:        s.Kind,
			IsHost:      s.IsHost,
			Connected:   s.Connected,
			Ready:       s.Ready,
		}
		if r := idx.RoleByID(s.RoleID); r != nil {
			ps.RoleName = r.Name
		}
		v.Seats = append(v.Seats, ps)
	}

	if seat != nil && seat.RoleID != "" {
		if r := idx.RoleByID(seat.RoleID); r != nil {
			v.Self.RoleName = r.Name
			cp := *r
			v.OwnRole = &cp
			for _, fid := range r.Private.KnownFactIDs {
				if f := idx.Facts[fid]; f != nil {
					addFact(&v, *f)
				}
			}
		}
	}

	ownSeatID := ""
	if seat != nil {
		ownSeatID = seat.ID
	}
	for _, c := range sc.Clues {
		st := g.ClueState[c.ID]
		switch {
		case c.IsPublic:
			v.PublicClues = append(v.PublicClues, c)
			appendFactLinks(&v, idx, c)
		case c.OwnerRoleID != "" && seat != nil && c.OwnerRoleID == seat.RoleID:
			v.OwnClues = append(v.OwnClues, c)
			appendFactLinks(&v, idx, c)
		case st.Revealed:
			v.PublicClues = append(v.PublicClues, c)
			appendFactLinks(&v, idx, c)
		case st.FoundBySeat != "" && st.FoundBySeat == ownSeatID:
			v.OwnClues = append(v.OwnClues, c)
			appendFactLinks(&v, idx, c)
		}
	}

	for _, ev := range g.Events {
		if !EventVisibleTo(ev, seat) {
			continue
		}
		switch ev.Type {
		case domain.EvDM, domain.EvSpeech, domain.EvSystem, domain.EvReveal, domain.EvPhase, domain.EvClueRevealed:
		default:
			continue
		}
		v.Transcript = append(v.Transcript, TranscriptLine{
			Seq:     ev.Seq,
			Kind:    ev.Type,
			SeatID:  ev.SeatID,
			RoleID:  ev.RoleID,
			Speaker: ev.Speaker,
			Text:    ev.Text,
			At:      ev.At,
		})
	}
	if opt.MaxTranscript > 0 && len(v.Transcript) > opt.MaxTranscript {
		v.Transcript = v.Transcript[len(v.Transcript)-opt.MaxTranscript:]
	}
	if g.Recap != nil && strings.TrimSpace(g.Recap.Text) != "" {
		v.Recap = g.Recap.Text
		v.RecapUpto = g.Recap.UptoSeq
	}

	if opt.IncludeTruth && v.Phase.Type == domain.PhaseReveal {
		t := sc.Truth
		v.Truth = &t
	}
	return v
}

func appendFactLinks(v *Visible, idx domain.ScriptIndex, c domain.Clue) {
	for _, fid := range c.Links.Facts {
		if f := idx.Facts[fid]; f != nil {
			addFact(v, *f)
		}
	}
}

// addFact appends a fact once: a fact reachable from several clues must not be
// listed twice
func addFact(v *Visible, f domain.Fact) {
	for _, existing := range v.OwnFacts {
		if existing.ID == f.ID {
			return
		}
	}
	v.OwnFacts = append(v.OwnFacts, f)
}

// EventVisibleTo reports whether one seat may receive an event. Spectators only
// see public events.
func EventVisibleTo(ev domain.Event, seat *domain.Seat) bool {
	switch ev.Visibility {
	case "", domain.VisPublic:
		return true
	case domain.VisDM:
		return seat != nil && seat.IsHost
	default:
		if strings.HasPrefix(ev.Visibility, domain.VisSeatPrefix) {
			if seat == nil {
				return false
			}
			return strings.TrimPrefix(ev.Visibility, domain.VisSeatPrefix) == seat.ID
		}
		return false
	}
}

// SeatVisibility builds the visibility string for a private event.
func SeatVisibility(seatID string) string { return domain.VisSeatPrefix + seatID }
