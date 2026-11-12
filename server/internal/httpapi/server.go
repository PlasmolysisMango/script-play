// Package httpapi exposes the REST and websocket API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"script-play/internal/auth"
	"script-play/internal/config"
	"script-play/internal/domain"
	"script-play/internal/importer"
	"script-play/internal/llm"
	"script-play/internal/persona"
	"script-play/internal/rooms"
	"script-play/internal/store"
)

type ctxKey string

const userKey ctxKey = "user"

// Server bundles the dependencies of every handler.
type Server struct {
	cfg   *config.Config
	store *store.Store
	reg   *llm.Registry
	rooms *rooms.Manager
	imp   *importer.Service
	mux   *http.ServeMux
}

// New builds the API.
func New(cfg *config.Config, st *store.Store, reg *llm.Registry, rm *rooms.Manager, imp *importer.Service) *Server {
	s := &Server{cfg: cfg, store: st, reg: reg, rooms: rm, imp: imp, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler returns the root middleware chain.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	if s.cfg.WebDir != "" {
		files := http.FileServer(http.Dir(s.cfg.WebDir))
		h = spaHandler(files, s.cfg.WebDir, s.mux)
	}
	return cors(h)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("POST /api/auth/register", s.handleRegister)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/refresh", s.handleRefresh)
	m.HandleFunc("GET /api/me", s.withAuth(s.handleMe))
	m.HandleFunc("GET /api/personas", s.withAuth(s.handlePersonas))
	m.HandleFunc("GET /api/providers", s.withAuth(s.handleProviders))

	m.HandleFunc("GET /api/scripts", s.withAuth(s.handleListScripts))
	// The script library is curated: only admins import or edit scripts, while
	// every signed-in user can list and read the published ones to host a room.
	m.HandleFunc("POST /api/scripts", s.withAdmin(s.handleCreateScript))
	m.HandleFunc("GET /api/scripts/{id}", s.withAuth(s.handleGetScript))
	m.HandleFunc("PUT /api/scripts/{id}", s.withAdmin(s.handleUpdateScript))
	m.HandleFunc("POST /api/scripts/{id}/publish", s.withAdmin(s.handlePublishScript))
	m.HandleFunc("POST /api/scripts/{id}/draft", s.withAdmin(s.handleForkScript))
	m.HandleFunc("DELETE /api/scripts/{id}", s.withAdmin(s.handleDeleteScript))
	// A clue is often a picture. Uploads land in data/media/<script id>/ and the
	// body stores the relative URL, so a script stays portable.
	m.HandleFunc("POST /api/scripts/{id}/media", s.withAdmin(s.handleUploadScriptMedia))
	m.HandleFunc("GET /media/{id}/{name}", s.handleMediaFile)
	// Pictures committed beside the script (scripts/<dir>/images/…) are served
	// straight from the repo, so the body and its assets travel together.
	m.HandleFunc("GET /script-assets/{script}/{file...}", s.handleScriptAsset)

	// Raw material is archived, not parsed: uploading drops the original in
	// data/inbox for a developer to convert into the standard JSON offline.
	m.HandleFunc("GET /api/admin/materials", s.withAdmin(s.handleListMaterials))
	m.HandleFunc("POST /api/admin/materials", s.withAdmin(s.handleUploadMaterials))
	m.HandleFunc("DELETE /api/admin/materials/{id}", s.withAdmin(s.handleDeleteMaterial))

	m.HandleFunc("GET /api/admin/users", s.withAdmin(s.handleAdminUsers))
	m.HandleFunc("POST /api/admin/users/purge", s.withAdmin(s.handleAdminPurgeUsers))
	m.HandleFunc("POST /api/admin/users/{id}/password", s.withAdmin(s.handleAdminResetPassword))
	m.HandleFunc("GET /api/admin/rooms", s.withAdmin(s.handleAdminRooms))
	m.HandleFunc("POST /api/admin/rooms/purge", s.withAdmin(s.handleAdminPurgeRooms))
	m.HandleFunc("POST /api/admin/rooms/{code}/close", s.withAdmin(s.handleAdminCloseRoom))
	m.HandleFunc("DELETE /api/admin/rooms/{code}", s.withAdmin(s.handleAdminDeleteRoom))

	m.HandleFunc("GET /api/rooms", s.withAuth(s.handleListRooms))
	m.HandleFunc("POST /api/rooms", s.withAuth(s.handleCreateRoom))
	m.HandleFunc("GET /api/rooms/{code}", s.withAuth(s.handleGetRoom))
	m.HandleFunc("POST /api/rooms/{code}/join", s.withAuth(s.handleJoinRoom))
	m.HandleFunc("POST /api/rooms/{code}/seats", s.withAuth(s.handleAddSeat))
	m.HandleFunc("PATCH /api/rooms/{code}/seats/{seatId}", s.withAuth(s.handlePatchSeat))
	m.HandleFunc("DELETE /api/rooms/{code}/seats/{seatId}", s.withAuth(s.handleDeleteSeat))
	m.HandleFunc("POST /api/rooms/{code}/start", s.withAuth(s.handleStartRoom))
	// The host's own lifecycle controls: give up a seat, end a game that
	// cannot finish itself, hand the table over. Without them a normal user
	// had no way to release a stuck room (only admins could).
	m.HandleFunc("POST /api/rooms/{code}/leave", s.withAuth(s.handleLeaveRoom))
	m.HandleFunc("POST /api/rooms/{code}/close", s.withAuth(s.handleCloseRoom))
	m.HandleFunc("POST /api/rooms/{code}/host", s.withAuth(s.handleTransferHost))
	m.HandleFunc("GET /api/rooms/{code}/seat-ticket", s.withAuth(s.handleSeatTicket))
	m.HandleFunc("GET /api/rooms/{code}/state", s.withAuth(s.handleRoomState))
	m.HandleFunc("GET /api/rooms/{code}/replay", s.withAuth(s.handleReplay))
	m.HandleFunc("GET /ws/rooms/{code}", s.handleWS)
}

// ---- middleware ----

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		claims, err := auth.Verify(s.cfg.JWTSecret, token)
		if err != nil || !isAccessToken(claims) {
			fail(w, http.StatusUnauthorized, errors.New("登录已失效，请重新登录"))
			return
		}
		u, err := s.store.UserByID(claims.Sub)
		if err != nil {
			fail(w, http.StatusUnauthorized, errors.New("账号不存在"))
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next(w, r.WithContext(ctx))
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// withAdmin wraps a handler with authentication plus the admin role. The role
// is read from the store on every request, never from the token, so demoting an
// account takes effect immediately instead of waiting for the token to expire.
func (s *Server) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			fail(w, http.StatusForbidden, errors.New("需要管理员权限"))
			return
		}
		next(w, r)
	})
}

