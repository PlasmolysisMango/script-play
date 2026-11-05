package httpapi

import (
	"net/http"
	"testing"
	"time"
)

// Access tokens are short-lived and a refresh token exists to replace them.
// The two are not interchangeable: a refresh token must never unlock an API
// route, and an access token must never mint a new session.
func TestRefreshRotatesAccessToken(t *testing.T) {
	ts := newE2EServer(t, "mock", time.Second, 10*time.Second)

	c := &apiClient{t: t, base: ts.URL}
	var reg struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    string `json:"expiresAt"`
	}
	c.mustJSON(http.MethodPost, "/api/auth/register",
		map[string]string{"username": "refresher", "password": "secret123"}, &reg, http.StatusCreated)
	if reg.Token == "" || reg.RefreshToken == "" || reg.ExpiresAt == "" {
		t.Fatalf("register did not return an access/refresh pair: %+v", reg)
	}
	if reg.Token == reg.RefreshToken {
		t.Fatalf("access and refresh tokens are identical")
	}

	// The access token works on its own.
	c.token = reg.Token
	c.mustJSON(http.MethodGet, "/api/me", nil, nil, http.StatusOK)

	// A refresh token is not an access token.
	c.token = reg.RefreshToken
	if code, payload := c.call(http.MethodGet, "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("me with refresh token = %d %s", code, payload)
	}

	// Refreshing yields a usable new pair.
	c.token = ""
	var fresh struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refreshToken"`
	}
	c.mustJSON(http.MethodPost, "/api/auth/refresh",
		map[string]string{"refreshToken": reg.RefreshToken}, &fresh, http.StatusOK)
	if fresh.Token == "" || fresh.Token == reg.Token {
		t.Fatalf("refresh did not mint a new access token: %+v", fresh)
	}
	c.token = fresh.Token
	c.mustJSON(http.MethodGet, "/api/me", nil, nil, http.StatusOK)

	// An access token cannot be replayed as a refresh token.
	c.token = ""
	if code, payload := c.call(http.MethodPost, "/api/auth/refresh",
		map[string]string{"refreshToken": fresh.Token}); code != http.StatusUnauthorized {
		t.Fatalf("refresh with access token = %d %s", code, payload)
	}
	if code, _ := c.call(http.MethodPost, "/api/auth/refresh",
		map[string]string{"refreshToken": "not-a-jwt"}); code != http.StatusUnauthorized {
		t.Fatalf("refresh with garbage = %d", code)
	}
}
