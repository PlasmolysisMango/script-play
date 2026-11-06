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
	// ToolWhisper is a line only one other seat can see. It leaves the public
	// floor untouched, which is why it is a tool of its own rather than a flag
	// on speak.
	ToolWhisper = "whisper"
)

// AgentAction is the single tool call an AI seat returns for one turn.
type AgentAction struct {
	Tool    string `json:"tool"`
	Text    string `json:"text,omitempty"`
	ReplyTo string `json:"replyTo,omitempty"`
	ClueID  string `json:"clueId,omitempty"`
	// To is the seat a whisper is addressed to.
	To     string `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`
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

// A bracketed aside is a stage direction — “（拍桌子）”, “（叹气）” — which no
// one should have to hear read aloud. Short ones with no sentence punctuation
// inside are dropped; a longer bracketed sentence is content and stays.
var bracketAside = regexp.MustCompile(`[（(]([^（）()]{1,30})[）)]`)

func stripDirection(s string) string {
	if !strings.ContainsAny(s, "（(") {
		return s
	}
	s = bracketAside.ReplaceAllStringFunc(s, func(m string) string {
		sub := bracketAside.FindStringSubmatch(m)
		if len(sub) < 2 || strings.ContainsAny(sub[1], "。！？!?；;") {
			return m
		}
		// No replacement space: in Chinese the aside sits between two characters
		// and a space there would leave a gap in the middle of the sentence.
		return ""
	})
	return s
}

// cleanSpeech strips harness chatter a provider can leave inside a text field.
// A field that is still nothing but an envelope yields no line at all: saying
// `{"tool":"speak"}` out loud is worse than saying nothing.
func cleanSpeech(s string) string {
	s = reminderBlock.ReplaceAllString(s, " ")
	s = stripDirection(s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || (strings.HasPrefix(s, "{") && strings.Contains(s, `"tool"`)) {
		return ""
	}
	return s
}

// speechToolsLocked advertises what this seat may do right now.
func (a *Actor) speechToolsLocked(seat domain.Seat, ownClues []string, targets []domain.Seat) []ToolSpec {
	tools := []ToolSpec{
		{Name: ToolSpeak, Args: `{"tool":"speak","text":"你要说的话","replyTo":"可选，座位id"}`, Desc: "说一句台词。这是最常用的动作。"},
		{Name: ToolPass, Args: `{"tool":"pass","reason":"为什么不说"}`, Desc: "这轮不发言。不想说、没有新信息、或不想回应时使用。"},
	}
	if len(targets) > 0 {
		tools = append(tools, ToolSpec{
			Name: ToolWhisper,
			Args: fmt.Sprintf(`{"tool":"whisper","to":"%s","text":"只给对方看的话"}`, targets[0].ID),
			Desc: "只对一个人耳语一句，其他玩家（包括主持人）看不到。适合单独交换情报、拉拢或串供；说完这一句本轮就结束。可选对象：" + seatList(a, targets),
		})
	}
	if len(ownClues) > 0 {
		tools = append(tools, ToolSpec{
			Name: ToolReveal,
			Args: fmt.Sprintf(`{"tool":"reveal","clueId":"%s","text":"可选，一边公开一边说的话"}`, ownClues[0]),
			Desc: "把一条线索原文直接贴到公屏。证据通常靠你发言转述，只有剧本要求亮出物证时才用它。可用线索 id：" + strings.Join(ownClues, "、"),
		})
	}
	return tools
}

// whisperReplyTools is the tiny toolset of a private answer: exactly one person
// to answer, or silence.
func whisperReplyTools(to domain.Seat) []ToolSpec {
	return []ToolSpec{
		{Name: ToolWhisper, Args: fmt.Sprintf(`{"tool":"whisper","to":"%s","text":"只给对方看的话"}`, to.ID), Desc: "只回给私聊你的这个人，其他人看不到。"},
		{Name: ToolPass, Args: `{"tool":"pass"}`, Desc: "不回应这条私聊。"},
	}
}

// seatList names seats as "id（角色名）" so the model can address them without
// guessing which id belongs to whom.
func seatList(a *Actor, seats []domain.Seat) string {
	names := make([]string, 0, len(seats))
	for _, s := range seats {
		names = append(names, fmt.Sprintf("%s（%s）", s.ID, a.speakerLocked(s)))
	}
	return strings.Join(names, "、")
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