func currentUser(r *http.Request) *domain.User {
	u, _ := r.Context().Value(userKey).(*domain.User)
	return u
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write json failed", "err", err)
	}
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// spaHandler serves the built web app, falling back to index.html.
func spaHandler(files http.Handler, dir string, api *http.ServeMux) http.Handler {
	root := filepath.Clean(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") ||
			strings.HasPrefix(r.URL.Path, "/media/") || strings.HasPrefix(r.URL.Path, scriptAssetsPrefix) {
			api.ServeHTTP(w, r)
			return
		}
		if fileExists(root, r.URL.Path) {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				// Vite fingerprints asset names, so a cached copy can never go stale.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		// Answering a missing asset with index.html turns a plain 404 into a
		// confusing "expected JavaScript module, got text/html" error.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		// The shell must never be cached: it is the only thing that points at the
		// current asset names, so a stale copy pins the browser to an old bundle.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}

// fileExists maps a URL path onto the web root, refusing anything that escapes it.
func fileExists(root, urlPath string) bool {
	p := filepath.Join(root, filepath.Clean("/"+urlPath))
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ---- basic handlers ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	running, max := s.rooms.Running()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"time":      time.Now().UTC(),
		"providers": s.reg.Names(),
		"default":   s.reg.Default(),
		// The lobby prints this next to "服务端默认" so hosts know the number
		// without reading SP_FLOOR_TIMEOUT_SEC.
		"floorTimeoutSec": int(s.cfg.Limits.FloorTimeout / time.Second),
		"agentTimeoutSec": int(s.cfg.Limits.AgentTimeout / time.Second),
		// Capacity and the automatic-reclaim timeouts, so an operator can see why
		// a start was refused and when an abandoned table frees its slot.
		"maxRooms":          max,
		"runningRooms":      running,
		"discussionIdleSec": int(s.cfg.Limits.DiscussIdle / time.Second),
		"abandonIdleSec":    int(s.cfg.Limits.AbandonIdle / time.Second),
		"gameMaxMin":        int(s.cfg.Limits.GameMax / time.Minute),
		"readingMinSec":     int(s.cfg.Limits.ReadingMin / time.Second),
	})
}

func (s *Server) handlePersonas(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"personas": persona.Presets()})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": s.reg.Names(),
		"default":   s.reg.Default(),
		"qoder":     map[string]string{"bin": s.cfg.LLM.Qoder.Bin, "model": s.cfg.LLM.Qoder.Model},
		"openai":    map[string]string{"baseUrl": s.cfg.LLM.OpenAI.BaseURL, "model": s.cfg.LLM.OpenAI.Model},
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, publicUser(currentUser(r)))
}

type authRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Password) < 6 {
		fail(w, http.StatusBadRequest, errors.New("用户名至少 3 位，密码至少 6 位"))
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = req.Username
	}
	u := &domain.User{
		ID:           store.NewID("user"),
		Username:     req.Username,
		DisplayName:  req.DisplayName,
		PasswordHash: hash,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.store.CreateUser(u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(w, http.StatusConflict, errors.New("用户名已被占用"))
			return
		}
		fail(w, http.StatusInternalServerError, err)
		return
	}
	s.writeSession(w, http.StatusCreated, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.store.UserByUsername(req.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		fail(w, http.StatusUnauthorized, errors.New("用户名或密码不正确"))
		return
	}
	// Best effort: the admin console uses this to tell active accounts from
	// dormant ones, so a failed write must never block the login.
	signed := time.Now().UTC()
	u.LastLoginAt = &signed
	if err := s.store.PutUser(u); err != nil {
		slog.Debug("record last login failed", "err", err)
	}
	s.writeSession(w, http.StatusOK, u)
}

// handleRefresh trades a valid refresh token for a fresh pair. It is the only
// session endpoint that does not require a live access token, because the whole
// point is to replace an expired one.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	claims, err := auth.Verify(s.cfg.JWTSecret, req.RefreshToken)
	if err != nil || claims.Purpose != auth.PurposeRefresh {
		fail(w, http.StatusUnauthorized, errors.New("刷新票据无效，请重新登录"))
		return
	}
	u, err := s.store.UserByID(claims.Sub)
	if err != nil {
		fail(w, http.StatusUnauthorized, errors.New("账号不存在"))
		return
	}
	s.writeSession(w, http.StatusOK, u)
}

func (s *Server) issueToken(u *domain.User) (string, error) {
	token, _, _, err := s.issueSession(u)
	return token, err
}

// issueSession mints the access/refresh pair returned by every login path.
func (s *Server) issueSession(u *domain.User) (access, refresh string, exp time.Time, err error) {
	access, exp, err = auth.SignSession(s.cfg.JWTSecret, u.ID, u.DisplayName, auth.PurposeAccess, s.accessTTL())
	if err != nil {
		return "", "", time.Time{}, err
	}
	refresh, _, err = auth.SignSession(s.cfg.JWTSecret, u.ID, u.DisplayName, auth.PurposeRefresh, s.refreshTTL())
	if err != nil {
		return "", "", time.Time{}, err
	}
	return access, refresh, exp, nil
}

// writeSession is the single response shape for register, login and refresh.
func (s *Server) writeSession(w http.ResponseWriter, status int, u *domain.User) {
	access, refresh, exp, err := s.issueSession(u)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, status, map[string]any{
		"token":        access,
		"refreshToken": refresh,
		"expiresAt":    exp,
		"user":         publicUser(u),
	})
}

func (s *Server) accessTTL() time.Duration {
	if s.cfg.Auth.AccessTTL > 0 {
		return s.cfg.Auth.AccessTTL
	}
	return 15 * time.Minute
}

func (s *Server) refreshTTL() time.Duration {
	if s.cfg.Auth.RefreshTTL > 0 {
		return s.cfg.Auth.RefreshTTL
	}
	return 7 * 24 * time.Hour
}

// isAccessToken accepts access tokens plus the legacy tokens that predate the
// purpose claim, and nothing else.
func isAccessToken(c *auth.Claims) bool {
	if c == nil {
		return false
	}
	return c.Purpose == "" || c.Purpose == auth.PurposeAccess
}

func publicUser(u *domain.User) map[string]any {
	return map[string]any{
		"id":          u.ID,
		"username":    u.Username,
		"displayName": u.DisplayName,
		"role":        roleOf(u),
	}
}

// roleOf normalises the empty role stored by pre-admin accounts.
func roleOf(u *domain.User) string {
	if u.IsAdmin() {
		return domain.RoleAdmin
	}
	return domain.RoleUser
}
