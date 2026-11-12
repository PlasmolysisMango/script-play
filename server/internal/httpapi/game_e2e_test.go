package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"script-play/internal/config"
	"script-play/internal/importer"
	"script-play/internal/llm"
	"script-play/internal/realtime"
	"script-play/internal/rooms"
	"script-play/internal/store"
)

// This file drives one complete game the way a browser does: REST for setup,
// a websocket bound to the host seat for the play loop. It runs against the
// mock provider in CI and against the real qoder CLI when SP_E2E_PROVIDER is
// set to qodercli, which is the M4 acceptance ("a real LLM game finishes").

type e2eEvent struct {
	Seq     int               `json:"seq"`
	Type    string            `json:"type"`
	SeatID  string            `json:"seatId"`
	RoleID  string            `json:"roleId"`
	Speaker string            `json:"speaker"`
	Text    string            `json:"text"`
	ClueID  string            `json:"clueId"`
	Meta    map[string]string `json:"meta"`
}

type e2eState struct {
	Status    string `json:"status"`
	VoteCount int    `json:"voteCount"`
	Search    []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"search"`
	Floor *struct {
		SeatID  string `json:"seatId"`
		PhaseID string `json:"phaseId"`
	} `json:"floor"`
	Visible struct {
		Phase *struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"phase"`
		Self struct {
			ID     string `json:"id"`
			RoleID string `json:"roleId"`
		} `json:"self"`
		Seats []struct {
			ID       string `json:"id"`
			RoleID   string `json:"roleId"`
			RoleName string `json:"roleName"`
		} `json:"seats"`
		Transcript []struct {
			Seq  int    `json:"seq"`
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"transcript"`
	} `json:"visible"`
	Phases []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"phases"`
}

type apiClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *apiClient) call(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal %s: %v", path, err)
		}
		reader = bytes.NewReader(blob)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("request %s: %v", path, err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		c.t.Fatalf("read %s: %v", path, err)
	}
	return res.StatusCode, buf.Bytes()
}

func (c *apiClient) mustJSON(method, path string, body, out any, want int) {
	c.t.Helper()
	code, payload := c.call(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s = %d want %d body=%s", method, path, code, want, payload)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			c.t.Fatalf("decode %s: %v body=%s", path, err, payload)
		}
	}
}

func (c *apiClient) state(code string) e2eState {
	c.t.Helper()
	var out struct {
		State e2eState `json:"state"`
	}
	c.mustJSON(http.MethodGet, "/api/rooms/"+code+"/state", nil, &out, http.StatusOK)
	return out.State
}

// newE2EServer is newTestServer with configurable provider and timeouts.
func newE2EServer(t *testing.T, provider string, floor, agent time.Duration) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		DataDir:   dir,
		JWTSecret: []byte("e2e-secret"),
		// Script writes are admin-only, so every fixture gets a bootstrap admin.
		Admin: config.Admin{Username: e2eAdminUser, Password: e2eAdminPass},
		LLM: config.LLM{
			Default:       provider,
			MaxConcurrent: 3,
			Qoder:         config.Qoder{Model: "Auto", Timeout: 3 * time.Minute},
		},
		Limits: config.Limits{
			MaxRooms:       2,
			AgentTimeout:   agent,
			FloorTimeout:   floor,
			MaxUploadBytes: 4 << 20,
			// The e2e flow walks the whole script with no human clicking between
			// phases, so it opts into auto advance.
			AutoAdvance: true,
		},
	}
	if err := EnsureAdmin(cfg, st); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	reg := llm.NewRegistry(provider, 3)
	if provider == "qodercli" {
		reg.Register(llm.NewQoderCLI("qodercli", "Auto", "default", t.TempDir(), 3*time.Minute))
	} else {
		reg.Register(llm.NewMock())
	}
	hub := realtime.NewHub()
	manager := rooms.NewManager(context.Background(), st, hub, reg, cfg)
	t.Cleanup(manager.Close)
	imp := importer.New(st)
	srv := New(cfg, st, reg, manager, imp)
	srv.mountTestSampleRoute()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

const (
	e2eAdminUser = "e2e-admin"
	e2eAdminPass = "admin12345"
)

