package engine

import (
	"script-play/internal/domain"
)

// Machine walks the phase list of a script. Phases are a plain slice: the
// engine never reorders them at runtime so the flow stays auditable.
type Machine struct {
	sc  *domain.Script
	idx domain.ScriptIndex
}

// NewMachine binds a script.
func NewMachine(sc *domain.Script) *Machine {
	return &Machine{sc: sc, idx: sc.Tokenize()}
}

// TokenClue looks a clue up by id.
func (m *Machine) TokenClue(id string) *domain.Clue { return m.idx.ClueByID(id) }

// Len is the number of phases.
func (m *Machine) Len() int { return len(m.sc.Phases) }

// Phase returns the phase at index or nil.
func (m *Machine) Phase(i int) *domain.Phase {
	if i < 0 || i >= len(m.sc.Phases) {
		return nil
	}
	return &m.sc.Phases[i]
}

// Current returns the phase the game is in.
func (m *Machine) Current(g *domain.Game) *domain.Phase { return m.Phase(g.PhaseIndex) }

// Next returns the following index, or -1 when the script is exhausted.
func (m *Machine) Next(i int) int {
	if i+1 >= len(m.sc.Phases) {
		return -1
	}
	return i + 1
}

// Advance moves the game forward. It reports the phase entered and whether the
// game ended.
func (m *Machine) Advance(g *domain.Game) (phase *domain.Phase, finished bool) {
	next := m.Next(g.PhaseIndex)
	if next < 0 {
		return nil, true
	}
	g.PhaseIndex = next
	p := &m.sc.Phases[next]
	if p.Round > 0 {
		g.Round = p.Round
	}
	return p, false
}

// Summary returns the phase list for the UI progress bar.
func (m *Machine) Summary(g *domain.Game) []PhaseSummary {
	out := make([]PhaseSummary, 0, len(m.sc.Phases))
	// A finished game has no "current" phase: the last one closed with the
	// game, otherwise the progress bar would look stuck forever.
	finished := g.Status == domain.RoomFinished
	for i, p := range m.sc.Phases {
		status := "pending"
		switch {
		case finished || i < g.PhaseIndex:
			status = "done"
		case i == g.PhaseIndex:
			status = "current"
		}
		out = append(out, PhaseSummary{ID: p.ID, Name: p.Name, Type: p.Type, Round: p.Round, Status: status})
	}
	return out
}

// PhaseSummary is one entry of the progress bar.
type PhaseSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Round  int    `json:"round,omitempty"`
	Status string `json:"status"`
}
