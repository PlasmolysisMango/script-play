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
	// Items is the seat's inventory (钳子/小铲子…). Public within the seat only:
	// who holds what is table talk, not server data.
	Items      []string         `json:"items"`
	Transcript []TranscriptLine `json:"transcript"`
	// Recap is the compressed history of everything up to RecapUpto. Both are
	// prompt-only: the client still receives the full transcript, because a
	// human can scroll back and an agent cannot.
	Recap     string `json:"-"`
	RecapUpto int    `json:"-"`
	// Truth is only populated for human-facing views during the reveal phase.
	Truth *domain.Truth `json:"truth,omitempty"`
	// CardRoles is the printed roster of a card game (A 谈资源 / B 余八卦 …).
	// The box lid lists every character, so the names are public; the seat that
	// holds one stays secret until the reveal.
	CardRoles []PublicRoleCard `json:"cardRoles,omitempty"`
	// Seats is the public player list (no model ids, no persona numbers).
	Seats []PublicSeat `json:"seats"`
}

// PublicRoleCard is one line of the printed roster of a card game.
type PublicRoleCard struct {
	ID     string `json:"id"`
	Letter string `json:"letter,omitempty"`
	Name   string `json:"name"`
	Intro  string `json:"intro,omitempty"`
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

// HiddenIdentities reports whether seat↔role assignments must stay secret.
//
// A card game hands every player one printed role face down and then pays
// points for naming everybody else's, so the public seat list may only carry a
// display name from the deal until the reveal phase opens the box.
func HiddenIdentities(g *domain.Game, sc *domain.Script) bool {
	if sc.Mode != domain.ModeCardGame || g.Status == domain.RoomFinished {
		return false
	}
	if g.PhaseIndex >= 0 && g.PhaseIndex < len(sc.Phases) {
		return sc.Phases[g.PhaseIndex].Type != domain.PhaseReveal
	}
	return true
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
	hidden := HiddenIdentities(g, sc)
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
		Items:       []string{},
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
	if sc.Mode == domain.ModeCardGame {
		for _, r := range sc.Roles {
			v.CardRoles = append(v.CardRoles, PublicRoleCard{ID: r.ID, Letter: r.Letter, Name: r.Name, Intro: r.Public.Intro})
		}
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
		if r := idx.RoleByID(s.RoleID); r != nil && (!hidden || (seat != nil && seat.ID == s.ID)) {
			ps.RoleName = r.Name
		}
		// In a card game the assignment itself is the secret: a seat list that
		// carries roleId would answer the score sheet for free.
		if hidden && (seat == nil || seat.ID != s.ID) {
			ps.RoleID = ""
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
	if seat != nil {
		if items := g.Items[seat.ID]; len(items) > 0 {
			v.Items = append(v.Items, items...)
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
			// A keyword-gated fragment stays sealed (even for its owner) until the
			// table says one of its keywords.
			if sealedClue(c, st) {
				continue
			}
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
		case domain.EvDM, domain.EvSpeech, domain.EvWhisper, domain.EvSystem, domain.EvReveal, domain.EvPhase, domain.EvClueRevealed:
		default:
			continue
		}
		// 卡牌本里 roleId 就是积分卡的答案：真相翻开前，连自己的发言行也
		// 不带它——speaker 已经写着座位名 / 角色名，前端不需要再猜。
		lineRole := ev.RoleID
		if hidden {
			lineRole = ""
		}
		v.Transcript = append(v.Transcript, TranscriptLine{
			Seq:     ev.Seq,
			Kind:    ev.Type,
			SeatID:  ev.SeatID,
			RoleID:  lineRole,
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
		if ids, ok := pairIDs(ev.Visibility); ok {
			if seat == nil {
				return false
			}
			for _, id := range strings.Split(ids, ",") {
				if id == seat.ID {
					return true
				}
			}
			return false
		}
		return false
	}
}

// SeatVisibility builds the visibility string for a private event.
func SeatVisibility(seatID string) string { return domain.VisSeatPrefix + seatID }

// PairVisibility builds the visibility string for a two-seat line, e.g. a
// whisper. The seats are sorted so the same pair always yields the same string.
func PairVisibility(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return domain.VisPairPrefix + a + "," + b
}

// pairIDs splits a pair visibility into its raw "<a>,<b>" part.
func pairIDs(visibility string) (string, bool) {
	if !strings.HasPrefix(visibility, domain.VisPairPrefix) {
		return "", false
	}
	return strings.TrimPrefix(visibility, domain.VisPairPrefix), true
}

// sealedClue reports whether a private clue is still waiting for its keyword.
// Fragments without keywords (a plain private item) are visible to their owner
// from the start, which is what "你的线索" showed before keywords existed.
func sealedClue(c domain.Clue, st domain.ClueState) bool {
	return c.OwnerRoleID != "" && len(c.Keywords) > 0 && !st.Unlocked
}
