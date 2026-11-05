package httpapi

import (
	"net/http"
	"testing"
	"time"
)

// Rooms pick their own speech deadline and whether a silent seat is skipped,
// so the lobby form has to survive the round trip.
func TestRoomSpeechSettingsRoundTrip(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	host := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	type roomSettings struct {
		Code          string `json:"code"`
		SpeechSeconds int    `json:"speechSeconds"`
		AutoSkip      bool   `json:"autoSkip"`
	}
	create := func(body map[string]any) roomSettings {
		t.Helper()
		payload := map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4}
		for k, v := range body {
			payload[k] = v
		}
		var res struct {
			Room roomSettings `json:"room"`
		}
		host.mustJSON(http.MethodPost, "/api/rooms", payload, &res, http.StatusCreated)
		return res.Room
	}

	hosted := create(map[string]any{"speechSeconds": 45, "autoSkip": false})
	if hosted.SpeechSeconds != 45 || hosted.AutoSkip {
		t.Fatalf("settings not stored: %+v", hosted)
	}
	// They must also survive a fresh read, not just the create response.
	var fetched struct {
		Room roomSettings `json:"room"`
	}
	host.mustJSON(http.MethodGet, "/api/rooms/"+hosted.Code, nil, &fetched, http.StatusOK)
	if fetched.Room.SpeechSeconds != 45 || fetched.Room.AutoSkip {
		t.Fatalf("settings lost on read: %+v", fetched.Room)
	}

	// Omitting the fields keeps the server default and auto-skip.
	def := create(nil)
	if def.SpeechSeconds != 0 || !def.AutoSkip {
		t.Fatalf("defaults changed: %+v", def)
	}

	// 5 seconds is below the supported floor.
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4, "speechSeconds": 5},
		nil, http.StatusBadRequest)

	// -1 opts the whole table out of the per-turn clock.
	unlimited := create(map[string]any{"speechSeconds": -1})
	if unlimited.SpeechSeconds != -1 {
		t.Fatalf("unlimited not stored: %+v", unlimited)
	}
	// Any other negative number is rejected instead of silently meaning "no limit".
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4, "speechSeconds": -2},
		nil, http.StatusBadRequest)

	// The lobby prints the server default next to the 「服务端默认」 option.
	var health struct {
		FloorTimeoutSec int `json:"floorTimeoutSec"`
	}
	host.mustJSON(http.MethodGet, "/api/health", nil, &health, http.StatusOK)
	if health.FloorTimeoutSec != 1 {
		t.Fatalf("health floor default = %d, want 1", health.FloorTimeoutSec)
	}
}
