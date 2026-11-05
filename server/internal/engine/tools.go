package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"script-play/internal/domain"
)

// AI seats act through tool calls instead of "the stage woke you up, say
// something": one turn is one tool call. That keeps speaking, declining and
// revealing a clue on the same footing, and it lets a silent persona actually
// stay silent.
const (
	ToolSpeak  = "speak"
	ToolPass   = "pass"
	ToolReveal = "reveal"
)

// AgentAction is the single tool call an AI seat returns for one turn.
type AgentAction struct {
	Tool    string `json:"tool"`
	Text    string `json:"text,omitempty"`
	ReplyTo string `json:"replyTo,omitempty"`
	ClueID  string `json:"clueId,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ToolSpec is one tool advertised to the model.
type ToolSpec struct {
	Name string
	Args string
	Desc string
}

// parseAction reads a tool call out of the model output.
//
// The *last* decodable envelope wins. A CLI provider sometimes echoes the
// prompt back before answering — transcript lines, `<system-reminder>` blocks
// and all — so the first `{` in the output is usually history rather than this
// turn, and slicing from the first `{` to the last `}` yields one unparseable
// blob. Trying every brace and keeping the last valid envelope picks the answer
// the model actually ended on.
func parseAction(raw string) AgentAction {
	raw = strings.TrimSpace(raw)
	var act AgentAction
	found := false
	for i := 0; i < len(raw); i++ {
		if raw[i] != '{' {
			continue
		}
		var cand AgentAction
		// Decode stops after one value, so trailing prose is harmless.
		if err := json.NewDecoder(strings.NewReader(raw[i:])).Decode(&cand); err != nil {
			continue
		}
		if strings.TrimSpace(cand.Tool) == "" {
			continue
		}
		act, found = cand, true
	}
	if found {
		return act
	}
	// No envelope at all. Prose is still the answer; echoed prompt text is not,
	// and it must never reach the table as a line of dialogue.
	if looksMachined(raw) {
		return AgentAction{Tool: ToolPass, Reason: "模型没有给出工具调用"}
	}
	return AgentAction{Tool: ToolSpeak, Text: raw}
}

// looksMachined reports whether a reply is harness chatter rather than prose.
func looksMachined(s string) bool {
	return strings.HasPrefix(s, "{") ||
		strings.Contains(s, "<system-reminder") ||
		strings.Contains(s, "```") ||
		strings.Contains(s, `"tool"`)
}

var reminderBlock = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// cleanSpeech strips harness chatter a provider can leave inside a text field.
// A field that is still nothing but an envelope yields no line at all: saying
// `{"tool":"speak"}` out loud is worse than saying nothing.
func cleanSpeech(s string) string {
	s = reminderBlock.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || (strings.HasPrefix(s, "{") && strings.Contains(s, `"tool"`)) {
		return ""
	}
	return s
}

// speechToolsLocked advertises what this seat may do right now.
func (a *Actor) speechToolsLocked(seat domain.Seat, ownClues []string) []ToolSpec {
	tools := []ToolSpec{
		{Name: ToolSpeak, Args: `{"tool":"speak","text":"你要说的话","replyTo":"可选，座位id"}`, Desc: "说一句台词。这是最常用的动作。"},
		{Name: ToolPass, Args: `{"tool":"pass","reason":"为什么不说"}`, Desc: "这轮不发言。不想说、没有新信息、或不想回应时使用。"},
	}
	if len(ownClues) > 0 {
		tools = append(tools, ToolSpec{
			Name: ToolReveal,
			Args: fmt.Sprintf(`{"tool":"reveal","clueId":"%s","text":"可选，一边公开一边说的话"}`, ownClues[0]),
			Desc: "公开你掌握的一条线索，让全桌都看到。可用线索 id：" + strings.Join(ownClues, "、"),
		})
	}
	return tools
}

// thinking publishes the transient "seat is composing" marker that drives the
// animation on the table. Deltas and thinking markers are never stored: they
// belong to the live stream only.
func (a *Actor) thinking(seat domain.Seat, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hub == nil || a.finished {
		return
	}
	a.hub.Publish(domain.Event{
		Type:    domain.EvThinking,
		GameID:  a.g.ID,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Meta:    map[string]string{"state": state},
	})
}
