// Package auth implements password hashing and HS256 tokens with only the
// standard library plus bcrypt.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidToken is returned for malformed or expired tokens.
var ErrInvalidToken = errors.New("invalid token")

// Purposes keep the three token kinds apart: a refresh token can never open an
// API route, and a seat ticket only unlocks its own websocket seat.
const (
	PurposeAccess     = "access"
	PurposeRefresh    = "refresh"
	PurposeSeatTicket = "seat-ticket"
)

// Claims is the JWT payload. API access tokens carry PurposeAccess, refresh
// tokens PurposeRefresh; older tokens left Purpose empty and are still accepted
// as access tokens. Seat tickets carry their own purpose together with the
// room/game and the seat they are bound to, so a leaked ticket can never be
// replayed against another seat.
type Claims struct {
	Sub     string `json:"sub"`
	Name    string `json:"name,omitempty"`
	Iat     int64  `json:"iat"`
	Exp     int64  `json:"exp"`
	Purpose string `json:"purpose,omitempty"`
	GameID  string `json:"gid,omitempty"`
	SeatID  string `json:"sid,omitempty"`
	// JTI keeps every token unique even when two are minted in the same second,
	// which matters for refresh rotation.
	JTI string `json:"jti,omitempty"`
}

// HashPassword hashes a plaintext password.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword verifies a password against a bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// SignSeatTicket issues a short-lived token bound to one seat of one game.
func SignSeatTicket(secret []byte, userID, displayName, gameID, seatID string, ttl time.Duration) (string, time.Time, error) {
	exp := time.Now().Add(ttl).UTC()
	token, err := Sign(secret, Claims{
		Sub:     userID,
		Name:    displayName,
		Purpose: PurposeSeatTicket,
		GameID:  gameID,
		SeatID:  seatID,
		Exp:     exp.Unix(),
	})
	return token, exp, err
}

// SignSession issues an access or refresh token for a user.
func SignSession(secret []byte, userID, displayName, purpose string, ttl time.Duration) (string, time.Time, error) {
	exp := time.Now().Add(ttl).UTC()
	token, err := Sign(secret, Claims{
		Sub:     userID,
		Name:    displayName,
		Purpose: purpose,
		JTI:     newJTI(),
		Exp:     exp.Unix(),
	})
	return token, exp, err
}

func newJTI() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Sign creates a compact HS256 JWT.
func Sign(secret []byte, c Claims) (string, error) {
	if c.Iat == 0 {
		c.Iat = time.Now().Unix()
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := header + "." + body
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

// Verify checks the signature and expiry of a token.
func Verify(secret []byte, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrInvalidToken
	}
	if c.Exp > 0 && time.Now().Unix() > c.Exp {
		return nil, ErrInvalidToken
	}
	return &c, nil
}
