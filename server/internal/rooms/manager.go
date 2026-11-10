// Package rooms owns the lifecycle of games: lobby seats, start, live actors.
package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"script-play/internal/config"
	"script-play/internal/domain"
	"script-play/internal/engine"
	"script-play/internal/llm"
	"script-play/internal/persona"
	"script-play/internal/realtime"
	"script-play/internal/store"
)

// Errors returned by the manager.
var (
	ErrRoomLimit = errors.New("同时进行的对局已达上限，请稍后再试")
	ErrNotFound  = store.ErrNotFound
)

// RoomLimitError explains a refused start: how many games run and how many the
// server allows. A bare "try later" left players with no idea whether to wait
// or to close an old game, and the acceptance run could not tell an empty list
// from four zombies holding every slot.
type RoomLimitError struct {
	Max     int
	Running int
}

func (e *RoomLimitError) Error() string {
	return fmt.Sprintf("同时进行的对局已达上限（%d/%d），请稍后再试或结束一个旧对局", e.Running, e.Max)
}

// Is keeps errors.Is(err, ErrRoomLimit) working for callers that only know the
// sentinel.
func (e *RoomLimitError) Is(target error) bool { return target == ErrRoomLimit }

// Manager keeps the live actor of every running room.
type Manager struct {
	mu     sync.Mutex
	store  *store.Store
	hub    *realtime.Hub
	reg    *llm.Registry
	cfg    *config.Config
	actors map[string]*engine.Actor // roomID -> actor
	byGame map[string]*engine.Actor // gameID -> actor
	ctx    context.Context
	// born and idle support the watchdog: when a game started, and how long it
	// has gone without a single live socket.
	born map[string]time.Time
	idle map[string]time.Duration
}

// NewManager wires the manager. ctx bounds every actor goroutine.
func NewManager(ctx context.Context, st *store.Store, hub *realtime.Hub, reg *llm.Registry, cfg *config.Config) *Manager {
	m := &Manager{
		store:  st,
		hub:    hub,
		reg:    reg,
		cfg:    cfg,
		actors: map[string]*engine.Actor{},
		byGame: map[string]*engine.Actor{},
		ctx:    ctx,
		born:   map[string]time.Time{},
		idle:   map[string]time.Duration{},
	}
	// A restart loses every actor while the store still says "running": those
	// rooms used to sit in the lobby forever and were invisible in the list. Mark
	// them finished now so the tables can be reopened and replays still work.
	m.recoverOrphans()
	go m.watch()
	return m
}

// Hub exposes the realtime hub to the HTTP layer.
func (m *Manager) Hub() *realtime.Hub { return m.hub }

// CreateGame builds the lobby game of a room.
func (m *Manager) CreateGame(roomID, scriptID string) (*domain.Game, error) {
	g := domain.NewGame(store.NewID("game"), roomID, scriptID, nil)
	m.store.PutGame(g)
	return g, nil
}

// Game returns the current authoritative game: the live actor wins over the
// stored snapshot.
func (m *Manager) Game(gameID string) (*domain.Game, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gameLocked(gameID)
}

// gameLocked is Game for callers that already hold m.mu.
func (m *Manager) gameLocked(gameID string) (*domain.Game, error) {
	if actor := m.byGame[gameID]; actor != nil {
		g := actor.Snapshot()
		return &g, nil
	}
	return m.store.GetGame(gameID)
}

// Seat looks a seat up in the lobby game.
func (m *Manager) Seat(gameID, seatID string) (domain.Seat, error) {
	g, err := m.Game(gameID)
	if err != nil {
		return domain.Seat{}, err
	}
	s := g.SeatByID(seatID)
	if s == nil {
		return domain.Seat{}, store.ErrNotFound
	}
	return *s, nil
}

// AddSeat appends a seat to a game. Lobby seats go to the store; once a game
// is running the actor owns the state, so spectators are inserted there.
func (m *Manager) AddSeat(gameID string, seat domain.Seat) (domain.Seat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The actor wins over the stored snapshot once it exists.
	g, err := m.gameLocked(gameID)
	if err != nil {
		return domain.Seat{}, err
	}
	// Spectators may still attach themselves to a running game; only players
	// are frozen once it starts.
	if g.Status != domain.RoomLobby && seat.Kind != domain.SeatSpectator {
		return domain.Seat{}, errors.New("对局已经开始，只能以观战身份进入")
	}
	if seat.ID == "" {
		seat.ID = store.NewID("seat")
	}
	if seat.JoinedAt.IsZero() {
		seat.JoinedAt = time.Now().UTC()
	}
	if seat.PersonaID == "" {
		seat.PersonaID = persona.RandomID(len(g.Seats))
	}
	if seat.Kind == "" {
		seat.Kind = domain.SeatHuman
	}
	if seat.RoleID != "" {
		for _, other := range g.Seats {
			if other.RoleID == seat.RoleID {
				return domain.Seat{}, errors.New("该角色已被其他座位占用")
			}
		}
	}
	if actor := m.byGame[gameID]; actor != nil {
		return actor.AddSeat(seat)
	}
	g.Seats = append(g.Seats, seat)
	m.store.PutGame(g)
	return seat, nil
}

