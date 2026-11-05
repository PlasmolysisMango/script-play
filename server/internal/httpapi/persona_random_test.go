package httpapi

import (
	"net/http"
	"testing"
	"time"
)

// The lobby offers a "随机" persona: the client sends the sentinel and the
// server must store a real preset, so the seat can describe its personality.
func TestRandomPersonaBecomesConcrete(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	host := adminClient(t, ts)

	var presets struct {
		Personas []struct {
			ID string `json:"id"`
		} `json:"personas"`
	}
	host.mustJSON(http.MethodGet, "/api/personas", nil, &presets, http.StatusOK)
	known := map[string]bool{}
	for _, p := range presets.Personas {
		known[p.ID] = true
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
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4}, &created, http.StatusCreated)
	code := created.Room.Code

	type seat struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		PersonaID string `json:"personaId"`
	}
	type roomPayload struct {
		Room struct {
			Seats []seat `json:"seats"`
		} `json:"room"`
	}
	assertResolved := func(label string, res roomPayload) string {
		t.Helper()
		var agent *seat
		for i := range res.Room.Seats {
			if res.Room.Seats[i].Kind == "agent" {
				agent = &res.Room.Seats[i]
			}
		}
		if agent == nil {
			t.Fatalf("%s: no agent seat in %+v", label, res.Room.Seats)
		}
		if agent.PersonaID == "" || agent.PersonaID == "random" {
			t.Fatalf("%s: persona left unresolved: %q", label, agent.PersonaID)
		}
		if !known[agent.PersonaID] {
			t.Fatalf("%s: persona %q is not a preset", label, agent.PersonaID)
		}
		return agent.ID
	}

	var added roomPayload
	host.mustJSON(http.MethodPost, "/api/rooms/"+code+"/seats",
		map[string]any{"personaId": "random"}, &added, http.StatusCreated)
	seatID := assertResolved("add seat", added)

	var patched roomPayload
	host.mustJSON(http.MethodPatch, "/api/rooms/"+code+"/seats/"+seatID,
		map[string]any{"personaId": "random"}, &patched, http.StatusOK)
	assertResolved("patch seat", patched)
}
