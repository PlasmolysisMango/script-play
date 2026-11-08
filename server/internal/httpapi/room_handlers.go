package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"script-play/internal/auth"
	"script-play/internal/domain"
	"script-play/internal/engine"
	"script-play/internal/persona"
	"script-play/internal/store"
)

type seatView struct {
	ID          string `json:"id"`
	RoleID      string `json:"roleId,omitempty"`
	RoleName    string `json:"roleName,omitempty"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
	PersonaID   string `json:"personaId,omitempty"`
	ModelID     string `json:"modelId,omitempty"`
	IsHost      bool   `json:"isHost"`
	Connected   bool   `json:"connected"`
	Ready       bool   `json:"ready"`
	Mine        bool   `json:"mine"`
}

type roomView struct {
	ID             string            `json:"id"`
	Code           string            `json:"code"`
	Name           string            `json:"name"`
	Status         string            `json:"status"`
	GameID         string            `json:"gameId"`
	ScriptID       string            `json:"scriptId"`
	ScriptTitle    string            `json:"scriptTitle"`
	MaxSeats       int               `json:"maxSeats"`
	AllowSpectator bool              `json:"allowSpectator"`
	SpeechSeconds  int               `json:"speechSeconds"`
	AutoSkip       bool              `json:"autoSkip"`
	Host           bool              `json:"host"`
	Seats          []seatView        `json:"seats"`
	Roles          []roleView        `json:"roles"`
	MySeatID       string            `json:"mySeatId,omitempty"`
	Personas       []persona.Persona `json:"personas"`
}

type roleView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Intro     string `json:"intro"`
	PersonaID string `json:"personaId,omitempty"`
	Taken     bool   `json:"taken"`
}

func (s *Server) roomView(room *domain.Room, g *domain.Game, rec *domain.ScriptRecord, userID string) roomView {
	v := roomView{
		ID:             room.ID,
		Code:           room.Code,
		Name:           room.Name,
		Status:         room.Status,
		GameID:         room.GameID,
		ScriptID:       room.ScriptID,
		MaxSeats:       room.MaxSeats,
		AllowSpectator: room.AllowSpectator,
		SpeechSeconds:  room.SpeechSeconds,
		AutoSkip:       !room.WaitForHost,
		Host:           room.HostUserID == userID,
		Personas:       persona.Presets(),
		// Never serialise as null: json clients parse these as arrays.
		Seats: []seatView{},
		Roles: []roleView{},
	}
	// Once a game runs, its status is the truth: the actor flips the game to
	// finished and the room record is only a lobby-era snapshot.
	if g != nil && g.Status != "" {
		v.Status = g.Status
	}
	if rec != nil && rec.Body != nil {
		v.ScriptTitle = rec.Body.Meta.Title
		for _, r := range rec.Body.Roles {
			v.Roles = append(v.Roles, roleView{ID: r.ID, Name: r.Name, Intro: r.Public.Intro, PersonaID: r.PersonaID})
		}
	}
	taken := map[string]bool{}
	for _, seat := range g.Seats {
		if seat.RoleID != "" {
			taken[seat.RoleID] = true
		}
	}
	for i := range v.Roles {
		v.Roles[i].Taken = taken[v.Roles[i].ID]
	}
	// 卡牌本翻真相前，谁是谁就是积分卡的答案：房间页的座位列表也得跟着藏，
	// 留着自己的那份方便看本。
	hidden := g != nil && rec != nil && rec.Body != nil && engine.HiddenIdentities(g, rec.Body)
	for _, seat := range g.Seats {
		mine := seat.UserID != "" && seat.UserID == userID
		sv := seatView{
			ID:          seat.ID,
			RoleID:      seat.RoleID,
			DisplayName: seat.DisplayName,
			Kind:        seat.Kind,
			PersonaID:   seat.PersonaID,
			IsHost:      seat.IsHost,
			Connected:   seat.Connected,
			Ready:       seat.Ready,
			Mine:        mine,
		}
		if seat.UserID == userID {
			sv.ModelID = seat.ModelID
			v.MySeatID = seat.ID
		}
		if hidden && !mine {
			sv.RoleID = ""
		} else if r := findRole(rec, seat.RoleID); r != nil {
			sv.RoleName = r.Name
		}
		v.Seats = append(v.Seats, sv)
	}
	// Host first, everyone else keeps the join order.
	sort.SliceStable(v.Seats, func(i, j int) bool {
		return v.Seats[i].IsHost && !v.Seats[j].IsHost
	})
	return v
}

func findRole(rec *domain.ScriptRecord, roleID string) *domain.Role {
	if rec == nil || rec.Body == nil || roleID == "" {
		return nil
	}
	for i := range rec.Body.Roles {
		if rec.Body.Roles[i].ID == roleID {
			return &rec.Body.Roles[i]
		}
	}
	return nil
}

// ---- room lookup helpers ----

type roomCtx struct {
	room *domain.Room
	game *domain.Game
	rec  *domain.ScriptRecord
	seat *domain.Seat
}

func (s *Server) loadRoom(w http.ResponseWriter, r *http.Request) (*roomCtx, bool) {
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))
	room, err := s.store.RoomByCode(code)
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("房间不存在"))
		return nil, false
	}
	g, err := s.rooms.Game(room.GameID)
	if err != nil {
		fail(w, http.StatusInternalServerError, errors.New("房间状态丢失"))
		return nil, false
	}
	rec, _ := s.store.GetScript(room.ScriptID)
	// The actor owns the lifecycle after the lobby: the stored room record is a
	// lobby-era snapshot, so every handler below reads the status from the game.
	if g.Status != "" && g.Status != room.Status {
		cp := *room
		cp.Status = g.Status
		room = &cp
	}
	u := currentUser(r)
	var seat *domain.Seat
	for i := range g.Seats {
		if g.Seats[i].UserID == u.ID {
			cp := g.Seats[i]
			seat = &cp
			break
		}
	}
	return &roomCtx{room: room, game: g, rec: rec, seat: seat}, true
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	all := s.store.ListRooms()
	out := make([]roomView, 0, len(all))
	for _, room := range all {
		g, err := s.rooms.Game(room.GameID)
		if err != nil {
			continue
		}
		member := room.HostUserID == u.ID
		for _, seat := range g.Seats {
			if seat.UserID == u.ID {
				member = true
			}
		}
		if !member && g.Status != domain.RoomLobby {
			continue
		}
		rec, _ := s.store.GetScript(room.ScriptID)
		out = append(out, s.roomView(room, g, rec, u.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		Name           string `json:"name"`
		ScriptID       string `json:"scriptId"`
		MaxSeats       int    `json:"maxSeats"`
		AllowSpectator bool   `json:"allowSpectator"`
		SpeechSeconds  int    `json:"speechSeconds"`
		AutoSkip       *bool  `json:"autoSkip"`
	}
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	rec, err := s.store.GetScript(req.ScriptID)
	if err != nil || rec.Status != "published" || rec.Body == nil {
		fail(w, http.StatusBadRequest, errors.New("请选择一个已发布的剧本"))
		return
	}
	// The seat count is dictated by the script: a fixed cast admits exactly that
	// many players, a flexible one stays inside its advertised range. Anything
	// else would either strand roles or run out of them at start.
	pc := rec.Body.Meta.PlayerCount
	if req.MaxSeats <= 0 {
		req.MaxSeats = pc.Max
	}
	if pc.Min == pc.Max && req.MaxSeats != pc.Max {
		fail(w, http.StatusBadRequest, fmt.Errorf("《%s》固定 %d 人，座位数必须为 %d", rec.Body.Meta.Title, pc.Max, pc.Max))
		return
	}
	if req.MaxSeats < pc.Min || req.MaxSeats > pc.Max {
		fail(w, http.StatusBadRequest, fmt.Errorf("座位数需在 %d~%d 之间（%s 支持的人数）", pc.Min, pc.Max, rec.Body.Meta.Title))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = rec.Body.Meta.Title
	}
	// 0 keeps the server default, -1 removes the per-turn clock entirely,
	// anything else is a deliberate per-turn limit.
	if req.SpeechSeconds < -1 || req.SpeechSeconds > 600 || (req.SpeechSeconds > 0 && req.SpeechSeconds < 10) {
		fail(w, http.StatusBadRequest, errors.New("发言限时需为 10~600 秒、0（服务端默认）或 -1（不限时）"))
		return
	}
	autoSkip := true
	if req.AutoSkip != nil {
		autoSkip = *req.AutoSkip
	}

	g, err := s.rooms.CreateGame("", rec.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	room := &domain.Room{
		ID:             store.NewID("room"),
		Code:           s.uniqueCode(),
		Name:           req.Name,
		HostUserID:     u.ID,
		ScriptID:       rec.ID,
		Status:         domain.RoomLobby,
		MaxSeats:       req.MaxSeats,
		AllowSpectator: req.AllowSpectator,
		SpeechSeconds:  req.SpeechSeconds,
		WaitForHost:    !autoSkip,
		GameID:         g.ID,
		CreatedAt:      time.Now().UTC(),
	}
	g.RoomID = room.ID
	hostSeat := domain.Seat{
		ID:          store.NewID("seat"),
		RoomID:      room.ID,
		Kind:        domain.SeatHuman,
		UserID:      u.ID,
		DisplayName: u.DisplayName,
		PersonaID:   "guarded",
		IsHost:      true,
		Connected:   true,
		JoinedAt:    time.Now().UTC(),
	}
	s.store.PutRoom(room)
	if _, err := s.rooms.AddSeat(g.ID, hostSeat); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	// Re-read instead of writing the pre-seat snapshot back: AddSeat owns
	// the stored copy.
	g, _ = s.rooms.Game(g.ID)
	view := s.roomView(room, g, rec, u.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"room": view})
}

func (s *Server) uniqueCode() string {
	for i := 0; i < 20; i++ {
		code := store.NewCode()
		if _, err := s.store.RoomByCode(code); err != nil {
			return code
		}
	}
	return store.NewCode()
}

func (s *Server) handleGetRoom(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{"room": s.roomView(rc.room, rc.game, rc.rec, u.ID)})
}

func (s *Server) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if rc.seat != nil {
		writeJSON(w, http.StatusOK, map[string]any{"room": s.roomView(rc.room, rc.game, rc.rec, u.ID), "rejoined": true})
		return
	}
	var req struct {
		DisplayName string `json:"displayName"`
		RoleID      string `json:"roleId"`
		PersonaID   string `json:"personaId"`
		Spectator   bool   `json:"spectator"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = u.DisplayName
	}

	kind := domain.SeatHuman
	if rc.room.Status != domain.RoomLobby || req.Spectator {
		if !rc.room.AllowSpectator {
			fail(w, http.StatusConflict, errors.New("对局已开始且不允许观战"))
			return
		}
		kind = domain.SeatSpectator
	}
	if kind == domain.SeatHuman {
		players := len(rc.game.PlayerSeats())
		if players >= rc.room.MaxSeats {
			fail(w, http.StatusConflict, errors.New("座位已满"))
			return
		}
		if req.RoleID != "" {
			for _, seat := range rc.game.Seats {
				if seat.RoleID == req.RoleID && seat.Kind != domain.SeatSpectator {
					fail(w, http.StatusConflict, errors.New("该角色已被占用"))
					return
				}
			}
			if findRole(rc.rec, req.RoleID) == nil {
				fail(w, http.StatusBadRequest, errors.New("角色不存在"))
				return
			}
		}
	}
	seat := domain.Seat{
		Kind:        kind,
		UserID:      u.ID,
		DisplayName: req.DisplayName,
		PersonaID:   req.PersonaID,
		RoleID:      req.RoleID,
		Connected:   true,
	}
	created, err := s.rooms.AddSeat(rc.game.ID, seat)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	g, _ := s.rooms.Game(rc.game.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"room": s.roomView(rc.room, g, rc.rec, u.ID), "seatId": created.ID})
}

