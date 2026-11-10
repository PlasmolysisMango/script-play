package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The room lifecycle (create as host only, leave, hand the table over, end a
// game) is what the acceptance run was missing: a creator could neither free
// their seat nor stop a stuck table, so every dead room stayed reachable only
// through the admin console.

type lifecycleRoom struct {
	Code     string `json:"code"`
	Status   string `json:"status"`
	MaxSeats int    `json:"maxSeats"`
	Host     bool   `json:"host"`
	MySeatID string `json:"mySeatId"`
	Seats    []struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		IsHost      bool   `json:"isHost"`
		Mine        bool   `json:"mine"`
		Ready       bool   `json:"ready"`
		RoleID      string `json:"roleId"`
		DisplayName string `json:"displayName"`
	} `json:"seats"`
}

type roomEnvelope struct {
	Room lifecycleRoom `json:"room"`
}

func registerUser(t *testing.T, ts *httptest.Server, name string) *apiClient {
	t.Helper()
	c := &apiClient{t: t, base: ts.URL}
	var reg struct {
		Token string `json:"token"`
	}
	c.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": name, "password": "secret123", "displayName": name}, &reg, http.StatusCreated)
	c.token = reg.Token
	return c
}

// importFlexibleScript publishes the 3-role / 2~3-seat fixture used by the
// seat-count test, and returns its id.
func importFlexibleScript(t *testing.T, admin *apiClient) string {
	t.Helper()
	var imported struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts", json.RawMessage(flexibleCastScript), &imported, http.StatusCreated)
	admin.mustJSON(http.MethodPost, "/api/scripts/"+imported.Script.ID+"/publish", nil, nil, http.StatusOK)
	return imported.Script.ID
}

func createRoom(t *testing.T, c *apiClient, payload map[string]any) roomEnvelope {
	t.Helper()
	var out roomEnvelope
	c.mustJSON(http.MethodPost, "/api/rooms", payload, &out, http.StatusCreated)
	if out.Room.Code == "" {
		t.Fatalf("created room has no code: %+v", out.Room)
	}
	return out
}

func joinRoom(t *testing.T, c *apiClient, code string) roomEnvelope {
	t.Helper()
	var out roomEnvelope
	c.mustJSON(http.MethodPost, "/api/rooms/"+code+"/join", map[string]string{}, &out, http.StatusCreated)
	return out
}

func mustRoom(t *testing.T, c *apiClient, code string) lifecycleRoom {
	t.Helper()
	var out roomEnvelope
	c.mustJSON(http.MethodGet, "/api/rooms/"+code, nil, &out, http.StatusOK)
	return out.Room
}

func waitUntil(t *testing.T, why string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// A creator who only runs the table must not eat one of the script's fixed
// player seats: the acceptance run lost one role to the host in every game.
func TestHostOnlyRoomLeavesEverySeatToPlayers(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)
	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	host := registerUser(t, ts, "lifecycle_hostonly")
	created := createRoom(t, host, map[string]any{
		"scriptId": seeded.Script.ID,
		"maxSeats": 4,
		"hostOnly": true,
	})
	if !created.Room.Host {
		t.Fatal("creator is not flagged as host")
	}
	var hostSeatIdx = -1
	for i := range created.Room.Seats {
		if created.Room.Seats[i].ID == created.Room.MySeatID {
			hostSeatIdx = i
		}
	}
	if hostSeatIdx < 0 {
		t.Fatalf("host has no seat: %+v", created.Room)
	}
	hostSeat := created.Room.Seats[hostSeatIdx]
	if hostSeat.Kind != "spectator" {
		t.Fatalf("host-only creator sits as %q, want spectator", hostSeat.Kind)
	}
	if !hostSeat.IsHost {
		t.Fatal("host spectator seat is not flagged")
	}

	// The fixed four-player cast must still fit: four humans join without ever
	// being told the room is full.
	for i := 0; i < 4; i++ {
		guest := registerUser(t, ts, fmt.Sprintf("lifecycle_fill_%d", i))
		seated := joinRoom(t, guest, created.Room.Code)
		players := 0
		for _, s := range seated.Room.Seats {
			if s.Kind == "human" {
				players++
			}
		}
		if players != i+1 {
			t.Fatalf("after %d joins the room holds %d players", i+1, players)
		}
	}
	fifth := registerUser(t, ts, "lifecycle_fill_e")
	if code, body := fifth.call(http.MethodPost, "/api/rooms/"+created.Room.Code+"/join", map[string]string{}); code != http.StatusConflict {
		t.Fatalf("fifth player joined a full room: %d %s", code, body)
	}

	// Walking away hands the table to a seated human, so the room survives: the
	// host's seat is gone but the four players are still there.
	var left struct {
		Closed bool `json:"closed"`
	}
	host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/leave", nil, &left, http.StatusOK)
	if left.Closed {
		t.Fatal("host leaving a staffed room closed it anyway")
	}
	if code, _ := host.call(http.MethodGet, "/api/rooms/"+created.Room.Code, nil); code != http.StatusOK {
		t.Fatalf("room disappeared after the host left: %d", code)
	}
}