// adminClient signs in as the bootstrap admin. Script import/edit is
// admin-only, so fixtures that seed a script start from this client.
func adminClient(t *testing.T, ts *httptest.Server) *apiClient {
	t.Helper()
	c := &apiClient{t: t, base: ts.URL}
	var res struct {
		Token string `json:"token"`
	}
	c.mustJSON(http.MethodPost, "/api/auth/login",
		map[string]string{"username": e2eAdminUser, "password": e2eAdminPass}, &res, http.StatusOK)
	c.token = res.Token
	return c
}

func TestFullGameOverRESTAndWebSocket(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		floor    time.Duration
		agent    time.Duration
		timeout  time.Duration
	}{
		{name: "mock", provider: "mock", floor: 1 * time.Second, agent: 10 * time.Second, timeout: 90 * time.Second},
	}
	if os.Getenv("SP_E2E_PROVIDER") == "qodercli" {
		cases = append(cases, struct {
			name     string
			provider string
			floor    time.Duration
			agent    time.Duration
			timeout  time.Duration
		}{name: "qodercli", provider: "qodercli", floor: 10 * time.Second, agent: 3 * time.Minute, timeout: 15 * time.Minute})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runFullGame(t, tc.provider, tc.floor, tc.agent, tc.timeout)
		})
	}
}

func runFullGame(t *testing.T, provider string, floor, agent, timeout time.Duration) {
	t.Helper()
	ts := newE2EServer(t, provider, floor, agent)

	host := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	var created struct {
		Room struct {
			Code  string `json:"code"`
			Seats []struct {
				ID string `json:"id"`
			} `json:"seats"`
			MySeat  string `json:"mySeatId"`
			RoleLen int    `json:"-"`
		} `json:"room"`
	}
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4}, &created, http.StatusCreated)
	code := created.Room.Code
	hostSeatID := created.Room.MySeat

	// Three AI seats next to the host: the sample script needs four players.
	personas := []string{"guarded", "sly", "theatrical"}
	for _, pid := range personas {
		host.mustJSON(http.MethodPost, "/api/rooms/"+code+"/seats",
			map[string]any{"personaId": pid}, nil, http.StatusCreated)
	}

	// Connect the host socket before the game starts, the way a browser sits in
	// the lobby, so the opening DM and the first phase change are not missed.
	wsURL := strings.Replace(ts.URL, "http", "ws", 1) + "/ws/rooms/" + code + "?token=" + host.token
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	host.mustJSON(http.MethodPost, "/api/rooms/"+code+"/start", nil, nil, http.StatusOK)

	events := make(chan e2eEvent, 512)
	go func() {
		defer close(events)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var ev e2eEvent
			if json.Unmarshal(data, &ev) == nil {
				select {
				case events <- ev:
				default:
				}
			}
		}
	}()

	send := func(payload map[string]string) {
		blob, _ := json.Marshal(payload)
		if err := conn.Write(ctx, websocket.MessageText, blob); err != nil {
			t.Logf("write failed: %v", err)
		}
	}

	var (
		collected  []e2eEvent
		deadline   = time.Now().Add(timeout)
		spoken     = map[string]bool{}
		voted      = map[string]bool{}
		picksInPhs = map[string]int{}
		finished   bool
	)
	for !finished && time.Now().Before(deadline) {
		select {
		case ev, ok := <-events:
			if !ok {
				finished = true
				break
			}
			collected = append(collected, ev)
			switch ev.Type {
			case "system":
				switch {
				case strings.Contains(ev.Text, "轮到你搜证"):
					st := host.state(code)
					if len(st.Search) == 0 {
						// Nothing left this seat may take; the phase timer will
						// move the game on, so just wait.
						continue
					}
					send(map[string]string{"type": "search.clue", "clueId": st.Search[0].ID})
					picksInPhs[st.Visible.Phase.ID]++
				case strings.Contains(ev.Text, "轮到你发言"):
					st := host.state(code)
					phaseID := ""
					if st.Visible.Phase != nil {
						phaseID = st.Visible.Phase.ID
					}
					if spoken[phaseID] {
						continue
					}
					spoken[phaseID] = true
					send(map[string]string{"type": "speak.text", "text": "我先说：那十分钟里我在塔下，只听到灯灭的声音。你怎么解释你手上的机油？"})
				case strings.Contains(ev.Text, "投票阶段"):
					st := host.state(code)
					target := ""
					for _, s := range st.Visible.Seats {
						if s.RoleID != "" && s.RoleID != st.Visible.Self.RoleID {
							target = s.RoleID
							break
						}
					}
					phaseID := ""
					if st.Visible.Phase != nil {
						phaseID = st.Visible.Phase.ID
					}
					if target == "" || voted[phaseID] {
						continue
					}
					voted[phaseID] = true
					send(map[string]string{"type": "vote.cast", "roleId": target})
				}
			case "error":
				t.Logf("server error frame: %s", ev.Text)
			}
		case <-time.After(300 * time.Millisecond):
			st := host.state(code)
			if st.Status == "finished" {
				finished = true
			}
		}
	}

	if !finished {
		t.Fatalf("game did not finish within %s; last state=%+v", timeout, host.state(code))
	}
	// Give the socket a moment to deliver the tail of the log (reveal +结束).
	tail := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				goto assertions
			}
			collected = append(collected, ev)
		case <-tail:
			goto assertions
		}
	}

