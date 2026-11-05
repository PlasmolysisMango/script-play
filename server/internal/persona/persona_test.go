package persona

import "testing"

func TestResolveRandomPicksConcretePreset(t *testing.T) {
	known := map[string]bool{}
	for _, p := range Presets() {
		known[p.ID] = true
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := Resolve(RandomSentinel)
		if !known[id] {
			t.Fatalf("Resolve(random) = %q, not a preset", id)
		}
		seen[id] = true
	}
	if len(seen) < 2 {
		t.Fatalf("random never varied across 50 rolls: %v", seen)
	}
}

func TestResolveEmptyAlsoRolls(t *testing.T) {
	if id := Resolve(""); id == "" || id == RandomSentinel {
		t.Fatalf("Resolve(\"\") = %q, want a concrete preset", id)
	}
}

func TestResolveKeepsExplicitID(t *testing.T) {
	for _, id := range []string{"candid", "guarded", "sly", "aggressive", "taciturn", "dramatic"} {
		if got := Resolve(id); got != id {
			t.Fatalf("Resolve(%q) = %q, want unchanged", id, got)
		}
	}
}