func (s *Server) handleAddSeat(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if rc.room.HostUserID != u.ID {
		fail(w, http.StatusForbidden, errors.New("只有主持人可以添加座位"))
		return
	}
	var req struct {
		Kind        string `json:"kind"`
		DisplayName string `json:"displayName"`
		RoleID      string `json:"roleId"`
		PersonaID   string `json:"personaId"`
		ModelID     string `json:"modelId"`
	}
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if req.Kind == "" {
		req.Kind = domain.SeatAgent
	}
	if req.Kind != domain.SeatAgent {
		fail(w, http.StatusBadRequest, errors.New("只能添加 AI 座位，真人请用房号加入"))
		return
	}
	if req.ModelID == "" {
		req.ModelID = s.cfg.LLM.Qoder.Model
	}
	req.PersonaID = persona.Resolve(req.PersonaID)
	if req.DisplayName == "" {
		req.DisplayName = "AI·" + persona.Get(req.PersonaID).Name
	}
	created, err := s.rooms.AddSeat(rc.game.ID, domain.Seat{
		Kind:        domain.SeatAgent,
		DisplayName: req.DisplayName,
		RoleID:      req.RoleID,
		PersonaID:   req.PersonaID,
		ModelID:     req.ModelID,
		Connected:   true,
	})
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	g, _ := s.rooms.Game(rc.game.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"room": s.roomView(rc.room, g, rc.rec, u.ID), "seatId": created.ID})
}

