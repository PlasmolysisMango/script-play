package httpapi

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"script-play/internal/auth"
	"script-play/internal/config"
	"script-play/internal/domain"
	"script-play/internal/store"
)

// EnsureAdmin seeds the single account allowed into the admin console.
//
// With SP_ADMIN_USERNAME alone an already registered account is promoted; with
// SP_ADMIN_PASSWORD as well the account is created (or its password reset) on
// start, which is how a fresh deployment gets its first administrator.
// SP_ADMIN_DISPLAY_NAME only renames the account, so it can be applied to an
// existing record on the next restart.
func EnsureAdmin(cfg *config.Config, st *store.Store) error {
	username := strings.TrimSpace(cfg.Admin.Username)
	if username == "" {
		return nil
	}
	nickname := strings.TrimSpace(cfg.Admin.DisplayName)
	if nickname == "" {
		nickname = username
	}
	hash, err := hashIfSet(cfg.Admin.Password)
	if err != nil {
		return err
	}

	u, err := st.UserByUsername(username)
	if err == nil {
		changed := false
		if u.Role != domain.RoleAdmin {
			u.Role = domain.RoleAdmin
			changed = true
		}
		// Empty DisplayName falls back to the username, so an account that was
		// registered with its own nickname keeps it until the env asks otherwise.
		if strings.TrimSpace(cfg.Admin.DisplayName) != "" && u.DisplayName != nickname {
			u.DisplayName = nickname
			changed = true
		}
		// Only rewrite the hash when the configured password actually differs,
		// so restarting with the same env does not churn the stored record.
		if hash != "" && !auth.CheckPassword(u.PasswordHash, cfg.Admin.Password) {
			u.PasswordHash = hash
			changed = true
		}
		if !changed {
			return nil
		}
		return st.PutUser(u)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if hash == "" {
		return fmt.Errorf("管理员 %q 尚未注册：请先注册该账号，或设置 SP_ADMIN_PASSWORD 让服务端创建", username)
	}
	created := &domain.User{
		ID:           store.NewID("user"),
		Username:     username,
		DisplayName:  nickname,
		PasswordHash: hash,
		Role:         domain.RoleAdmin,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateUser(created); err != nil {
		return err
	}
	slog.Info("admin account created", "username", username)
	return nil
}

func hashIfSet(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	return auth.HashPassword(pw)
}

// adminUserView is one row of the account table. The password hash is
// deliberately absent: it is one-way, so the console offers a reset instead.
type adminUserView struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	Role        string     `json:"role"`
	Scripts     int        `json:"scripts"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
	Me          bool       `json:"me"`
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	me := currentUser(r)
	users := s.store.ListUsers()
	out := make([]adminUserView, 0, len(users))
	for _, u := range users {
		out = append(out, adminUserView{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Role:        roleOf(u),
			Scripts:     s.store.CountScriptsBy(u.ID),
			CreatedAt:   u.CreatedAt,
			LastLoginAt: u.LastLoginAt,
			Me:          u.ID == me.ID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// handleAdminPurgeUsers deletes every non-admin account. Admin accounts are
// never touched, and a script owned by a removed account is handed to the
// acting admin instead of disappearing with its author.
func (s *Server) handleAdminPurgeUsers(w http.ResponseWriter, r *http.Request) {
	me := currentUser(r)
	deleted, reassigned, failed := 0, 0, 0
	var problems []string
	for _, u := range s.store.ListUsers() {
		if u.Role == domain.RoleAdmin {
			continue
		}
		for _, rec := range s.store.ListScripts(u.ID) {
			// ListScripts also returns every published script, including ones
			// this account does not own; only move what would be orphaned.
			if rec.OwnerUserID != u.ID {
				continue
			}
			rec.OwnerUserID = me.ID
			s.store.PutScript(rec)
			reassigned++
		}
		if err := s.store.DeleteUser(u.ID); err != nil {
			failed++
			problems = append(problems, u.Username+": "+err.Error())
			continue
		}
		deleted++
	}
	slog.Info("admin purged accounts", "deleted", deleted, "scriptsReassigned", reassigned, "by", me.Username)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":           deleted,
		"scriptsReassigned": reassigned,
		"failed":            failed,
		"errors":            problems,
	})
}

// handleAdminResetPassword issues a new password. When the request omits one a
// temporary password is generated and returned exactly once, so the admin can
// hand it over without the server ever storing a readable secret.
func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, err)
		return
	}
	pw := req.Password
	generated := false
	if pw == "" {
		var err error
		if pw, err = tempPassword(10); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		generated = true
	}
	if len(pw) < 6 {
		fail(w, http.StatusBadRequest, errors.New("密码至少 6 位"))
		return
	}
	u, err := s.store.UserByID(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("账号不存在"))
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	u.PasswordHash = hash
	if err := s.store.PutUser(u); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("admin reset password", "target", u.Username, "by", currentUser(r).Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "password": pw, "generated": generated})
}

func tempPassword(n int) (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b), nil
}

// adminRoomView is one row of the room table. It merges the lobby record with
// the live game so an admin sees the truth even mid-game.
type adminRoomView struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	ScriptTitle string    `json:"scriptTitle"`
	GameID      string    `json:"gameId,omitempty"`
	HostName    string    `json:"hostName"`
	Seats       int       `json:"seats"`
	Players     int       `json:"players"`
	Running     bool      `json:"running"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *Server) handleAdminRooms(w http.ResponseWriter, r *http.Request) {
	rooms := s.store.ListRooms()
	out := make([]adminRoomView, 0, len(rooms))
	for _, room := range rooms {
		view := adminRoomView{
			ID:        room.ID,
			Code:      room.Code,
			Name:      room.Name,
			Status:    room.Status,
			GameID:    room.GameID,
			CreatedAt: room.CreatedAt,
		}
		if rec, err := s.store.GetScript(room.ScriptID); err == nil && rec.Body != nil {
			view.ScriptTitle = rec.Body.Meta.Title
		}
		if host, err := s.store.UserByID(room.HostUserID); err == nil {
			view.HostName = host.DisplayName
		}
		if room.GameID != "" {
			if g, err := s.rooms.Game(room.GameID); err == nil {
				view.Status = g.Status
				view.Seats = len(g.Seats)
				view.Players = len(g.PlayerSeats())
			}
		}
		_, view.Running = s.rooms.Actor(room.ID)
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

// handleAdminPurgeRooms deletes rooms in bulk. Without {"all":true} only rooms
// with no live actor are removed, so a lobby cleanup cannot stop a running
// game; the full sweep closes actors before deleting their records.
func (s *Server) handleAdminPurgeRooms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		All bool `json:"all"`
	}
	if err := decode(r, &req); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, err)
		return
	}
	deleted, skipped, failed := 0, 0, 0
	var problems []string
	for _, room := range s.store.ListRooms() {
		if !req.All {
			if _, running := s.rooms.Actor(room.ID); running {
				skipped++
				continue
			}
		}
		if err := s.rooms.DeleteRoom(room.ID); err != nil {
			failed++
			problems = append(problems, room.Code+": "+err.Error())
			continue
		}
		deleted++
	}
	slog.Info("admin purged rooms", "deleted", deleted, "skipped", skipped, "all", req.All, "by", currentUser(r).Username)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"skipped": skipped,
		"failed":  failed,
		"errors":  problems,
	})
}

func (s *Server) handleAdminCloseRoom(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.RoomByCode(r.PathValue("code"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("房间不存在"))
		return
	}
	if room.Status == domain.RoomFinished {
		writeJSON(w, http.StatusOK, map[string]any{"room": room, "changed": false})
		return
	}
	closed, err := s.rooms.CloseRoom(room.ID, "管理员关闭了房间")
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("admin closed room", "code", closed.Code, "by", currentUser(r).Username)
	writeJSON(w, http.StatusOK, map[string]any{"room": closed, "changed": true})
}

func (s *Server) handleAdminDeleteRoom(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.RoomByCode(r.PathValue("code"))
	if err != nil {
		fail(w, http.StatusNotFound, errors.New("房间不存在"))
		return
	}
	if err := s.rooms.DeleteRoom(room.ID); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("admin deleted room", "code", room.Code, "by", currentUser(r).Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
