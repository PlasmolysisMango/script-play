package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// flexibleCastScript advertises 2~3 players with three roles, which is the only
// shape where a room may choose its own seat count.
const flexibleCastScript = `{
  "schemaVersion": 1,
  "meta": {"title": "弹性人数测试本", "playerCount": {"min": 2, "max": 3}},
  "truth": {"killerRoleId": "r2", "summary": "真相是乙。"},
  "phases": [
    {"id": "p1", "type": "reading", "name": "阅读剧本"},
    {"id": "p2", "type": "search", "name": "搜证", "cluePool": ["c1"], "perPlayerPick": 1},
    {"id": "p3", "type": "discussion", "name": "讨论", "speechPerPlayer": 1},
    {"id": "p4", "type": "vote", "name": "投票"},
    {"id": "p5", "type": "reveal", "name": "复盘"}
  ],
  "roles": [
    {"id": "r1", "name": "甲", "public": {"intro": "甲"}, "private": {"script": "甲本"}},
    {"id": "r2", "name": "乙", "public": {"intro": "乙"}, "private": {"script": "乙本"}},
    {"id": "r3", "name": "丙", "public": {"intro": "丙"}, "private": {"script": "丙本"}}
  ],
  "clues": [{"id": "c1", "title": "刀", "content": "一把刀"}]
}`

// TestRoomSeatsFollowScriptPlayerCount pins the rule that a room never seats a
// number of players the script cannot host: a fixed cast locks the seat count,
// a flexible one only allows its advertised range.
func TestRoomSeatsFollowScriptPlayerCount(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	host := adminClient(t, ts)

	type createResult struct {
		Room struct {
			Code     string `json:"code"`
			MaxSeats int    `json:"maxSeats"`
		} `json:"room"`
	}
	create := func(t *testing.T, scriptID string, seats int, want int) createResult {
		t.Helper()
		payload := map[string]any{"scriptId": scriptID}
		if seats > 0 {
			payload["maxSeats"] = seats
		}
		var res createResult
		host.mustJSON(http.MethodPost, "/api/rooms", payload, &res, want)
		return res
	}

	// The bundled sample is a fixed four-player script.
	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	for _, seats := range []int{2, 3, 5} {
		create(t, seeded.Script.ID, seats, http.StatusBadRequest)
	}
	fixed := create(t, seeded.Script.ID, 4, http.StatusCreated)
	if fixed.Room.MaxSeats != 4 {
		t.Fatalf("fixed cast seated %d, want 4", fixed.Room.MaxSeats)
	}
	// Omitting the field falls back to the script's own count, not a guess.
	if def := create(t, seeded.Script.ID, 0, http.StatusCreated); def.Room.MaxSeats != 4 {
		t.Fatalf("default seat count = %d, want 4", def.Room.MaxSeats)
	}

	// A flexible script advertises 2~3 players, so both ends and the middle pass
	// while anything outside the range is refused.
	var imported struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodPost, "/api/scripts", json.RawMessage(flexibleCastScript), &imported, http.StatusCreated)
	host.mustJSON(http.MethodPost, "/api/scripts/"+imported.Script.ID+"/publish", nil, nil, http.StatusOK)

	for _, seats := range []int{2, 3} {
		if got := create(t, imported.Script.ID, seats, http.StatusCreated); got.Room.MaxSeats != seats {
			t.Fatalf("flexible cast seated %d, want %d", got.Room.MaxSeats, seats)
		}
	}
	for _, seats := range []int{1, 4} {
		create(t, imported.Script.ID, seats, http.StatusBadRequest)
	}
	if def := create(t, imported.Script.ID, 0, http.StatusCreated); def.Room.MaxSeats != 3 {
		t.Fatalf("flexible default = %d, want the upper bound 3", def.Room.MaxSeats)
	}
}
