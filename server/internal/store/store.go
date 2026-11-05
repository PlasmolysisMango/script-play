// Package store persists users, scripts, rooms, games and import jobs.
//
// Implementation note: the plan called for SQLite. This build ships an
// embedded JSON snapshot store instead (atomic rename + debounced flush)
// because the sqlite driver is a very heavy pure-Go compile and the target
// machine only has 2 vCPU / 3.5 GB. The Store type is the whole surface the
// rest of the server uses, so swapping in a SQLite backend later only touches
// this package.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

type snapshot struct {
	Users     []*domain.User         `json:"users"`
	Scripts   []*domain.ScriptRecord `json:"scripts"`
	Rooms     []*domain.Room         `json:"rooms"`
	Games     []*domain.Game         `json:"games"`
	Materials []*domain.RawMaterial  `json:"materials"`
}

// Store is a process-wide, mutex-guarded document store.
type Store struct {
	mu        sync.RWMutex
	dir       string
	file      string
	users     map[string]*domain.User
	usernames map[string]string
	scripts   map[string]*domain.ScriptRecord
	rooms     map[string]*domain.Room
	games     map[string]*domain.Game
	materials map[string]*domain.RawMaterial

	dirty   bool
	stop    chan struct{}
	done    chan struct{}
	flushed time.Time
}

// Open loads (or creates) the store under dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	s := &Store{
		dir:       dir,
		file:      filepath.Join(dir, "state.json"),
		users:     map[string]*domain.User{},
		usernames: map[string]string{},
		scripts:   map[string]*domain.ScriptRecord{},
		rooms:     map[string]*domain.Room{},
		games:     map[string]*domain.Game{},
		materials: map[string]*domain.RawMaterial{},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	go s.loop()
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	for _, u := range snap.Users {
		s.users[u.ID] = u
		s.usernames[strings.ToLower(u.Username)] = u.ID
	}
	for _, sc := range snap.Scripts {
		s.scripts[sc.ID] = sc
	}
	for _, r := range snap.Rooms {
		s.rooms[r.ID] = r
	}
	for _, g := range snap.Games {
		s.games[g.ID] = g
	}
	for _, j := range snap.Materials {
		s.materials[j.ID] = j
	}
	return nil
}

func (s *Store) loop() {
	defer close(s.done)
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			_ = s.Flush()
			return
		case <-t.C:
			_ = s.Flush()
		}
	}
}

// Close stops the flusher and writes the last snapshot.
func (s *Store) Close() error {
	close(s.stop)
	<-s.done
	return s.Flush()
}

// Flush writes the snapshot to disk when dirty.
func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	snap := snapshot{
		Users:     make([]*domain.User, 0, len(s.users)),
		Scripts:   make([]*domain.ScriptRecord, 0, len(s.scripts)),
		Rooms:     make([]*domain.Room, 0, len(s.rooms)),
		Games:     make([]*domain.Game, 0, len(s.games)),
		Materials: make([]*domain.RawMaterial, 0, len(s.materials)),
	}
	for _, v := range s.users {
		snap.Users = append(snap.Users, v)
	}
	for _, v := range s.scripts {
		snap.Scripts = append(snap.Scripts, v)
	}
	for _, v := range s.rooms {
		snap.Rooms = append(snap.Rooms, v)
	}
	for _, v := range s.games {
		snap.Games = append(snap.Games, v)
	}
	for _, v := range s.materials {
		snap.Materials = append(snap.Materials, v)
	}
	s.dirty = false
	s.flushed = time.Now()
	s.mu.Unlock()

	sort.Slice(snap.Users, func(i, j int) bool { return snap.Users[i].CreatedAt.Before(snap.Users[j].CreatedAt) })
	b, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

func (s *Store) touch() { s.dirty = true }

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
	s.touch()
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
	s.touch()
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
	s.touch()
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

// DeleteScript removes a script record.
func (s *Store) DeleteScript(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scripts[id]; !ok {
		return ErrNotFound
	}
	delete(s.scripts, id)
	s.touch()
	return nil
}

// ---- rooms ----

// PutRoom stores a private copy of the room.
func (s *Store) PutRoom(r *domain.Room) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.UpdatedAt = time.Now().UTC()
	s.rooms[r.ID] = clonePtr(r)
	s.touch()
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

// DeleteRoom removes a room record (admin delete).
func (s *Store) DeleteRoom(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rooms[id]; !ok {
		return ErrNotFound
	}
	delete(s.rooms, id)
	s.touch()
	return nil
}

// ---- games ----

// PutGame stores a private copy of the game. The caller keeps its own
// authoritative value, so the flusher never reads a struct being mutated.
func (s *Store) PutGame(g *domain.Game) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.games[g.ID] = clonePtr(g)
	s.touch()
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

// DeleteGame removes a game record. A missing game is not an error: deleting a
// lobby room that never started should still succeed.
func (s *Store) DeleteGame(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.games[id]; ok {
		delete(s.games, id)
		s.touch()
	}
}

// ---- raw materials ----

// PutMaterial stores a private copy of the material record.
func (s *Store) PutMaterial(m *domain.RawMaterial) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.materials[m.ID] = clonePtr(m)
	s.touch()
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
	s.touch()
	return nil
}

// Dir returns the data directory (used for uploads).
func (s *Store) Dir() string { return s.dir }
