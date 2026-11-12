package rooms_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"script-play/internal/config"
	"script-play/internal/domain"
	"script-play/internal/llm"
	"script-play/internal/realtime"
	"script-play/internal/rooms"
	"script-play/internal/store"
)

// A finished game must not keep its SP_MAX_ROOMS slot: the actor is dropped as
// soon as the game ends and the room record flips to finished, so a long-lived
// server can keep hosting new games.
func TestFinishedGameFreesItsSlot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sc := &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "两阶段测试本", PlayerCount: domain.CountRange{Min: 2, Max: 2}},
		Roles:         []domain.Role{{ID: "r1", Name: "甲"}, {ID: "r2", Name: "乙"}},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			// A discussion round keeps the first game live long enough for the
			// capacity check below; an all-auto script can finish between the
			// two calls and make this test timing-dependent.
			{ID: "p2", Type: domain.PhaseDiscussion, Name: "讨论", SpeechPerPlayer: 1, Order: domain.OrderRoundRobin},
			{ID: "p3", Type: domain.PhaseReveal, Name: "复盘"},
		},
	}
	rec := &domain.ScriptRecord{ID: "script_test", Status: "published", Body: sc}
	st.PutScript(rec)

	cfg := &config.Config{Limits: config.Limits{
		MaxRooms:     1,
		FloorTimeout: time.Second,
		AgentTimeout: 2 * time.Second,
		// This test only checks slot release, so let the two phases run on.
		AutoAdvance: true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg := llm.NewRegistry("mock", 1)
	reg.Register(llm.NewMock())
	mgr := rooms.NewManager(ctx, st, realtime.NewHub(), reg, cfg)
	t.Cleanup(mgr.Close)

	room, _ := startGame(t, mgr, st, rec.ID, "room_first")
	if _, err := startGame2(t, mgr, st, rec.ID, "room_second"); err == nil {
		t.Fatalf("second start succeeded while the first game still ran")
	}

	// Wait for the two-phase game to finish and the manager to release it.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, live := mgr.Actor(room.ID); !live {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, live := mgr.Actor(room.ID); live {
		t.Fatalf("finished actor still holds the room slot")
	}

	stored, err := st.GetRoom(room.ID)
	if err != nil {
		t.Fatalf("get room: %v", err)
	}
	if stored.Status != domain.RoomFinished {
		t.Fatalf("room status = %q, want %q", stored.Status, domain.RoomFinished)
	}

	// With the slot freed the next game can start.
	second, _ := startGame(t, mgr, st, rec.ID, "room_second")
	if second == nil {
		t.Fatal("second game did not start after the slot was freed")
	}
}

