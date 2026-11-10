package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"script-play/internal/domain"
)

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	n := 0
	for _, line := range splitLines(b) {
		if len(line) > 0 {
			n++
		}
	}
	return n
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// TestMigrateLegacySnapshot pins the one-time conversion: the old single
// state.json must be split into entity files, events moved to the sidecar, and
// the original archived instead of deleted.
func TestMigrateLegacySnapshot(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	legacy := legacySnapshot{
		Users: []*domain.User{{ID: "user_1", Username: "Mango", CreatedAt: now}},
		Scripts: []*domain.ScriptRecord{{
			ID: "script_1", OwnerUserID: "user_1", Status: "published", UpdatedAt: now,
		}},
		Rooms: []*domain.Room{{ID: "room_1", Code: "ABC234", CreatedAt: now}},
		Games: []*domain.Game{{
			ID: "game_1", RoomID: "room_1", NextSeq: 2,
			Events: []domain.Event{
				{Seq: 1, Type: domain.EvSystem, Text: "one", Visibility: domain.VisPublic},
				{Seq: 2, Type: domain.EvSpeech, Text: "two", Visibility: domain.VisPublic},
			},
		}},
	}
	raw, err := json.MarshalIndent(legacy, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy state.json should be archived, stat err = %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "state.json.pre-split-*")); len(matches) != 1 {
		t.Fatalf("expected one archived snapshot, got %v", matches)
	}

	for _, p := range []string{
		filepath.Join(dir, "store", "users.json"),
		filepath.Join(dir, "store", "rooms.json"),
		filepath.Join(dir, "store", "scripts", "script_1.json"),
		filepath.Join(dir, "store", "games", "game_1.json"),
		filepath.Join(dir, "store", "games", "game_1.events.jsonl"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}

	if n := countLines(t, filepath.Join(dir, "store", "games", "game_1.events.jsonl")); n != 2 {
		t.Fatalf("expected 2 event rows, got %d", n)
	}

	// The body must not carry a second copy of the events.
	var body map[string]json.RawMessage
	b, err := os.ReadFile(filepath.Join(dir, "store", "games", "game_1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(body["game"], &inner); err != nil {
		t.Fatal(err)
	}
	if ev, ok := inner["events"]; ok && string(ev) != "null" {
		t.Fatalf("game body still carries events: %s", ev)
	}
	if string(body["eventsInLog"]) != "2" {
		t.Fatalf("eventsInLog = %s, want 2", body["eventsInLog"])
	}

	g, err := st.GetGame("game_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Events) != 2 || g.Events[1].Text != "two" {
		t.Fatalf("events not restored: %+v", g.Events)
	}
}

// TestGameEventsAppendOnly checks that a flush only appends new events and that
// the entity is rebuilt correctly after a restart.
func TestGameEventsAppendOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	g := &domain.Game{ID: "game_2", RoomID: "room_2", ScriptID: "script_2"}
	st.PutGame(g)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(dir, "store", "games", "game_2.events.jsonl")
	if n := countLines(t, eventsPath); n != 0 {
		t.Fatalf("no events yet, got %d rows", n)
	}

	g.Events = append(g.Events, domain.Event{Seq: 1, Type: domain.EvSystem, Text: "one", Visibility: domain.VisPublic, At: now})
	g.NextSeq = 1
	st.PutGame(g)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countLines(t, eventsPath); n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}

	g.Events = append(g.Events, domain.Event{Seq: 2, Type: domain.EvSpeech, Text: "two", Visibility: domain.VisPublic, At: now})
	g.NextSeq = 2
	st.PutGame(g)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countLines(t, eventsPath); n != 2 {
		t.Fatalf("append should not rewrite, got %d rows", n)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.GetGame("game_2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || got.Events[1].Seq != 2 || got.Events[0].Text != "one" {
		t.Fatalf("events not restored in order: %+v", got.Events)
	}
}

// TestExtraEventRowsAreCompacted simulates a crash between appending events and
// writing the body: the log has more lines than the body records, and the
// loader must ignore the surplus and compact the file.
func TestExtraEventRowsAreCompacted(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := &domain.Game{ID: "game_3", RoomID: "room_3"}
	st.PutGame(g)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	g.Events = append(g.Events, domain.Event{Seq: 1, Type: domain.EvSystem, Visibility: domain.VisPublic})
	g.NextSeq = 1
	st.PutGame(g)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Append a line that the body does not know about.
	eventsPath := filepath.Join(dir, "store", "games", "game_3.events.jsonl")
	f, err := os.OpenFile(eventsPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":2,"gameId":"game_3","type":"system","visibility":"public"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.GetGame("game_3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("surplus rows must be ignored, got %d events", len(got.Events))
	}
	if n := countLines(t, eventsPath); n != 1 {
		t.Fatalf("log should be compacted to 1 row, got %d", n)
	}
}

// TestFlushTouchesOnlyDirtyEntities guards the whole point of the split: saving
// one game must not rewrite any script file.
func TestFlushTouchesOnlyDirtyEntities(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	st.PutScript(&domain.ScriptRecord{ID: "script_4", OwnerUserID: "user_4", Status: "published"})
	st.PutGame(&domain.Game{ID: "game_4", RoomID: "room_4"})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "store", "scripts", "script_4.json")
	before, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(20 * time.Millisecond)
	st.PutGame(&domain.Game{ID: "game_4", RoomID: "room_4", Round: 2})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("script rewritten by an unrelated game flush: %v -> %v", before.ModTime(), after.ModTime())
	}

	g, err := st.GetGame("game_4")
	if err != nil {
		t.Fatal(err)
	}
	if g.Round != 2 {
		t.Fatalf("game round = %d, want 2", g.Round)
	}
}

func TestDeleteGameRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	g := &domain.Game{ID: "game_5", RoomID: "room_5"}
	st.PutGame(g)
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	gamePath := filepath.Join(dir, "store", "games", "game_5.json")
	eventsPath := filepath.Join(dir, "store", "games", "game_5.events.jsonl")

	st.DeleteGame("game_5")
	if _, err := os.Stat(gamePath); !os.IsNotExist(err) {
		t.Fatalf("game file should be gone, stat err = %v", err)
	}
	// No events were written, so the sidecar never existed; deleting must not fail.
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Fatalf("event log should be gone, stat err = %v", err)
	}
	if _, err := st.GetGame("game_5"); err != ErrNotFound {
		t.Fatalf("lookup after delete = %v, want ErrNotFound", err)
	}
}