// UpdateSeat mutates a lobby seat under the manager lock.
func (m *Manager) UpdateSeat(gameID, seatID string, fn func(*domain.Seat) error) (domain.Seat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.store.GetGame(gameID)
	if err != nil {
		return domain.Seat{}, err
	}
	s := g.SeatByID(seatID)
	if s == nil {
		return domain.Seat{}, store.ErrNotFound
	}
	running := g.Status != domain.RoomLobby
	if running {
		return *s, errors.New("对局开始后不能修改座位")
	}
	if err := fn(s); err != nil {
		return *s, err
	}
	// Check the game this lock owns, not a handler's earlier snapshot: two
	// concurrent PATCHes could each see a free role and claim it.
	if s.RoleID != "" {
		for i := range g.Seats {
			if g.Seats[i].ID != s.ID && g.Seats[i].RoleID == s.RoleID {
				return *s, errors.New("该角色已被其他座位占用")
			}
		}
	}
	m.store.PutGame(g)
	return *s, nil
}

// RemoveSeat drops a seat from a lobby game. Mid-game removals would have to
// go through the actor, so they are rejected instead of silently ignored.
func (m *Manager) RemoveSeat(gameID, seatID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.gameLocked(gameID)
	if err != nil {
		return err
	}
	if g.Status != domain.RoomLobby {
		return errors.New("对局开始后不能移除座位")
	}
	out := g.Seats[:0]
	found := false
	for _, s := range g.Seats {
		if s.ID == seatID {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return store.ErrNotFound
	}
	g.Seats = out
	m.store.PutGame(g)
	return nil
}

// Start validates the lobby, assigns missing roles, creates the actor and runs
// the first phase.
func (m *Manager) Start(room *domain.Room, rec *domain.ScriptRecord) (*engine.Actor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.actors[room.ID]; ok {
		return nil, errors.New("该房间的对局已经开始")
	}
	if len(m.actors) >= m.cfg.Limits.MaxRooms {
		return nil, &RoomLimitError{Max: m.cfg.Limits.MaxRooms, Running: len(m.actors)}
	}
	g, err := m.store.GetGame(room.GameID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Body == nil {
		return nil, errors.New("剧本未发布或内容为空")
	}
	sc := rec.Body
	players := g.PlayerSeats()
	if len(players) < 2 {
		return nil, errors.New("至少需要 2 个玩家座位")
	}
	if sc.Meta.PlayerCount.Max > 0 && len(players) > sc.Meta.PlayerCount.Max {
		return nil, fmt.Errorf("座位数 %d 超过剧本上限 %d", len(players), sc.Meta.PlayerCount.Max)
	}
	// Rooms created before the seat range was enforced can still be short of the
	// script's minimum, so start re-checks it rather than leaving roles unplayed.
	if sc.Meta.PlayerCount.Min > 0 && len(players) < sc.Meta.PlayerCount.Min {
		return nil, fmt.Errorf("座位数 %d 少于剧本下限 %d", len(players), sc.Meta.PlayerCount.Min)
	}

	// Assign missing roles in join order.
	used := map[string]bool{}
	for _, s := range players {
		if s.RoleID != "" {
			used[s.RoleID] = true
		}
	}
	free := make([]string, 0, len(sc.Roles))
	for _, r := range sc.Roles {
		if !used[r.ID] {
			free = append(free, r.ID)
		}
	}
	// Drawing at random, not in join order: whoever opens the room joins first, and
	// handing them the first role in the script meant the host played the same
	// character in every single test game.
	rand.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })
	for i := range g.Seats {
		if g.Seats[i].Kind == domain.SeatSpectator || g.Seats[i].RoleID != "" {
			continue
		}
		if len(free) == 0 {
			return nil, errors.New("角色不足，请减少座位或补充剧本角色")
		}
		g.Seats[i].RoleID = free[0]
		free = free[1:]
	}
	g.Status = domain.RoomRunning
	m.store.PutGame(g)

	floor := m.cfg.Limits.FloorTimeout
	unlimited := room.SpeechSeconds < 0
	if room.SpeechSeconds > 0 {
		floor = time.Duration(room.SpeechSeconds) * time.Second
	}
	actor := engine.NewActor(g, sc, m.reg, m.store, m.hub, engine.ActorConfig{
		FloorTimeout: floor,
		AgentTimeout: m.cfg.Limits.AgentTimeout,
		// Rooms that did not opt into host-driven skipping keep auto-advancing.
		WaitForHost: room.WaitForHost,
		// -1 in speechSeconds means the table never puts a seat on a clock.
		Unlimited: unlimited,
		// Production tables are host-driven; SP_AUTO_ADVANCE only exists for
		// scripted tests and unattended demos.
		AutoAdvance: m.cfg.Limits.AutoAdvance,
		// A seat whose browser is gone gets its turn played for it instead of
		// freezing the table on a prompt nobody will answer.
		Presence:    m.hub.SeatPresence(g.ID),
		DiscussIdle: m.cfg.Limits.DiscussIdle,
		ReadingMin:  m.cfg.Limits.ReadingMin,
	})
	m.actors[room.ID] = actor
	m.byGame[g.ID] = actor
	m.born[room.ID] = time.Now()
	m.idle[room.ID] = 0

	room.Status = domain.RoomRunning
	room.UpdatedAt = time.Now().UTC()
	m.store.PutRoom(room)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("actor panic", "room", room.ID, "panic", r)
			}
		}()
		actor.Start(m.ctx)
	}()
	// As soon as the game reaches its final phase the actor must stop counting
	// against SP_MAX_ROOMS, otherwise a long-lived server refuses new games
	// after a handful of finished ones.
	go func() {
		select {
		case <-actor.Done():
			m.release(room, actor, g.ID)
		case <-m.ctx.Done():
		}
	}()
	return actor, nil
}

