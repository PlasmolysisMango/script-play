// Package config loads runtime settings from environment variables.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved server configuration.
type Config struct {
	Addr    string
	DataDir string
	WebDir  string
	// ScriptsDir is where the committed scripts live — each one a directory
	// holding script.json and its pictures. The server only reads pictures from
	// it (GET /script-assets/…); the JSON is imported through the studio, so an
	// edit on disk never silently rewrites a published script.
	ScriptsDir string
	JWTSecret  []byte
	Auth       Auth
	Admin      Admin
	LLM        LLM
	Limits     Limits
}

// Admin seeds the one account allowed into the admin console. Username is the
// only required part: with a password the account is created on first start,
// without one an already registered account is promoted.
type Admin struct {
	Username string
	Password string
	// DisplayName is the nickname shown in the UI. Empty means "same as
	// username", which keeps the older single-variable deployment working.
	DisplayName string
}

// Auth controls session token lifetimes. Access tokens are short-lived and
// renewed with a refresh token, so a stolen access token ages out quickly.
type Auth struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// LLM selects and configures model providers.
type LLM struct {
	// Default provider: qodercli | openai | mock.
	Default       string
	MaxConcurrent int
	Qoder         Qoder
	OpenAI        OpenAI
}

// Qoder drives the local Qoder CLI in headless mode.
type Qoder struct {
	Bin        string
	Model      string
	Timeout    time.Duration
	Permission string
}

// OpenAI points at any OpenAI-compatible endpoint.
type OpenAI struct {
	BaseURL     string
	APIKey      string
	Model       string
	VisionModel string
	Timeout     time.Duration
}

// Limits caps concurrency on small machines.
type Limits struct {
	MaxRooms       int
	AgentTimeout   time.Duration
	FloorTimeout   time.Duration
	MaxUploadBytes int64
	// AutoAdvance skips the host gate between phases. It exists for scripted
	// tests and unattended demos; real tables wait for the host's click.
	AutoAdvance bool
}

// Load reads the environment, creating a JWT secret on first run.
func Load() (*Config, error) {
	dataDir := env("SP_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	secret := os.Getenv("SP_JWT_SECRET")
	secretBytes := []byte(secret)
	if len(secretBytes) == 0 {
		var err error
		secretBytes, err = loadOrCreateSecret(dataDir)
		if err != nil {
			return nil, err
		}
	}

	cfg := &Config{
		Addr:    env("SP_ADDR", ":8080"),
		DataDir: dataDir,
		WebDir:  os.Getenv("SP_WEB_DIR"),
		// Relative to the working directory, which is server/ for both
		// `go run ./cmd/server` and the systemd unit.
		ScriptsDir: env("SP_SCRIPTS_DIR", "../scripts"),
		JWTSecret:  secretBytes,
		Auth: Auth{
			AccessTTL:  time.Duration(envInt("SP_ACCESS_TTL_MIN", 15)) * time.Minute,
			RefreshTTL: time.Duration(envInt("SP_REFRESH_TTL_HOUR", 7*24)) * time.Hour,
		},
		Admin: Admin{
			Username:    strings.TrimSpace(os.Getenv("SP_ADMIN_USERNAME")),
			Password:    os.Getenv("SP_ADMIN_PASSWORD"),
			DisplayName: strings.TrimSpace(os.Getenv("SP_ADMIN_DISPLAY_NAME")),
		},
		LLM: LLM{
			Default:       strings.ToLower(env("SP_LLM_PROVIDER", "qodercli")),
			MaxConcurrent: envInt("SP_LLM_MAX_CONCURRENT", 3),
			Qoder: Qoder{
				Bin:        env("SP_QODER_BIN", "qodercli"),
				Model:      env("SP_QODER_MODEL", "Auto"),
				Timeout:    time.Duration(envInt("SP_QODER_TIMEOUT_SEC", 180)) * time.Second,
				Permission: env("SP_QODER_PERMISSION", "default"),
			},
			OpenAI: OpenAI{
				BaseURL:     strings.TrimRight(os.Getenv("SP_OPENAI_BASE_URL"), "/"),
				APIKey:      os.Getenv("SP_OPENAI_API_KEY"),
				Model:       env("SP_OPENAI_MODEL", "gpt-4o-mini"),
				VisionModel: os.Getenv("SP_OPENAI_VISION_MODEL"),
				Timeout:     time.Duration(envInt("SP_OPENAI_TIMEOUT_SEC", 120)) * time.Second,
			},
		},
		Limits: Limits{
			MaxRooms:     envInt("SP_MAX_ROOMS", 4),
			AgentTimeout: time.Duration(envInt("SP_AGENT_TIMEOUT_SEC", 120)) * time.Second,
			FloorTimeout: time.Duration(envInt("SP_FLOOR_TIMEOUT_SEC", 90)) * time.Second,
			// Raw scripts arrive as whole bundles: a docx with photos or a zip of
			// chapter folders is easily tens of megabytes, and the newest single
			// archive on hand is 68 MB. The handler streams parts, so a bigger
			// cap costs disk rather than RAM; it also caps a whole request at
			// twice this value. nginx's client_max_body_size must be at least
			// that large or the proxy answers 413 first.
			MaxUploadBytes: int64(envInt("SP_MAX_UPLOAD_MB", 256)) << 20,
			AutoAdvance:    envBool("SP_AUTO_ADVANCE", false),
		},
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func loadOrCreateSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, "secret.key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	out := []byte(hex.EncodeToString(buf))
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return nil, fmt.Errorf("persist secret: %w", err)
	}
	return out, nil
}
