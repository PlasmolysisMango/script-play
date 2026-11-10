package store

// Persistence layer: one file per entity instead of a single state.json.
//
// Layout under <data>/store:
//
//	users.json            whole table (small, changes rarely)
//	rooms.json            whole table (small)
//	materials.json        whole table (small)
//	scripts/<id>.json     one record per file
//	games/<id>.json       game body, events stripped out
//	games/<id>.events.jsonl  append-only event log, one Event per line
//	.layout               layout marker, 2 means "split layout"
//
// Why: the old single snapshot rewrote every script body (~1.7 MB) on each
// flush just because one event landed in one game. Splitting lets a flush touch
// only the entities that changed, and the event log turns the fastest growing
// part of the data (games.events was 60% of the file) into an O(new events)
// append.
//
// Crash consistency: small files are written to .tmp then renamed. For a game
// the event log is appended first and the body (which records how many events
// are in the log) is written second; if the process dies between the two, the
// extra lines are ignored on load and the log is compacted to the recorded
// count. A failed body write rewinds the log so the next flush does not append
// the same events twice.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"script-play/internal/domain"
)

const (
	keyUsers     = "users"
	keyRooms     = "rooms"
	keyMaterials = "materials"

	scriptKeyPrefix = "script:"
	gameKeyPrefix   = "game:"

	layoutMarker = ".layout"
)

func scriptKey(id string) string { return scriptKeyPrefix + id }
func gameKey(id string) string   { return gameKeyPrefix + id }

// legacySnapshot is the single-file layout used before the split.
type legacySnapshot struct {
	Users     []*domain.User         `json:"users"`
	Scripts   []*domain.ScriptRecord `json:"scripts"`
	Rooms     []*domain.Room         `json:"rooms"`
	Games     []*domain.Game         `json:"games"`
	Materials []*domain.RawMaterial  `json:"materials"`
}

// gameFile is the on-disk wrapper around a game body. Events live in the
// sidecar log, so the body only records how many of them belong to it.
type gameFile struct {
	Game        *domain.Game `json:"game"`
	EventsInLog int          `json:"eventsInLog"`
}

type flushItem struct {
	key  string
	ver  uint64
	kind string
	id   string
}

// Open loads (or creates) the store under dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	base := filepath.Join(dir, "store")
	for _, sub := range []string{"", "scripts", "games"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir store dir: %w", err)
		}
	}
	s := &Store{
		dir:       dir,
		base:      base,
		users:     map[string]*domain.User{},
		usernames: map[string]string{},
		scripts:   map[string]*domain.ScriptRecord{},
		rooms:     map[string]*domain.Room{},
		games:     map[string]*domain.Game{},
		materials: map[string]*domain.RawMaterial{},
		dirty:     map[string]uint64{},
		vers:      map[string]uint64{},
		eventRows: map[string]int{},
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
	if _, err := os.Stat(filepath.Join(s.base, layoutMarker)); err != nil {
		legacy := filepath.Join(s.dir, "state.json")
		if _, statErr := os.Stat(legacy); statErr == nil {
			return s.migrate(legacy)
		}
	}
	if err := s.loadParts(); err != nil {
		return err
	}
	return s.writeMarker()
}

func (s *Store) loadParts() error {
	if err := s.readUsers(); err != nil {
		return err
	}
	if err := s.readRooms(); err != nil {
		return err
	}
	if err := s.readMaterials(); err != nil {
		return err
	}
	if err := s.readScripts(); err != nil {
		return err
	}
	return s.readGames()
}

func (s *Store) readUsers() error {
	b, err := os.ReadFile(filepath.Join(s.base, keyUsers+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read users: %w", err)
	}
	var list []*domain.User
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("decode users: %w", err)
	}
	for _, u := range list {
		if u == nil || u.ID == "" {
			continue
		}
		s.users[u.ID] = u
		s.usernames[strings.ToLower(u.Username)] = u.ID
	}
	return nil
}

func (s *Store) readRooms() error {
	b, err := os.ReadFile(filepath.Join(s.base, keyRooms+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read rooms: %w", err)
	}
	var list []*domain.Room
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("decode rooms: %w", err)
	}
	for _, r := range list {
		if r != nil && r.ID != "" {
			s.rooms[r.ID] = r
		}
	}
	return nil
}

func (s *Store) readMaterials() error {
	b, err := os.ReadFile(filepath.Join(s.base, keyMaterials+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read materials: %w", err)
	}
	var list []*domain.RawMaterial
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("decode materials: %w", err)
	}
	for _, m := range list {
		if m != nil && m.ID != "" {
			s.materials[m.ID] = m
		}
	}
	return nil
}

func (s *Store) readScripts() error {
	entries, err := os.ReadDir(filepath.Join(s.base, "scripts"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read scripts dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.base, "scripts", e.Name()))
		if err != nil {
			return fmt.Errorf("read script %s: %w", e.Name(), err)
		}
		var rec domain.ScriptRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return fmt.Errorf("decode script %s: %w", e.Name(), err)
		}
		if rec.ID == "" {
			continue
		}
		cp := rec
		s.scripts[cp.ID] = &cp
	}
	return nil
}

