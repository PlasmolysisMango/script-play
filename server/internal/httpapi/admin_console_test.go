package httpapi

import (
	"net/http"
	"testing"
	"time"
)

// The admin console is the only way into the curated library, so the boundary
// has to be enforced server side, not by hiding the button.
func TestNonAdminCannotImportOrEditScripts(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)

	plain := &apiClient{t: t, base: ts.URL}
	var reg struct {
		Token string `json:"token"`
		User  struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	plain.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "plainuser", "password": "secret123"}, &reg, http.StatusCreated)
	plain.token = reg.Token
	if reg.User.Role != "user" {
		t.Fatalf("registered role = %q", reg.User.Role)
	}

	if code, body := plain.call(http.MethodPost, "/api/admin/materials",
		map[string]string{"source": "raw"}); code != http.StatusForbidden {
		t.Fatalf("upload material as user = %d %s", code, body)
	}
	if code, body := plain.call(http.MethodGet, "/api/admin/materials", nil); code != http.StatusForbidden {
		t.Fatalf("list materials as user = %d %s", code, body)
	}
	if code, body := plain.call(http.MethodPost, "/api/scripts/sample", nil); code != http.StatusForbidden {
		t.Fatalf("seed sample as user = %d %s", code, body)
	}
	if code, body := plain.call(http.MethodGet, "/api/admin/users", nil); code != http.StatusForbidden {
		t.Fatalf("admin list as user = %d %s", code, body)
	}

	admin := adminClient(t, ts)
	var me struct {
		Role string `json:"role"`
	}
	admin.mustJSON(http.MethodGet, "/api/me", nil, &me, http.StatusOK)
	if me.Role != "admin" {
		t.Fatalf("admin role = %q", me.Role)
	}
}

// Resetting a password is the console's answer to "show me the password": the
// stored value is a bcrypt hash, so a fresh one is issued instead.
func TestAdminResetsPasswordAndListsAccounts(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)

	plain := &apiClient{t: t, base: ts.URL}
	var reg struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	plain.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "resetme", "password": "secret123"}, &reg, http.StatusCreated)

	admin := adminClient(t, ts)
	var list struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			Me       bool   `json:"me"`
		} `json:"users"`
	}
	admin.mustJSON(http.MethodGet, "/api/admin/users", nil, &list, http.StatusOK)
	if len(list.Users) < 2 {
		t.Fatalf("expected admin plus the registered user, got %+v", list.Users)
	}
	found := false
	for _, u := range list.Users {
		if u.Username == "resetme" {
			found = true
			if u.Role != "user" || u.ID != reg.User.ID {
				t.Fatalf("unexpected row: %+v", u)
			}
		}
	}
	if !found {
		t.Fatal("registered account missing from the admin list")
	}

	var reset struct {
		Password  string `json:"password"`
		Generated bool   `json:"generated"`
	}
	admin.mustJSON(http.MethodPost, "/api/admin/users/"+reg.User.ID+"/password",
		map[string]string{}, &reset, http.StatusOK)
	if len(reset.Password) < 6 || !reset.Generated {
		t.Fatalf("reset response = %+v", reset)
	}

	fresh := &apiClient{t: t, base: ts.URL}
	var session struct {
		Token string `json:"token"`
	}
	fresh.mustJSON(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "resetme", "password": reset.Password}, &session, http.StatusOK)
	if session.Token == "" {
		t.Fatal("temporary password did not produce a session")
	}
	// The old password must be gone.
	if code, _ := plain.call(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "resetme", "password": "secret123"}); code != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", code)
	}
}

// "Close" keeps the transcript for the replay, "delete" removes the room for
// good. Both are admin actions on someone else's room.
func TestAdminClosesAndDeletesRooms(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 5*time.Second)
	admin := adminClient(t, ts)

	var seeded struct {
		Script struct {
			ID string `json:"id"`
		} `json:"script"`
	}
	admin.mustJSON(http.MethodPost, "/api/scripts/sample", nil, &seeded, http.StatusCreated)

	host := &apiClient{t: t, base: ts.URL}
	var reg struct {
		Token string `json:"token"`
	}
	host.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "roomowner", "password": "secret123"}, &reg, http.StatusCreated)
	host.token = reg.Token

	var created struct {
		Room struct {
			Code string `json:"code"`
		} `json:"room"`
	}
	host.mustJSON(http.MethodPost, "/api/rooms",
		map[string]any{"scriptId": seeded.Script.ID, "maxSeats": 4}, &created, http.StatusCreated)
	code := created.Room.Code

	var rooms struct {
		Rooms []struct {
			Code        string `json:"code"`
			Status      string `json:"status"`
			ScriptTitle string `json:"scriptTitle"`
			HostName    string `json:"hostName"`
		} `json:"rooms"`
	}
	admin.mustJSON(http.MethodGet, "/api/admin/rooms", nil, &rooms, http.StatusOK)
	seen := false
	for _, r := range rooms.Rooms {
		if r.Code == code {
			seen = true
			if r.ScriptTitle == "" || r.HostName == "" {
				t.Fatalf("room row is missing joins: %+v", r)
			}
		}
	}
	if !seen {
		t.Fatalf("room %s missing from the admin list", code)
	}

	admin.mustJSON(http.MethodPost, "/api/admin/rooms/"+code+"/close", nil, nil, http.StatusOK)
	statusOf := func() string {
		var after struct {
			Rooms []struct {
				Code   string `json:"code"`
				Status string `json:"status"`
			} `json:"rooms"`
		}
		admin.mustJSON(http.MethodGet, "/api/admin/rooms", nil, &after, http.StatusOK)
		for _, r := range after.Rooms {
			if r.Code == code {
				return r.Status
			}
		}
		return ""
	}
	if got := statusOf(); got != "finished" {
		t.Fatalf("room status after close = %q", got)
	}

	admin.mustJSON(http.MethodDelete, "/api/admin/rooms/"+code, nil, nil, http.StatusOK)
	if got := statusOf(); got != "" {
		t.Fatalf("room still listed after delete: %q", got)
	}
}