// A start request that arrives after the game ended must not restart the
// table: the retried click used to create a fresh actor on the finished game,
// re-run its last phase and leave the room "running" until the idle reaper
// (K28 持斧奥夫).
func TestFinishedGameCannotBeStartedAgain(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rec := &domain.ScriptRecord{ID: "script_restart", Status: "published", Body: &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "重启测试本", PlayerCount: domain.CountRange{Min: 2, Max: 2}},
		Roles:         []domain.Role{{ID: "r1", Name: "甲"}, {ID: "r2", Name: "乙"}},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseReveal, Name: "复盘"},
		},
	}}
	st.PutScript(rec)

	cfg := &config.Config{Limits: config.Limits{
		MaxRooms:     4,
		FloorTimeout: time.Second,
		AgentTimeout: 2 * time.Second,
		AutoAdvance:  true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg := llm.NewRegistry("mock", 1)
	reg.Register(llm.NewMock())
	mgr := rooms.NewManager(ctx, st, realtime.NewHub(), reg, cfg)
	t.Cleanup(mgr.Close)

	room, g := startGame(t, mgr, st, rec.ID, "room_restart")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, live := mgr.Actor(room.ID); !live {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stored, err := st.GetGame(g.ID)
	if err != nil {
		t.Fatalf("get game: %v", err)
	}
	if stored.Status != domain.RoomFinished {
		t.Fatalf("game status = %q, want finished", stored.Status)
	}
	before := len(stored.Events)

	if _, err := mgr.Start(room, rec); err == nil {
		t.Fatal("a finished game accepted a second start")
	}
	stored, err = st.GetGame(g.ID)
	if err != nil {
		t.Fatalf("get game: %v", err)
	}
	if stored.Status != domain.RoomFinished {
		t.Fatalf("restart flipped the game to %q", stored.Status)
	}
	if len(stored.Events) != before {
		t.Fatalf("restart appended %d events to the finished game", len(stored.Events)-before)
	}
}

func startGame(t *testing.T, mgr *rooms.Manager, st *store.Store, scriptID, roomID string) (*domain.Room, *domain.Game) {
	t.Helper()
	room, g, err := prepare(t, mgr, st, scriptID, roomID)
	if err != nil {
		t.Fatalf("start %s: %v", roomID, err)
	}
	return room, g
}

func startGame2(t *testing.T, mgr *rooms.Manager, st *store.Store, scriptID, roomID string) (*domain.Room, error) {
	t.Helper()
	_, _, err := prepare(t, mgr, st, scriptID, roomID)
	return nil, err
}

func prepare(t *testing.T, mgr *rooms.Manager, st *store.Store, scriptID, roomID string) (*domain.Room, *domain.Game, error) {
	t.Helper()
	gameID := store.NewID("game")
	g := domain.NewGame(gameID, roomID, scriptID, []domain.Seat{
		{ID: store.NewID("seat"), RoomID: roomID, Kind: domain.SeatHuman, DisplayName: "玩家一", IsHost: true},
		{ID: store.NewID("seat"), RoomID: roomID, Kind: domain.SeatHuman, DisplayName: "玩家二", PersonaID: "guarded"},
	})
	st.PutGame(g)
	room := &domain.Room{
		ID:       roomID,
		Code:     store.NewCode(),
		Name:     "测试房",
		ScriptID: scriptID,
		GameID:   gameID,
		Status:   domain.RoomLobby,
		MaxSeats: 2,
	}
	st.PutRoom(room)
	rec, err := st.GetScript(scriptID)
	if err != nil {
		return room, g, err
	}
	if _, err := mgr.Start(room, rec); err != nil {
		return room, g, err
	}
	return room, g, nil
}

// Roles are drawn at random, not in join order. The host joins first every
// time, so "first seat gets the first role" turned every rehearsal into the
// same character; this test starts the same room repeatedly and expects the
// host's role to vary.
func TestRolesAreDrawnAtRandom(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sc := &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "四角本", PlayerCount: domain.CountRange{Min: 4, Max: 4}},
		Roles: []domain.Role{
			{ID: "r1", Name: "甲"}, {ID: "r2", Name: "乙"}, {ID: "r3", Name: "丙"}, {ID: "r4", Name: "丁"},
		},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseReveal, Name: "复盘"},
		},
	}
	rec := &domain.ScriptRecord{ID: "script_random", Status: "published", Body: sc}
	st.PutScript(rec)

	cfg := &config.Config{Limits: config.Limits{
		MaxRooms:     64,
		FloorTimeout: time.Second,
		AgentTimeout: time.Second,
		AutoAdvance:  true,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg := llm.NewRegistry("mock", 1)
	reg.Register(llm.NewMock())
	mgr := rooms.NewManager(ctx, st, realtime.NewHub(), reg, cfg)
	t.Cleanup(mgr.Close)

	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		roomID := fmt.Sprintf("room_rand_%d", i)
		gameID := store.NewID("game")
		g := domain.NewGame(gameID, roomID, rec.ID, []domain.Seat{
			{ID: store.NewID("seat"), RoomID: roomID, Kind: domain.SeatHuman, DisplayName: "主持人", IsHost: true},
			{ID: store.NewID("seat"), RoomID: roomID, Kind: domain.SeatHuman, DisplayName: "玩家二"},
			{ID: store.NewID("seat"), RoomID: roomID, Kind: domain.SeatHuman, DisplayName: "玩家三"},
			{ID: store.NewID("seat"), RoomID: roomID, Kind: domain.SeatHuman, DisplayName: "玩家四"},
		})
		st.PutGame(g)
		room := &domain.Room{
			ID: roomID, Code: store.NewCode(), Name: "随机房", ScriptID: rec.ID,
			GameID: gameID, Status: domain.RoomLobby, MaxSeats: 4,
		}
		st.PutRoom(room)
		if _, err := mgr.Start(room, rec); err != nil {
			t.Fatalf("start %s: %v", roomID, err)
		}
		started, _ := st.GetGame(gameID)
		hostRole := started.Seats[0].RoleID
		if hostRole == "" {
			t.Fatalf("host seat got no role")
		}
		seen[hostRole] = true
	}
	if len(seen) < 2 {
		t.Fatalf("host played the same role %d times: %v", 12, seen)
	}
}