// The host may cast across seats, hand the table over, and leave; a lobby room
// nobody is left in is deleted.
func TestHostTransfersAndLeavesRoom(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)
	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	host := registerUser(t, ts, "lifecycle_host")
	guest := registerUser(t, ts, "lifecycle_guest")

	created := createRoom(t, host, map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4})
	hostSeatID := created.Room.MySeatID
	if hostSeatID == "" {
		t.Fatal("creator seat missing")
	}
	seated := joinRoom(t, guest, created.Room.Code)
	guestSeat := seated.Room.MySeatID
	if guestSeat == "" {
		t.Fatal("guest seat missing")
	}

	// Casting from the host's chair is a DM task; it used to answer 403 for
	// everybody but the seat's owner.
	host.mustJSON(http.MethodPatch, "/api/rooms/"+created.Room.Code+"/seats/"+guestSeat,
		map[string]any{"roleId": "r1"}, nil, http.StatusOK)

	// Hand the table over: the guest is now the host, the creator is a player.
	host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/host",
		map[string]string{"seatId": guestSeat}, nil, http.StatusOK)
	if !mustRoom(t, guest, created.Room.Code).Host {
		t.Fatal("guest did not receive the host flag")
	}
	if mustRoom(t, host, created.Room.Code).Host {
		t.Fatal("the handing-over host kept the flag")
	}

	// The former host leaves: the room survives because somebody can still run it.
	host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/leave", nil, nil, http.StatusOK)
	after := mustRoom(t, guest, created.Room.Code)
	for _, s := range after.Seats {
		if s.ID == hostSeatID {
			t.Fatal("the leaving host's seat is still on the table")
		}
	}

	// The last host leaving deletes the room rather than stranding it.
	var left struct {
		Closed bool `json:"closed"`
	}
	guest.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/leave", nil, &left, http.StatusOK)
	if !left.Closed {
		t.Fatal("last host leaving did not close the room")
	}
	if code, _ := guest.call(http.MethodGet, "/api/rooms/"+created.Room.Code, nil); code != http.StatusNotFound {
		t.Fatalf("deleted room still answered %d", code)
	}
}

// Ready is voluntary until somebody uses it, then it gates the start; closing a
// running game frees its slot and keeps the room readable as finished.
func TestReadyGateAndCloseRunningGame(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)
	scriptID := importFlexibleScript(t, admin)

	host := registerUser(t, ts, "lifecycle_ready_host")
	p1 := registerUser(t, ts, "lifecycle_ready_p1")
	p2 := registerUser(t, ts, "lifecycle_ready_p2")

	created := createRoom(t, host, map[string]any{"scriptId": scriptID, "maxSeats": 3})
	code := created.Room.Code
	s1 := joinRoom(t, p1, code).Room.MySeatID
	s2 := joinRoom(t, p2, code).Room.MySeatID

	// One ticked box commits everybody: the start names the seat still waiting.
	p1.mustJSON(http.MethodPatch, "/api/rooms/"+code+"/seats/"+s1, map[string]any{"ready": true}, nil, http.StatusOK)
	if status, body := host.call(http.MethodPost, "/api/rooms/"+code+"/start", nil); status != http.StatusConflict ||
		!strings.Contains(string(body), "没有准备") {
		t.Fatalf("start with an unready player = %d %s", status, body)
	}
	p2.mustJSON(http.MethodPatch, "/api/rooms/"+code+"/seats/"+s2, map[string]any{"ready": true}, nil, http.StatusOK)
	host.mustJSON(http.MethodPost, "/api/rooms/"+code+"/start", nil, nil, http.StatusOK)

	if got := mustRoom(t, host, code).Status; got != "running" {
		t.Fatalf("started room reports %q", got)
	}

	// A play/score script has no reveal phase, so the host's own ending is the
	// only way out; it must also release the concurrency slot.
	host.mustJSON(http.MethodPost, "/api/rooms/"+code+"/close", nil, nil, http.StatusOK)
	waitUntil(t, "the closed room to report finished", 3*time.Second, func() bool {
		return mustRoom(t, host, code).Status == "finished"
	})
}

// A refused start must say how many games hold the slots, and closing an old
// game must let the next one in — the acceptance run was blocked by 409s that
// named no cause.
func TestRunningLimitNamesOccupancyAndCloseFreesIt(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)
	scriptID := importFlexibleScript(t, admin)

	type running struct {
		host *apiClient
		code string
	}
	start := func(name string) running {
		t.Helper()
		host := registerUser(t, ts, name)
		guest := registerUser(t, ts, name+"_p2")
		code := createRoom(t, host, map[string]any{"scriptId": scriptID, "maxSeats": 2}).Room.Code
		joinRoom(t, guest, code)
		host.mustJSON(http.MethodPost, "/api/rooms/"+code+"/start", nil, nil, http.StatusOK)
		return running{host: host, code: code}
	}

	first := start("lifecycle_cap_a")
	start("lifecycle_cap_b")

	thirdHost := registerUser(t, ts, "lifecycle_cap_c")
	thirdGuest := registerUser(t, ts, "lifecycle_cap_c_p2")
	thirdCode := createRoom(t, thirdHost, map[string]any{"scriptId": scriptID, "maxSeats": 2}).Room.Code
	joinRoom(t, thirdGuest, thirdCode)

	status, body := thirdHost.call(http.MethodPost, "/api/rooms/"+thirdCode+"/start", nil)
	if status != http.StatusConflict {
		t.Fatalf("third start = %d %s, want 409", status, body)
	}
	if !strings.Contains(string(body), "2/2") {
		t.Fatalf("refusal does not name the occupancy: %s", body)
	}

	first.host.mustJSON(http.MethodPost, "/api/rooms/"+first.code+"/close", nil, nil, http.StatusOK)
	waitUntil(t, "the freed slot to admit the third game", 3*time.Second, func() bool {
		code, _ := thirdHost.call(http.MethodPost, "/api/rooms/"+thirdCode+"/start", nil)
		return code == http.StatusOK
	})
}