func (s *Store) readGames() error {
	entries, err := os.ReadDir(filepath.Join(s.base, "games"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read games dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".events.jsonl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.base, "games", name))
		if err != nil {
			return fmt.Errorf("read game %s: %w", name, err)
		}
		var gf gameFile
		if err := json.Unmarshal(b, &gf); err != nil {
			return fmt.Errorf("decode game %s: %w", name, err)
		}
		if gf.Game == nil || gf.Game.ID == "" {
			continue
		}
		g := gf.Game
		events, lines, err := readEvents(s.eventsPath(g.ID))
		if err != nil {
			return err
		}
		// A sidecar (or its recorded count) wins over an inline copy; games
		// migrated from the old snapshot have their events inline only until the
		// first flush moves them out.
		if gf.EventsInLog > 0 || lines > 0 {
			n := gf.EventsInLog
			if n > len(events) {
				// The sidecar lost lines (crash or manual edit): trust what is there.
				n = len(events)
			}
			g.Events = events[:n]
			if lines != n {
				if err := rewriteEvents(s.eventsPath(g.ID), g.Events); err != nil {
					return err
				}
			}
		}
		s.games[g.ID] = g
		s.eventRows[g.ID] = len(g.Events)
	}
	return nil
}

// migrate converts the old single-file snapshot into the split layout and keeps
// the original around as state.json.pre-split-<ts> for rollback.
func (s *Store) migrate(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read legacy state: %w", err)
	}
	var snap legacySnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return fmt.Errorf("decode legacy state: %w", err)
	}

	s.mu.Lock()
	for _, u := range snap.Users {
		if u == nil || u.ID == "" {
			continue
		}
		s.users[u.ID] = u
		s.usernames[strings.ToLower(u.Username)] = u.ID
	}
	for _, sc := range snap.Scripts {
		if sc != nil && sc.ID != "" {
			s.scripts[sc.ID] = sc
		}
	}
	for _, r := range snap.Rooms {
		if r != nil && r.ID != "" {
			s.rooms[r.ID] = r
		}
	}
	for _, g := range snap.Games {
		if g != nil && g.ID != "" {
			s.games[g.ID] = g
			s.eventRows[g.ID] = 0
		}
	}
	for _, m := range snap.Materials {
		if m != nil && m.ID != "" {
			s.materials[m.ID] = m
		}
	}
	s.mark(keyUsers)
	s.mark(keyRooms)
	s.mark(keyMaterials)
	for id := range s.scripts {
		s.mark(scriptKey(id))
	}
	for id := range s.games {
		s.mark(gameKey(id))
	}
	s.mu.Unlock()

	if err := s.Flush(); err != nil {
		return fmt.Errorf("write split layout: %w", err)
	}

	backup := filepath.Join(s.dir, "state.json.pre-split-"+time.Now().Format("20060102T150405"))
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("archive legacy state: %w", err)
	}
	if err := s.writeMarker(); err != nil {
		return err
	}
	slog.Info("store split from legacy snapshot",
		"backup", backup,
		"users", len(s.users), "scripts", len(s.scripts),
		"rooms", len(s.rooms), "games", len(s.games))
	return nil
}

func (s *Store) loop() {
	defer close(s.done)
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-s.stop:
			_ = s.Flush()
			return
		case <-t.C:
			if err := s.Flush(); err != nil {
				// Retrying silently every tick would bury the real problem, so log
				// only when the message changes. dirty entries stay queued.
				if msg := err.Error(); msg != lastErr {
					slog.Warn("store flush failed", "err", err)
					lastErr = msg
				}
			} else {
				lastErr = ""
			}
		}
	}
}

// Close stops the flusher and writes the last snapshot.
func (s *Store) Close() error {
	close(s.stop)
	<-s.done
	return s.Flush()
}

// Flush writes every dirty entity. A flush only clears an entry when it wrote
// the same version that is still current: a Put that lands mid-write keeps the
// entity dirty so the next tick picks it up. Writing the file first and marking
// clean afterwards is what keeps a failed write from losing the update.
func (s *Store) Flush() error {
	items := s.collectDirty()
	if len(items) == 0 {
		return nil
	}
	var firstErr error
	for _, it := range items {
		var err error
		switch it.kind {
		case keyUsers:
			err = s.writeUsers()
		case keyRooms:
			err = s.writeRooms()
		case keyMaterials:
			err = s.writeMaterials()
		case "script":
			err = s.writeScript(it.id)
		case "game":
			err = s.writeGame(it.id)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("flush %s: %w", it.key, err)
			}
			continue
		}
		s.markClean(it)
	}
	if firstErr == nil {
		s.mu.Lock()
		s.flushed = time.Now()
		s.mu.Unlock()
	}
	return firstErr
}

// mark records a pending write. Callers must hold s.mu.
func (s *Store) mark(key string) {
	s.vers[key]++
	s.dirty[key] = s.vers[key]
}

