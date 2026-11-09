package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A seat's role book, secrets and private clues must not be reachable by asking
// the state endpoint for somebody else's seat id. The host keeps the operator
// exception; every other member is tied to its own seat.
func TestPlayerCannotReadAnotherSeatsPrivateState(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 10*time.Second)

	host := adminClient(t, ts)
	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	var created struct {
		Room struct {
			Code   string `json:"code"`
			MySeat string `json:"mySeatId"`
		} `json:"room"`
	}
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4}, &created, http.StatusCreated)

	guest := &apiClient{t: t, base: ts.URL}
	var reg struct {
		Token string `json:"token"`
	}
	guest.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "privacyguest", "password": "secret123"}, &reg, http.StatusCreated)
	guest.token = reg.Token

	code, payload := guest.call(http.MethodPost, "/api/rooms/"+created.Room.Code+"/join",
		map[string]string{"displayName": "另一个玩家"})
	if code != http.StatusCreated {
		t.Fatalf("join = %d %s", code, payload)
	}
	var joined struct {
		SeatID string `json:"seatId"`
	}
	if err := json.Unmarshal(payload, &joined); err != nil {
		t.Fatalf("decode join: %v", err)
	}
	if joined.SeatID == "" || created.Room.MySeat == "" {
		t.Fatalf("missing seats: host=%q guest=%q", created.Room.MySeat, joined.SeatID)
	}

	for _, pid := range []string{"guarded", "sly"} {
		host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/seats",
			map[string]any{"personaId": pid}, nil, http.StatusCreated)
	}
	host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/start", nil, nil, http.StatusOK)

	code, payload = guest.call(http.MethodGet,
		"/api/rooms/"+created.Room.Code+"/state?seatId="+created.Room.MySeat, nil)
	if code != http.StatusForbidden {
		t.Fatalf("guest read host private state = %d %s", code, payload)
	}

	code, payload = guest.call(http.MethodGet,
		"/api/rooms/"+created.Room.Code+"/state?seatId="+joined.SeatID, nil)
	if code != http.StatusOK {
		t.Fatalf("guest read own state = %d %s", code, payload)
	}

	code, payload = host.call(http.MethodGet,
		"/api/rooms/"+created.Room.Code+"/state?seatId="+joined.SeatID, nil)
	if code != http.StatusOK {
		t.Fatalf("host operator read = %d %s", code, payload)
	}
}
