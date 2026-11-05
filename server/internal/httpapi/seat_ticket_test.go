package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Browsers cannot set headers on a WebSocket handshake, so the client trades
// its session for a short-lived ticket bound to one seat. These tests pin the
// ticket contract: who can mint one, what it unlocks, and what it refuses.
func TestSeatTicketBindsWebSocketToOneSeat(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 10*time.Second)

	host := adminClient(t, ts)
	var reg struct {
		Token string `json:"token"`
	}

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)
	var created struct {
		Room struct {
			Code string `json:"code"`
		} `json:"room"`
	}
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4, "allowSpectator": true}, &created, http.StatusCreated)
	code := created.Room.Code

	// Someone without a seat gets no ticket.
	outsider := &apiClient{t: t, base: ts.URL}
	outsider.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "ticketout", "password": "secret123"}, &reg, http.StatusCreated)
	outsider.token = reg.Token
	if code, payload := outsider.call(http.MethodGet, "/api/rooms/"+code+"/seat-ticket", nil); code != http.StatusForbidden {
		t.Fatalf("outsider ticket = %d %s", code, payload)
	}

	var minted struct {
		Ticket    string `json:"ticket"`
		SeatID    string `json:"seatId"`
		GameID    string `json:"gameId"`
		ExpiresAt string `json:"expiresAt"`
		WSPath    string `json:"wsPath"`
	}
	host.mustJSON(http.MethodGet, "/api/rooms/"+code+"/seat-ticket", nil, &minted, http.StatusOK)
	if minted.Ticket == "" || minted.SeatID == "" || minted.GameID == "" {
		t.Fatalf("incomplete ticket: %+v", minted)
	}
	if !strings.Contains(minted.WSPath, "?ticket=") {
		t.Errorf("wsPath = %q", minted.WSPath)
	}

	// A session token is not a ticket, so it cannot be smuggled in as one.
	sessionAsTicket := strings.Replace(ts.URL, "http", "ws", 1) + "/ws/rooms/" + code + "?ticket=" + host.token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := websocket.Dial(ctx, sessionAsTicket, nil); err == nil {
		t.Fatal("session token accepted as a seat ticket")
	}

	// The real ticket opens the socket bound to that seat.
	wsURL := strings.Replace(ts.URL, "http", "ws", 1) + "/ws/rooms/" + code + "?ticket=" + minted.Ticket
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial with ticket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// And a ticket for one room must not open another room's socket.
	var second struct {
		Room struct {
			Code string `json:"code"`
		} `json:"room"`
	}
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4}, &second, http.StatusCreated)
	otherURL := strings.Replace(ts.URL, "http", "ws", 1) + "/ws/rooms/" + second.Room.Code + "?ticket=" + minted.Ticket
	if _, _, err := websocket.Dial(ctx, otherURL, nil); err == nil {
		t.Fatal("ticket from another game opened this room")
	}
}
