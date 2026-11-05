package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"script-play/internal/auth"
	"script-play/internal/domain"
	"script-play/internal/engine"
)

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Two ways in: a seat ticket (?ticket=, preferred) or the session token
	// (?token= / bearer).
	ticket := r.URL.Query().Get("ticket")
	var claims *auth.Claims
	if ticket != "" {
		c, err := auth.Verify(s.cfg.JWTSecret, ticket)
		if err != nil || c.Purpose != auth.PurposeSeatTicket {
			fail(w, http.StatusUnauthorized, auth.ErrInvalidToken)
			return
		}
		claims = c
	} else {
		token := r.URL.Query().Get("token")
		if token == "" {
			token = bearer(r)
		}
		c, err := auth.Verify(s.cfg.JWTSecret, token)
		if err != nil || !isAccessToken(c) {
			fail(w, http.StatusUnauthorized, err)
			return
		}
		claims = c
	}
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))
	room, err := s.store.RoomByCode(code)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	g, err := s.rooms.Game(room.GameID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if claims.GameID != "" && claims.GameID != g.ID {
		fail(w, http.StatusForbidden, auth.ErrInvalidToken)
		return
	}
	wantSeat := r.URL.Query().Get("seat")
	if claims.SeatID != "" {
		wantSeat = claims.SeatID
	}
	var seat *domain.Seat
	for i := range g.Seats {
		st := g.Seats[i]
		if st.UserID != claims.Sub {
			continue
		}
		if wantSeat != "" && st.ID != wantSeat {
			continue
		}
		cp := st
		seat = &cp
		break
	}
	if seat == nil {
		fail(w, http.StatusForbidden, errNoSeat)
		return
	}

	onMessage := func(ctx context.Context, seat domain.Seat, payload []byte) error {
		var msg engine.ClientMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			return err
		}
		return s.rooms.Handle(ctx, g.ID, seat.ID, msg)
	}
	s.rooms.Hub().Serve(r.Context(), w, r, *seat, g.ID, onMessage)
}

var errNoSeat = errNoSeatError{}

type errNoSeatError struct{}

func (errNoSeatError) Error() string { return "你还没有加入这个房间" }