// release drops a finished actor and records the end on the room, so the
// lobby can tell a finished room from a running one even after a restart.
func (m *Manager) release(room *domain.Room, actor *engine.Actor, gameID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.actors[room.ID]; ok && cur == actor {
		delete(m.actors, room.ID)
	}
	if cur, ok := m.byGame[gameID]; ok && cur == actor {
		delete(m.byGame, gameID)
	}
	delete(m.born, room.ID)
	delete(m.idle, room.ID)
	stored, err := m.store.GetRoom(room.ID)
	if err != nil {
		return
	}
	stored.Status = domain.RoomFinished
	stored.UpdatedAt = time.Now().UTC()
	m.store.PutRoom(stored)
}

// recoverOrphans closes every room the store still calls "running" while no
// actor drives it. It runs once at startup: after a restart those rooms would
// otherwise sit in a state nobody can advance, invisible from the lobby list
// but still counted by every capacity check.
func (m *Manager) recoverOrphans() {
	for _, room := range m.store.ListRooms() {
		if room.GameID == "" {
			continue
		}
		g, err := m.store.GetGame(room.GameID)
		if err != nil {
			continue
		}
		if g.Status != domain.RoomRunning && room.Status != domain.RoomRunning {
			continue
		}
		now := time.Now().UTC()
		if g.Status == domain.RoomRunning {
			g.Status = domain.RoomFinished
			if g.EndedAt == nil {
				g.EndedAt = &now
			}
			g.AppendEvent(domain.Event{Type: domain.EvSystem, Text: "服务重启：这局没有人继续推进，已标记结束"})
			m.store.PutGame(g)
		}
		room.Status = domain.RoomFinished
		room.UpdatedAt = now
		m.store.PutRoom(room)
		slog.Info("rooms: closed an orphaned running room", "room", room.ID, "code", room.Code)
	}
}

// watch is the zombie reaper. A game whose seats have had no live socket for
// SP_ABANDON_IDLE_SEC, or which has simply run for SP_GAME_MAX_MIN, is closed
// so it stops holding one of the SP_MAX_ROOMS slots. Without it four abandoned
// games could block the whole platform with 409s that named no cause.
func (m *Manager) watch() {
	limits := m.cfg.Limits
	if limits.AbandonIdle <= 0 && limits.GameMax <= 0 {
		return
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		m.reap()
	}
}

func (m *Manager) reap() {
	limits := m.cfg.Limits
	type victim struct {
		actor  *engine.Actor
		reason string
	}
	var doomed []victim
	m.mu.Lock()
	for roomID, actor := range m.actors {
		if limits.GameMax > 0 {
			if born, ok := m.born[roomID]; ok && time.Since(born) > limits.GameMax {
				doomed = append(doomed, victim{actor, fmt.Sprintf("对局已超过最长时长 %d 分钟，自动结束", int(limits.GameMax.Minutes()))})
				continue
			}
		}
		if limits.AbandonIdle <= 0 {
			continue
		}
		if m.hub.Count(actor.GameID()) > 0 {
			m.idle[roomID] = 0
			continue
		}
		m.idle[roomID] += 15 * time.Second
		if m.idle[roomID] >= limits.AbandonIdle {
			doomed = append(doomed, victim{actor, fmt.Sprintf("全部玩家已离线 %d 秒，对局自动结束", int(limits.AbandonIdle.Seconds()))})
		}
	}
	m.mu.Unlock()
	// Close outside the lock: an actor persists its last event, which can take
	// the store lock, and the manager only needs the map removal afterwards.
	for _, v := range doomed {
		slog.Info("rooms: reclaiming an abandoned game", "game", v.actor.GameID(), "reason", v.reason)
		v.actor.Close(v.reason)
	}
}

