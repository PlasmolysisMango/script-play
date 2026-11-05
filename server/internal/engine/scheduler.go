package engine

import (
	"sort"
	"time"

	"script-play/internal/domain"
)

// Floor is the speaking token granted to one seat. Agents only generate when
// they hold the floor, so turns never interleave.
type Floor struct {
	SeatID  string `json:"seatId"`
	PhaseID string `json:"phaseId"`
	// Kind is why this seat holds the floor (turn / reply / request) and
	// ReplyTo names the seat it is answering, so the UI can prompt correctly.
	Kind      string    `json:"kind,omitempty"`
	ReplyTo   string    `json:"replyTo,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Scheduler produces a stable speaking order. The host speaks first, then seats
// in join order, which keeps replays readable.
type Scheduler struct {
	order []domain.Seat
}

// NewScheduler builds the order for one phase from the player seats.
func NewScheduler(seats []domain.Seat) *Scheduler {
	players := make([]domain.Seat, 0, len(seats))
	for _, s := range seats {
		if s.Kind == domain.SeatSpectator {
			continue
		}
		players = append(players, s)
	}
	sort.SliceStable(players, func(i, j int) bool {
		if players[i].IsHost != players[j].IsHost {
			return players[i].IsHost
		}
		return players[i].JoinedAt.Before(players[j].JoinedAt)
	})
	return &Scheduler{order: players}
}

// Order returns the player seats in speaking order.
func (s *Scheduler) Order() []domain.Seat {
	out := make([]domain.Seat, len(s.order))
	copy(out, s.order)
	return out
}

// Next returns the first seat that still has quota left, or nil.
func (s *Scheduler) Next(counts map[string]int, limit int) *domain.Seat {
	if limit <= 0 {
		limit = 1
	}
	for i := range s.order {
		if counts[s.order[i].ID] < limit {
			seat := s.order[i]
			return &seat
		}
	}
	return nil
}

// NextAfter returns the next seat after prev that still has quota, wrapping
// around once. Used to keep round-robin order inside a round.
func (s *Scheduler) NextAfter(prev string, counts map[string]int, limit int) *domain.Seat {
	if len(s.order) == 0 {
		return nil
	}
	start := 0
	for i := range s.order {
		if s.order[i].ID == prev {
			start = i + 1
			break
		}
	}
	for k := 0; k < len(s.order); k++ {
		i := (start + k) % len(s.order)
		if counts[s.order[i].ID] < limit {
			seat := s.order[i]
			return &seat
		}
	}
	return nil
}

// Quota is the remaining actions for a seat in the current phase.
func Quota(done, limit int) int {
	if limit <= 0 {
		return 0
	}
	if done >= limit {
		return 0
	}
	return limit - done
}
