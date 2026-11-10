// Package store persists users, scripts, rooms, games and import jobs.
//
// The data lives in JSON files under <data>/store: whole-table files for the
// small tables, one file per script and per game, and an append-only JSONL
// event log per game (see persist.go). The old single state.json snapshot is
// migrated automatically on first start and kept as state.json.pre-split-<ts>.
//
// Implementation note: the plan called for SQLite. This build ships embedded
// JSON files instead (atomic rename + debounced flush) because the sqlite
// driver is a very heavy pure-Go compile and the target machine only has
// 2 vCPU / 3.5 GB. The Store type is the whole surface the rest of the server
// uses, so swapping in a SQLite backend later only touches this package.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"script-play/internal/domain"
)

// ErrNotFound is returned by every lookup that misses.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint is violated.
var ErrConflict = errors.New("conflict")

// Store is a process-wide, mutex-guarded document store.
type Store struct {
	mu        sync.RWMutex
	dir       string
	base      string
	users     map[string]*domain.User
	usernames map[string]string
	scripts   map[string]*domain.ScriptRecord
	rooms     map[string]*domain.Room
	games     map[string]*domain.Game
	materials map[string]*domain.RawMaterial

	// dirty holds, per entity key, the version waiting to be written; vers holds
	// the version currently in memory. A flush clears an entry only when the two
	// still match, so a write that races with a Put is retried instead of lost.
	dirty     map[string]uint64
	vers      map[string]uint64
	eventRows map[string]int

	stop    chan struct{}
	done    chan struct{}
	flushed time.Time
}

// NewID returns a random hex identifier with a human readable prefix.
func NewID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// NewCode returns a 6 character room code.
func NewCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "AAAAAA"
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// ---- users ----

// CreateUser inserts a user, rejecting duplicate usernames.
func (s *Store) CreateUser(u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(u.Username)
	if _, ok := s.usernames[key]; ok {
		return ErrConflict
	}
	s.users[u.ID] = u
	s.usernames[key] = u.ID
	s.mark(keyUsers)
	return nil
}

// UserByUsername looks a user up case-insensitively.
func (s *Store) UserByUsername(name string) (*domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.usernames[strings.ToLower(name)]
	if !ok {
		return nil, ErrNotFound
	}
	return s.users[id], nil
}