func (s *Server) handlePatchSeat(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	var req struct {
		DisplayName *string `json:"displayName"`
		RoleID      *string `json:"roleId"`
		PersonaID   *string `json:"personaId"`
		ModelID     *string `json:"modelId"`
		Ready       *bool   `json:"ready"`
	}
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	seatID := r.PathValue("seatId")
	target := rc.game.SeatByID(seatID)
	if target == nil {
		fail(w, http.StatusNotFound, errors.New("座位不存在"))
		return
	}
	if rc.room.HostUserID != u.ID {
		// Players may claim a different role or mark themselves ready; every
		// other field stays a host decision.
		if target.UserID == "" || target.UserID != u.ID {
			fail(w, http.StatusForbidden, errors.New("只能修改自己的座位"))
			return
		}
		if req.DisplayName != nil || req.PersonaID != nil || req.ModelID != nil {
			fail(w, http.StatusForbidden, errors.New("只有主持人可以修改昵称、性格与模型"))
			return
		}
	}
	_, err := s.rooms.UpdateSeat(rc.game.ID, seatID, func(seat *domain.Seat) error {
		if req.DisplayName != nil {
			seat.DisplayName = *req.DisplayName
		}
		if req.PersonaID != nil {
			seat.PersonaID = persona.Resolve(*req.PersonaID)
		}
		if req.ModelID != nil {
			seat.ModelID = *req.ModelID
		}
		if req.Ready != nil {
			seat.Ready = *req.Ready
		}
		if req.RoleID != nil {
			roleID := *req.RoleID
			if roleID != "" && findRole(rc.rec, roleID) == nil {
				return errors.New("角色不存在")
			}
			for _, other := range rc.game.Seats {
				if other.ID != seat.ID && other.RoleID == roleID && roleID != "" {
					return errors.New("该角色已被其他座位占用")
				}
			}
			seat.RoleID = roleID
		}
		return nil
	})
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	g, _ := s.rooms.Game(rc.game.ID)
	writeJSON(w, http.StatusOK, map[string]any{"room": s.roomView(rc.room, g, rc.rec, u.ID)})
}

