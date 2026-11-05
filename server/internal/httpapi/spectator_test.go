package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A late joiner must be able to watch a running game (that is the whole point
// of allowSpectator) and must not receive a role book, private clues or the
// solution.
func TestLateJoinerBecomesASpectatorWithoutPrivateData(t *testing.T) {
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
	// The sample script seats exactly four, so fill the three AI seats before
	// starting: a short lobby is refused now that seats follow the cast.
	for _, pid := range []string{"guarded", "sly", "theatrical"} {
		host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/seats",
			map[string]any{"personaId": pid}, nil, http.StatusCreated)
	}
	host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/start", nil, nil, http.StatusOK)

	guest := &apiClient{t: t, base: ts.URL}
	guest.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "guestspec", "password": "secret123"}, &reg, http.StatusCreated)
	guest.token = reg.Token

	code, payload := guest.call(http.MethodPost, "/api/rooms/"+created.Room.Code+"/join",
		map[string]string{"displayName": "迟到观众"})
	if code != http.StatusCreated {
		t.Fatalf("join = %d %s", code, payload)
	}
	var joined struct {
		SeatID string `json:"seatId"`
		Room   struct {
			Status string `json:"status"`
			Seats  []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
				Mine bool   `json:"mine"`
			} `json:"seats"`
		} `json:"room"`
	}
	if err := json.Unmarshal(payload, &joined); err != nil {
		t.Fatalf("decode join: %v body=%s", err, payload)
	}
	if joined.SeatID == "" {
		t.Fatal("spectator got no seat id")
	}
	var kind string
	for _, s := range joined.Room.Seats {
		if s.Mine {
			kind = s.Kind
		}
	}
	if kind != "spectator" {
		t.Fatalf("late joiner kind = %q want spectator; body=%s", kind, payload)
	}

	code, payload = guest.call(http.MethodGet, "/api/rooms/"+created.Room.Code+"/state", nil)
	if code != http.StatusOK {
		t.Fatalf("spectator state = %d %s", code, payload)
	}
	var state struct {
		State struct {
			Visible struct {
				OwnRole     json.RawMessage   `json:"ownRole"`
				OwnClues    []json.RawMessage `json:"ownClues"`
				PublicClues []json.RawMessage `json:"publicClues"`
				Truth       json.RawMessage   `json:"truth"`
			} `json:"visible"`
			Search []json.RawMessage `json:"search"`
		} `json:"state"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.State.Visible.OwnRole) > 0 && string(state.State.Visible.OwnRole) != "null" {
		t.Errorf("spectator received a role book: %s", state.State.Visible.OwnRole)
	}
	if len(state.State.Visible.OwnClues) != 0 {
		t.Errorf("spectator received private clues: %s", payload)
	}
	if strings.Contains(strings.ToLower(string(payload)), "killerroleid") {
		t.Errorf("spectator state exposed the killer: %s", payload)
	}
	if len(state.State.Search) != 0 {
		t.Errorf("spectator was offered clues to search: %s", payload)
	}
}