assertions:
	final := host.state(code)
	if final.Status != "finished" {
		t.Fatalf("status = %q want finished", final.Status)
	}
	for _, p := range final.Phases {
		if p.Status != "done" {
			t.Errorf("phase %s status = %q want done", p.ID, p.Status)
		}
	}

	var kinds = map[string]int{}
	for _, ev := range collected {
		kinds[ev.Type]++
	}
	for _, want := range []string{"phase", "dm", "speech", "clue.found", "vote", "vote.result", "reveal"} {
		if kinds[want] == 0 {
			t.Errorf("websocket never delivered a %q event (kinds=%v)", want, kinds)
		}
	}

	// The host transcript must carry real agent speeches, and no speech may
	// quote the solution before the reveal.
	agentSpeeches := 0
	for _, ev := range collected {
		if ev.Type != "speech" {
			continue
		}
		if ev.SeatID != hostSeatID && strings.TrimSpace(ev.Text) != "" {
			agentSpeeches++
			t.Logf("agent %s said: %.90s", ev.Speaker, ev.Text)
		}
	}
	if agentSpeeches == 0 {
		t.Error("no agent speech reached the host socket")
	}
	if kinds["leak.meta"] > 0 {
		t.Logf("leak incidents reported to the host: %d", kinds["leak.meta"])
	}

	var reveals int
	for _, ev := range collected {
		if ev.Type == "reveal" {
			reveals++
			if !strings.Contains(ev.Text, "凶手") {
				t.Errorf("reveal text is not a reveal: %q", ev.Text)
			}
		}
	}
	if reveals == 0 {
		t.Fatal("no reveal event after the vote")
	}
}

// TestGameSnapshotNeverLeaksTruthToAPlayerSocket guards the M4 acceptance:
// at no point before the reveal does a player's state carry the solution.
func TestGameSnapshotNeverLeaksTruthToAPlayerSocket(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 10*time.Second)
	host := adminClient(t, ts)
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
	// A full cast is required to start; this test only reads one snapshot, so the
	// other seats stay AI.
	for _, pid := range []string{"guarded", "sly", "theatrical"} {
		host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/seats", map[string]any{"personaId": pid}, nil, http.StatusCreated)
	}
	host.mustJSON(http.MethodPost, "/api/rooms/"+created.Room.Code+"/start", nil, nil, http.StatusOK)

	// The author sees the whole script; the player state must not contain any
	// of it.
	var full struct {
		Script struct {
			Body struct {
				Truth struct {
					Summary string `json:"summary"`
					Method  string `json:"method"`
				} `json:"truth"`
			} `json:"body"`
		} `json:"script"`
	}
	host.mustJSON(http.MethodGet, "/api/scripts/"+seeded.Script.ID, nil, &full, http.StatusOK)
	if full.Script.Body.Truth.Summary == "" {
		t.Fatal("sample script truth is empty, the test would prove nothing")
	}

	code, payload := host.call(http.MethodGet, "/api/rooms/"+created.Room.Code+"/state", nil)
	if code != http.StatusOK {
		t.Fatalf("state = %d %s", code, payload)
	}
	body := string(payload)
	if strings.Contains(strings.ToLower(body), "killerroleid") {
		t.Fatalf("player state exposed a killer field: %s", body)
	}
	if strings.Contains(body, full.Script.Body.Truth.Summary) {
		t.Fatalf("player state exposed the truth summary: %s", body)
	}
	if full.Script.Body.Truth.Method != "" && strings.Contains(body, full.Script.Body.Truth.Method) {
		t.Fatalf("player state exposed the truth method: %s", body)
	}
}