func (s *Server) handleDeleteSeat(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if rc.room.HostUserID != u.ID {
		fail(w, http.StatusForbidden, errors.New("只有主持人可以移除座位"))
		return
	}
	seatID := r.PathValue("seatId")
	if rc.seat != nil && rc.seat.ID == seatID {
		fail(w, http.StatusBadRequest, errors.New("不能移除自己的主持人座位"))
		return
	}
	if err := s.rooms.RemoveSeat(rc.game.ID, seatID); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	g, _ := s.rooms.Game(rc.game.ID)
	writeJSON(w, http.StatusOK, map[string]any{"room": s.roomView(rc.room, g, rc.rec, u.ID)})
}

func (s *Server) handleStartRoom(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if rc.room.HostUserID != u.ID {
		fail(w, http.StatusForbidden, errors.New("只有主持人可以开始"))
		return
	}
	if rc.rec == nil || rc.rec.Status != "published" {
		fail(w, http.StatusBadRequest, errors.New("剧本未发布"))
		return
	}
	if _, err := s.rooms.Start(rc.room, rc.rec); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	room, _ := s.store.GetRoom(rc.room.ID)
	g, _ := s.rooms.Game(room.GameID)
	writeJSON(w, http.StatusOK, map[string]any{"room": s.roomView(room, g, rc.rec, u.ID)})
}

func (s *Server) handleRoomState(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	seatID := r.URL.Query().Get("seatId")
	if seatID == "" && rc.seat != nil {
		seatID = rc.seat.ID
	}
	if seatID == "" {
		fail(w, http.StatusBadRequest, errors.New("你还不在这个房间"))
		return
	}
	if rc.seat == nil {
		fail(w, http.StatusForbidden, errors.New("无权查看该座位状态"))
		return
	}
	st, err := s.rooms.StateForSeat(rc.game.ID, seatID)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": st,
		"room":  s.roomView(rc.room, rc.game, rc.rec, currentUser(r).ID),
	})
}

// handleSeatTicket mints a short-lived websocket ticket bound to one seat.
//
// Browsers cannot set headers on a WebSocket handshake and a long-lived session
// token in a URL ends up in logs and history, so the client trades its session
// for a ticket scoped to (game, seat) and re-mints it on reconnect.
func (s *Server) handleSeatTicket(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if rc.seat == nil {
		fail(w, http.StatusForbidden, errNoSeat)
		return
	}
	ttl := time.Hour
	ticket, exp, err := auth.SignSeatTicket(s.cfg.JWTSecret, u.ID, u.DisplayName, rc.game.ID, rc.seat.ID, ttl)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":    ticket,
		"seatId":    rc.seat.ID,
		"gameId":    rc.game.ID,
		"expiresAt": exp,
		"wsPath":    "/ws/rooms/" + rc.room.Code + "?ticket=" + ticket,
	})
}

// handleReplay returns the full log for the host after the game ends.
func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	rc, ok := s.loadRoom(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if rc.room.HostUserID != u.ID {
		fail(w, http.StatusForbidden, errors.New("只有主持人可以查看完整回放"))
		return
	}
	g, err := s.rooms.Game(rc.game.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if g.Status != domain.RoomFinished {
		fail(w, http.StatusConflict, errors.New("对局尚未结束"))
		return
	}
	if rc.rec == nil || rc.rec.Body == nil {
		fail(w, http.StatusInternalServerError, errors.New("剧本内容丢失"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"game":   g,
		"truth":  rc.rec.Body.Truth,
		"phases": engine.NewMachine(rc.rec.Body).Summary(g),
		"replay": g.Events,
	})
}