func (s *Store) collectDirty() []flushItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]flushItem, 0, len(s.dirty))
	for key, ver := range s.dirty {
		switch {
		case key == keyUsers || key == keyRooms || key == keyMaterials:
			out = append(out, flushItem{key: key, ver: ver, kind: key})
		case strings.HasPrefix(key, scriptKeyPrefix):
			id := strings.TrimPrefix(key, scriptKeyPrefix)
			if _, ok := s.scripts[id]; ok {
				out = append(out, flushItem{key: key, ver: ver, kind: "script", id: id})
			} else {
				delete(s.dirty, key)
			}
		case strings.HasPrefix(key, gameKeyPrefix):
			id := strings.TrimPrefix(key, gameKeyPrefix)
			if _, ok := s.games[id]; ok {
				out = append(out, flushItem{key: key, ver: ver, kind: "game", id: id})
			} else {
				delete(s.dirty, key)
			}
		default:
			delete(s.dirty, key)
		}
	}
	return out
}

// markClean drops the dirty flag unless the entity changed again mid-write. If
// the entity was deleted while its file was being written, the file is removed
// again so a delete never leaves a resurrected record behind.
func (s *Store) markClean(it flushItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vers[it.key] != it.ver {
		return
	}
	delete(s.dirty, it.key)
	switch it.kind {
	case "script":
		if _, ok := s.scripts[it.id]; !ok {
			_ = os.Remove(s.scriptPath(it.id))
		}
	case "game":
		if _, ok := s.games[it.id]; !ok {
			_ = os.Remove(s.gamePath(it.id))
			_ = os.Remove(s.eventsPath(it.id))
			delete(s.eventRows, it.id)
		}
	}
}

func (s *Store) writeUsers() error {
	s.mu.RLock()
	list := make([]*domain.User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.base, keyUsers+".json"), b)
}

func (s *Store) writeRooms() error {
	s.mu.RLock()
	list := make([]*domain.Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		list = append(list, r)
	}
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.base, keyRooms+".json"), b)
}

func (s *Store) writeMaterials() error {
	s.mu.RLock()
	list := make([]*domain.RawMaterial, 0, len(s.materials))
	for _, m := range s.materials {
		list = append(list, m)
	}
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.base, keyMaterials+".json"), b)
}

func (s *Store) writeScript(id string) error {
	s.mu.RLock()
	rec, ok := s.scripts[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	b, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(s.scriptPath(id), b)
}

func (s *Store) writeGame(id string) error {
	s.mu.RLock()
	g, ok := s.games[id]
	prev := s.eventRows[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	events := g.Events
	if err := writeEvents(s.eventsPath(id), events, prev); err != nil {
		return err
	}

	// Events are stripped from the body: the sidecar owns them. The recorded
	// count is the only thing the loader uses to tell the two apart.
	body := *g
	body.Events = nil
	payload, err := json.Marshal(&gameFile{Game: &body, EventsInLog: len(events)})
	if err != nil {
		return err
	}
	if err := writeAtomic(s.gamePath(id), payload); err != nil {
		// Rewind the sidecar, otherwise the next flush would append the same
		// events a second time.
		if rbErr := rewriteEvents(s.eventsPath(id), events[:prev]); rbErr != nil {
			slog.Warn("rewind event log failed", "game", id, "err", rbErr)
		}
		return err
	}

	s.mu.Lock()
	s.eventRows[id] = len(events)
	s.mu.Unlock()
	return nil
}

// writeEvents appends only the events that are not in the log yet. A shrinking
// event list (never happens today, but keeps the invariant honest) rewrites the
// whole log instead.
func writeEvents(path string, events []domain.Event, prev int) error {
	switch {
	case len(events) < prev:
		return rewriteEvents(path, events)
	case len(events) == prev:
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, ev := range events[prev:] {
		b, err := json.Marshal(ev)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := w.Write(b); err != nil {
			_ = f.Close()
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func rewriteEvents(path string, events []domain.Event) error {
	var buf bytes.Buffer
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return writeAtomic(path, buf.Bytes())
}

func readEvents(path string) ([]domain.Event, int, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read events %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// A long speech or a generated clue can be large; the default 64 KB line
	// limit would silently truncate the log.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var out []domain.Event
	lines := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev domain.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return out, lines, fmt.Errorf("decode event %s line %d: %w", filepath.Base(path), lines+1, err)
		}
		out = append(out, ev)
		lines++
	}
	return out, lines, sc.Err()
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) writeMarker() error {
	return os.WriteFile(filepath.Join(s.base, layoutMarker), []byte("2\n"), 0o644)
}

func (s *Store) scriptPath(id string) string {
	return filepath.Join(s.base, "scripts", id+".json")
}

func (s *Store) gamePath(id string) string {
	return filepath.Join(s.base, "games", id+".json")
}

func (s *Store) eventsPath(id string) string {
	return filepath.Join(s.base, "games", id+".events.jsonl")
}
