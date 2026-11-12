package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"script-play/internal/config"
	"script-play/internal/domain"
	"script-play/internal/engine"
	"script-play/internal/importer"
	"script-play/internal/llm"
	"script-play/internal/realtime"
	"script-play/internal/rooms"
	"script-play/internal/store"
)

func TestSampleScriptIsValidAndConsistent(t *testing.T) {
	var sc domain.Script
	if err := json.Unmarshal([]byte(SampleScriptJSON), &sc); err != nil {
		t.Fatalf("sample is not valid JSON: %v", err)
	}
	if errs := sc.Validate(); len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("sample validation: %s", e)
		}
		t.FailNow()
	}
	if len(sc.Roles) < 4 {
		t.Fatalf("sample has %d roles, want at least 4", len(sc.Roles))
	}
	if sc.Truth.KillerRoleID == "" || sc.Truth.Summary == "" {
		t.Fatal("sample must ship a complete truth block")
	}
	// The truth must never be quoted inside a role script, otherwise the whole
	// information boundary is theatre.
	for _, r := range sc.Roles {
		if strings.Contains(r.Private.Script, sc.Truth.Summary) {
			t.Errorf("role %s copies the truth summary into its script", r.ID)
		}
	}
	types := map[string]bool{}
	for _, p := range sc.Phases {
		types[p.Type] = true
	}
	for _, want := range []string{domain.PhaseReading, domain.PhaseSearch, domain.PhaseDiscussion, domain.PhaseVote, domain.PhaseReveal} {
		if !types[want] {
			t.Errorf("sample is missing a %s phase", want)
		}
	}
}

// newTestServer builds a server over temp storage and a mock provider.
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		DataDir:   dir,
		JWTSecret: []byte("test-secret"),
		LLM: config.LLM{
			Default:       "mock",
			MaxConcurrent: 2,
			Qoder:         config.Qoder{Model: "Auto"},
		},
		Limits: config.Limits{
			MaxRooms:       2,
			AgentTimeout:   5,
			FloorTimeout:   2,
			MaxUploadBytes: 4 << 20,
		},
	}
	reg := llm.NewRegistry("mock", 2)
	reg.Register(llm.NewMock())
	hub := realtime.NewHub()
	manager := rooms.NewManager(t.Context(), st, hub, reg, cfg)
	t.Cleanup(manager.Close)
	imp := importer.New(st)
	srv := New(cfg, st, reg, manager, imp)
	srv.mountTestSampleRoute()
	return srv, st
}

func TestHealthAndAuthFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("health = %d", rr.Code)
	}

	body := `{"username":"tester","password":"secret123","displayName":"测试"}`
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("register = %d body=%s", rr.Code, rr.Body.String())
	}
	var reg struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &reg); err != nil || reg.Token == "" {
		t.Fatalf("register token missing: %s", rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("me = %d body=%s", rr.Code, rr.Body.String())
	}

	// Unauthenticated access is rejected.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous me = %d", rr.Code)
	}
}

func TestNonOwnerCannotSeeTheSolution(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The redaction helper is the API level guard for script browsing.
	var sc domain.Script
	if err := json.Unmarshal([]byte(SampleScriptJSON), &sc); err != nil {
		t.Fatal(err)
	}
	rec := &domain.ScriptRecord{ID: "s1", OwnerUserID: "owner", Status: "published", Body: &sc}
	safe := redact(rec)
	if safe.Body.Truth.Summary != "" || safe.Body.Truth.KillerRoleID != "" {
		t.Fatal("redact leaked the truth")
	}
	for _, r := range safe.Body.Roles {
		if strings.TrimSpace(r.Private.Script) == "" {
			continue
		}
		if strings.Contains(r.Private.Script, "redact") || len(r.Private.Secrets) > 0 {
			t.Fatal("redact leaked private role data")
		}
	}
	if len(safe.Body.Clues) != 0 {
		t.Fatal("redact leaked clues")
	}
	for _, p := range safe.Body.Phases {
		if p.DMScript != "" {
			t.Fatalf("redact leaked phase dmScript: %s", p.ID)
		}
	}
}

func TestVisibleBeforeAndAfterReveal(t *testing.T) {
	var sc domain.Script
	if err := json.Unmarshal([]byte(SampleScriptJSON), &sc); err != nil {
		t.Fatal(err)
	}
	g := domain.NewGame("g", "room", "s1", nil)
	g.Seats = []domain.Seat{{ID: "seat1", RoleID: sc.Roles[0].ID, Kind: domain.SeatHuman, DisplayName: "玩家"}}
	seat := &g.Seats[0]

	blob, _ := json.Marshal(engine.VisibleFor(g, &sc, seat, engine.VisibleOptions{}))
	if strings.Contains(string(blob), sc.Truth.Summary) {
		t.Fatal("lobby view leaked the truth")
	}
	last := len(sc.Phases) - 1
	if sc.Phases[last].Type != domain.PhaseReveal {
		t.Fatalf("sample must end with reveal, got %s", sc.Phases[last].Type)
	}
	g.PhaseIndex = last
	blob, _ = json.Marshal(engine.VisibleFor(g, &sc, seat, engine.VisibleOptions{IncludeTruth: true}))
	if !strings.Contains(string(blob), sc.Truth.Summary) {
		t.Fatal("reveal view should expose the truth")
	}
}
