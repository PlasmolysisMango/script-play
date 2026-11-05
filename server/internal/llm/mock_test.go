package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"script-play/internal/domain"
)

// The mock is what runs in CI and in the offline demo, so its "extraction" has
// to keep the user's material instead of emitting 甲/乙/丙 placeholders.
func TestMockScriptJSONExtractsRealContent(t *testing.T) {
	source := "以下是剧本材料，请抽取为结构化剧本：\n\n" +
		"三人被困在岛上气象站。角色：苏禾，气象员，负责记录数据；周叙，维修工，晚上给电缆做保养；陈砚，外来者，来整理叔父遗物。" +
		"线索：被撕掉的一页值班日志，停在 21:30 到 22:10；码头缆绳上的时间标签，写着 21:45。22:00 全岛停电。"
	raw := MockScriptJSON(Request{Messages: []Message{{Role: "user", Text: source}}})

	var sc domain.Script
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		t.Fatalf("mock output is not a script: %v\n%s", err, raw)
	}
	if len(sc.Roles) != 3 {
		t.Fatalf("roles = %d want 3: %s", len(sc.Roles), raw)
	}
	names := []string{sc.Roles[0].Name, sc.Roles[1].Name, sc.Roles[2].Name}
	want := []string{"苏禾", "周叙", "陈砚"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("role %d = %q want %q", i, names[i], want[i])
		}
	}
	if len(sc.Clues) != 2 {
		t.Fatalf("clues = %d want 2: %+v", len(sc.Clues), sc.Clues)
	}
	if !strings.Contains(sc.Clues[0].Content, "21:30") {
		t.Errorf("clue lost the original text: %q", sc.Clues[0].Content)
	}
	if sc.Meta.PlayerCount.Max != 3 || sc.Meta.PlayerCount.Min != 3 {
		t.Errorf("playerCount = %+v, must follow the role count", sc.Meta.PlayerCount)
	}
	if strings.Contains(sc.Meta.Title, "\n") || !strings.Contains(sc.Meta.Title, "导入草稿") {
		t.Errorf("title = %q", sc.Meta.Title)
	}
	// The title keeps its full stop, but a book-title reference must not end up
	// as 《…。》.
	if strings.Contains(sc.Phases[0].DMScript, "。》") {
		t.Errorf("dmScript double punctuation: %q", sc.Phases[0].DMScript)
	}
	// Every phase carries a host line, otherwise the table opens in silence.
	for _, ph := range sc.Phases {
		if strings.TrimSpace(ph.DMScript) == "" {
			t.Errorf("phase %s has no dmScript", ph.ID)
		}
	}
	if errs := sc.Validate(); len(errs) != 0 {
		t.Errorf("mock output does not validate: %v", errs)
	}
}

// Pasting something without any structure must still yield a publishable draft.
func TestMockScriptJSONFallsBackSafely(t *testing.T) {
	raw := MockScriptJSON(Request{Messages: []Message{{Role: "user", Text: "以下是剧本材料，请抽取为结构化剧本：\n\n随便写两句没有结构的话。"}}})
	var sc domain.Script
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		t.Fatalf("mock output is not a script: %v\n%s", err, raw)
	}
	if len(sc.Roles) == 0 || len(sc.Clues) == 0 {
		t.Fatalf("fallback lost roles/clues: %s", raw)
	}
	if errs := sc.Validate(); len(errs) != 0 {
		t.Errorf("fallback does not validate: %v", errs)
	}
}
