package httpapi

import (
	"testing"

	"script-play/internal/config"
	"script-play/internal/domain"
	"script-play/internal/store"
)

// TestEnsureAdminSeedsNickname pins the two behaviours the deployment relies on:
// a fresh account takes the configured nickname, and an existing record is
// renamed without touching the password beyond what the env asks for.
func TestEnsureAdminSeedsNickname(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := &config.Config{Admin: config.Admin{
		Username:    "mango",
		Password:    "admin",
		DisplayName: "芒果干",
	}}
	if err := EnsureAdmin(cfg, st); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	u, err := st.UserByUsername("mango")
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if u.Role != domain.RoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}
	if u.DisplayName != "芒果干" {
		t.Errorf("displayName = %q, want 芒果干", u.DisplayName)
	}

	// A restart without the nickname must not wipe the one already stored.
	cfg.Admin.DisplayName = ""
	if err := EnsureAdmin(cfg, st); err != nil {
		t.Fatalf("reseed admin: %v", err)
	}
	if u, _ = st.UserByUsername("mango"); u.DisplayName != "芒果干" {
		t.Errorf("displayName after rename-less restart = %q, want 芒果干", u.DisplayName)
	}

	// Setting the env renames the existing account in place.
	cfg.Admin.DisplayName = "芒果"
	if err := EnsureAdmin(cfg, st); err != nil {
		t.Fatalf("rename admin: %v", err)
	}
	if u, _ = st.UserByUsername("mango"); u.DisplayName != "芒果" {
		t.Errorf("displayName after rename = %q, want 芒果", u.DisplayName)
	}
}