// Running returns how many games currently hold a slot, and the cap.
func (m *Manager) Running() (running, max int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.actors), m.cfg.Limits.MaxRooms
}

// Actor returns the live actor of a room.
func (m *Manager) Actor(roomID string) (*engine.Actor, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actors[roomID]
	return a, ok
}

// ActorByGame returns the live actor of a game.
func (m *Manager) ActorByGame(gameID string) (*engine.Actor, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.byGame[gameID]
	return a, ok
}

// StateForSeat returns the filtered state of one seat, falling back to a
// lobby view when the game is not running yet.
func (m *Manager) StateForSeat(gameID, seatID string) (engine.SeatState, error) {
	if a, ok := m.ActorByGame(gameID); ok {
		if st, found := a.StateForSeat(seatID); found {
			return st, nil
		}
		return engine.SeatState{}, store.ErrNotFound
	}
	g, err := m.store.GetGame(gameID)
	if err != nil {
		return engine.SeatState{}, err
	}
	rec, err := m.store.GetScript(g.ScriptID)
	if err != nil {
		return engine.SeatState{}, err
	}
	if rec.Body == nil {
		return engine.SeatState{}, errors.New("剧本内容为空")
	}
	seat := g.SeatByID(seatID)
	if seat == nil {
		return engine.SeatState{}, store.ErrNotFound
	}
	v := engine.VisibleFor(g, rec.Body, seat, engine.VisibleOptions{IncludeTruth: false, MaxTranscript: 200})
	return engine.SeatState{
		Visible: v,
		Phases:  engine.NewMachine(rec.Body).Summary(g),
		Status:  g.Status,
	}, nil
}

// Handle dispatches a websocket command to the room actor.
func (m *Manager) Handle(ctx context.Context, gameID, seatID string, msg engine.ClientMessage) error {
	a, ok := m.ActorByGame(gameID)
	if !ok {
		return errors.New("对局尚未开始")
	}
	return a.Handle(ctx, seatID, msg)
}

// Stop marks a room finished and frees its actor slot.
func (m *Manager) Stop(roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.actors[roomID]
	if !ok {
		return
	}
	g := a.Snapshot()
	delete(m.actors, roomID)
	delete(m.byGame, g.ID)
	delete(m.born, roomID)
	delete(m.idle, roomID)
}

// CloseRoom ends a room on an admin's behalf. A running game is stopped in
// place so players keep the transcript for the replay; a lobby room is only
// marked finished, which takes it out of the joinable list.
func (m *Manager) CloseRoom(roomID, reason string) (*domain.Room, error) {
	room, err := m.store.GetRoom(roomID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	actor := m.actors[room.ID]
	m.mu.Unlock()
	if actor != nil {
		actor.Close(reason)
	} else if room.GameID != "" {
		// No actor means the game never started (or the server was restarted),
		// so the stored record is the only truth to close.
		if g, err := m.store.GetGame(room.GameID); err == nil && g.Status != domain.RoomFinished {
			g.Status = domain.RoomFinished
			if g.EndedAt == nil {
				now := time.Now().UTC()
				g.EndedAt = &now
			}
			m.store.PutGame(g)
		}
	}
	room.Status = domain.RoomFinished
	room.UpdatedAt = time.Now().UTC()
	m.store.PutRoom(room)
	return room, nil
}

// DeleteRoom removes a room and its game log for good.
func (m *Manager) DeleteRoom(roomID string) error {
	room, err := m.store.GetRoom(roomID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	actor := m.actors[room.ID]
	delete(m.actors, room.ID)
	delete(m.byGame, room.GameID)
	delete(m.born, room.ID)
	delete(m.idle, room.ID)
	m.mu.Unlock()
	// Close before deleting: persisting the final event after the records are
	// gone would resurrect the game in the store.
	if actor != nil {
		actor.Close("房间已被管理员删除")
	}
	m.store.DeleteGame(room.GameID)
	return m.store.DeleteRoom(room.ID)
}

// Close stops every actor (server shutdown).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actors = map[string]*engine.Actor{}
	m.byGame = map[string]*engine.Actor{}
	m.born = map[string]time.Time{}
	m.idle = map[string]time.Duration{}
}

// CloneGame deep copies a game (kept for callers that need isolation).
func CloneGame(g *domain.Game) *domain.Game {
	b, err := json.Marshal(g)
	if err != nil {
		return g
	}
	var out domain.Game
	if err := json.Unmarshal(b, &out); err != nil {
		return g
	}
	return &out
}