// UserByID looks a user up by id.
func (s *Store) UserByID(id string) (*domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

// PutUser stores a private copy of an existing user. Callers own the pointer
// they looked up, so mutating it in place would race with the flusher.
func (s *Store) PutUser(u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; !ok {
		return ErrNotFound
	}
	cp := *u
	s.users[u.ID] = &cp
	s.usernames[strings.ToLower(cp.Username)] = cp.ID
	s.mark(keyUsers)
	return nil
}

// ListUsers returns every account, newest first. Hashes are included because
// the admin console needs to know whether a password was already reset, but
// they are never reversible.
func (s *Store) ListUsers() []*domain.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.User, 0, len(s.users))
	for _, u := range s.users {
		cp := *u
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// CountScriptsBy returns how many script records a user owns.
func (s *Store) CountScriptsBy(userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, rec := range s.scripts {
		if rec.OwnerUserID == userID {
			n++
		}
	}
	return n
}

// ---- scripts ----

// PutScript stores a private copy of the record.
func (s *Store) PutScript(rec *domain.ScriptRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.UpdatedAt = time.Now().UTC()
	s.scripts[rec.ID] = clonePtr(rec)
	s.mark(scriptKey(rec.ID))
}

// GetScript returns a copy of the record. The store owns the canonical value:
// returning its pointer would let handlers write into a struct the flusher may
// be marshalling at the same time.
func (s *Store) GetScript(id string) (*domain.ScriptRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.scripts[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePtr(rec), nil
}

// ListScripts returns records visible to a user (own drafts + all published).
func (s *Store) ListScripts(userID string) []*domain.ScriptRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.ScriptRecord, 0, len(s.scripts))
	for _, rec := range s.scripts {
		if rec.Status == "published" || rec.OwnerUserID == userID {
			out = append(out, clonePtr(rec))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// AllScripts returns every script record, newest first (admin console).
func (s *Store) AllScripts() []*domain.ScriptRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.ScriptRecord, 0, len(s.scripts))
	for _, rec := range s.scripts {
		out = append(out, clonePtr(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// DeleteScript removes a script record and its file.
func (s *Store) DeleteScript(id string) error {
	s.mu.Lock()
	if _, ok := s.scripts[id]; !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.scripts, id)
	delete(s.dirty, scriptKey(id))
	s.mu.Unlock()
	if err := os.Remove(s.scriptPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ---- rooms ----

// PutRoom stores a private copy of the room.
func (s *Store) PutRoom(r *domain.Room) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.UpdatedAt = time.Now().UTC()
	s.rooms[r.ID] = clonePtr(r)
	s.mark(keyRooms)
}

// GetRoom returns a room by id (a copy owned by the caller).
func (s *Store) GetRoom(id string) (*domain.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rooms[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePtr(r), nil
}

// RoomByCode returns a room by join code.
func (s *Store) RoomByCode(code string) (*domain.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.rooms {
		if strings.EqualFold(r.Code, code) {
			return clonePtr(r), nil
		}
	}
	return nil, ErrNotFound
}

// ListRooms returns all rooms, newest first.
func (s *Store) ListRooms() []*domain.Room {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		out = append(out, clonePtr(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// DeleteRoom removes a room record (admin delete). Rooms live in one table
// file, so the whole table is rewritten on the next flush.
func (s *Store) DeleteRoom(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rooms[id]; !ok {
		return ErrNotFound
	}
	delete(s.rooms, id)
	s.mark(keyRooms)
	return nil
}

// ---- games ----

// PutGame stores a private copy of the game. The caller keeps its own
// authoritative value, so the flusher never reads a struct being mutated.
func (s *Store) PutGame(g *domain.Game) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.games[g.ID] = clonePtr(g)
	s.mark(gameKey(g.ID))
}

// GetGame returns a game by id (a copy owned by the caller).
func (s *Store) GetGame(id string) (*domain.Game, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.games[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePtr(g), nil
}

// DeleteGame removes a game record and its event log. A missing game is not an
// error: deleting a lobby room that never started should still succeed.
func (s *Store) DeleteGame(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	_, ok := s.games[id]
	if ok {
		delete(s.games, id)
		delete(s.dirty, gameKey(id))
		delete(s.eventRows, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if err := os.Remove(s.gamePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("remove game file failed", "game", id, "err", err)
	}
	if err := os.Remove(s.eventsPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("remove game event log failed", "game", id, "err", err)
	}
}

// ---- raw materials ----

// PutMaterial stores a private copy of the material record.
func (s *Store) PutMaterial(m *domain.RawMaterial) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.materials[m.ID] = clonePtr(m)
	s.mark(keyMaterials)
}

// GetMaterial returns one uploaded original by id.
func (s *Store) GetMaterial(id string) (*domain.RawMaterial, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.materials[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePtr(m), nil
}

// ListMaterials returns every uploaded original, newest first.
func (s *Store) ListMaterials() []*domain.RawMaterial {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.RawMaterial, 0, len(s.materials))
	for _, m := range s.materials {
		out = append(out, clonePtr(m))
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	return out
}

// DeleteMaterial drops the record; the caller removes the file itself.
func (s *Store) DeleteMaterial(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.materials[id]; !ok {
		return ErrNotFound
	}
	delete(s.materials, id)
	s.mark(keyMaterials)
	return nil
}

// Dir returns the data directory (used for uploads).
func (s *Store) Dir() string { return s.dir }

// clonePtr deep copies a stored value. The store and its callers must never
// share a pointer: the flusher marshals stored values while actors and handlers
// keep mutating their own copies.
func clonePtr[T any](v *T) *T {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return &out
}
