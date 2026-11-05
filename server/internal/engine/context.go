package engine

import (
	"fmt"
	"strings"

	"script-play/internal/domain"
	"script-play/internal/llm"
	"script-play/internal/persona"
)

// PromptInput is everything the assembler needs. Note that it carries a
// Visible value, never a Game: prompt building physically cannot read hidden
// state.
type PromptInput struct {
	Visible     Visible
	Persona     persona.Persona
	Pressure    persona.RevealPressure
	Instruction string
	// Tools turns the turn into a tool call: the model answers with one JSON
	// object naming a tool instead of free prose. Empty means plain speech.
	Tools []ToolSpec
	// Fresh is what happened since this seat last acted. The transcript below
	// is a rolling window, so without this an agent cannot tell a new accusation
	// from an old one and answers the wrong line.
	Fresh []TranscriptLine
}

// PromptBuilder renders prompts from Visible data only.
type PromptBuilder struct {
	// MaxTranscript keeps the prompt bounded on small machines.
	MaxTranscript int
	// MaxChars hard-caps the user message.
	MaxChars int
}

// NewPromptBuilder returns a builder with sane defaults.
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{MaxTranscript: 40, MaxChars: 9000}
}

// Build renders the speaking request of one turn.
func (b *PromptBuilder) Build(in PromptInput) llm.Request {
	v := in.Visible
	if b.MaxTranscript > 0 && len(v.Transcript) > b.MaxTranscript {
		v.Transcript = v.Transcript[len(v.Transcript)-b.MaxTranscript:]
	}
	roleName := "旁观者"
	if v.OwnRole != nil {
		roleName = v.OwnRole.Name
	}

	system := fmt.Sprintf(`你正在一场剧本杀中扮演「%s」。你只能使用下面【可见信息】中出现的内容。
你必须严格遵守：
1. 可见信息之外的事实（他人的角色本、未公开线索、案件真相）你不知道、也无法引用，绝不能假装知道。
2. 只输出角色台词，可带少量括号动作描写；不要输出分析过程、编号、标题、或任何“作为AI”的自述。
3. 不要复述整段角色本，按你的性格与目的选择性透露。
4. 输出不超过 %d 句，直接说话，不要重复别人刚说过的话。

%s`, roleName, in.Persona.MaxSentences(), in.Persona.Describe())

	var u strings.Builder
	u.WriteString("【本局角色】\n")
	if v.OwnRole != nil {
		fmt.Fprintf(&u, "你：%s。%s\n", v.OwnRole.Name, v.OwnRole.Public.Intro)
	}
	u.WriteString("其他角色：\n")
	for _, r := range v.PublicRoles {
		if v.OwnRole != nil && r.Intro == v.OwnRole.Public.Intro {
			continue
		}
		fmt.Fprintf(&u, "- %s\n", oneLine(r.Intro))
	}

	if v.OwnRole != nil {
		u.WriteString("\n【你的角色本】\n")
		u.WriteString(strings.TrimSpace(v.OwnRole.Private.Script))
		u.WriteString("\n")
	}

	if v.OwnRole != nil && len(v.OwnRole.Private.Secrets) > 0 {
		u.WriteString("\n【你的秘密（是否透露由你决定）】\n")
		for _, s := range v.OwnRole.Private.Secrets {
			hint := "尽量回避，被逼问再考虑"
			if in.Persona.ShouldReveal(s.RevealCost, in.Pressure) {
				hint = "这次可以透露"
			}
			fmt.Fprintf(&u, "- [%s] %s（透露门槛 %.2f，建议：%s）\n", s.ID, oneLine(s.Text), s.RevealCost, hint)
		}
	}

	if to := v.OwnRole; to != nil && len(to.Private.Goals) > 0 {
		u.WriteString("\n【你的目标】\n")
		for _, g := range to.Private.Goals {
			fmt.Fprintf(&u, "- %s\n", oneLine(g))
		}
	}

	writeFacts(&u, v)
	writeClues(&u, v)
	writeFresh(&u, in.Fresh)
	writeTranscript(&u, v)

	fmt.Fprintf(&u, "\n【当前阶段】%s（第 %d 轮）\n", v.Phase.Name, maxInt(v.Round, 1))
	writeTools(&u, in.Tools)
	u.WriteString("\n【指令】\n")
	u.WriteString(strings.TrimSpace(in.Instruction))
	u.WriteString("\n")

	body := u.String()
	if b.MaxChars > 0 && len([]rune(body)) > b.MaxChars {
		r := []rune(body)
		body = string(r[len(r)-b.MaxChars:])
	}

	return llm.Request{
		SeatID:      v.Seat.ID,
		SpeakerName: roleName,
		RoomID:      v.RoomID,
		GameID:      v.GameID,
		PhaseID:     v.Phase.ID,
		Model:       v.Seat.ModelID,
		Provider:    "",
		System:      system,
		Messages:    []llm.Message{{Role: "user", Text: body}},
		Temperature: 0.8,
	}
}

// BuildAction renders a turn whose answer is one tool call.
func (b *PromptBuilder) BuildAction(in PromptInput) llm.Request {
	req := b.Build(in)
	req.JSON = true
	req.Temperature = 0.75
	return req
}

// writeTools advertises the available tool calls. The JSON-only contract is
// restated here as well as in the system prompt: a model that answers with
// prose must still be understood, so this is guidance, not a hard gate.
func writeTools(u *strings.Builder, tools []ToolSpec) {
	if len(tools) == 0 {
		return
	}
	u.WriteString("\n【你可以调用的工具】\n")
	for _, t := range tools {
		fmt.Fprintf(u, "- %s：%s\n  参数：%s\n", t.Name, t.Desc, t.Args)
	}
	u.WriteString("\n【输出格式】只输出一个 JSON 对象，形如 {\"tool\":\"speak\",\"text\":\"…\"}。不要输出多余文字。\n")
}

// BuildVote renders the voting request. The reply must be a JSON object.
func (b *PromptBuilder) BuildVote(in PromptInput) llm.Request {
	req := b.Build(in)
	req.JSON = true
	req.Temperature = 0.3
	return req
}

func writeFacts(u *strings.Builder, v Visible) {
	if len(v.OwnFacts) == 0 {
		return
	}
	u.WriteString("\n【你已知的事实】\n")
	for _, f := range v.OwnFacts {
		fmt.Fprintf(u, "- %s\n", oneLine(f.Text))
	}
}

func writeClues(u *strings.Builder, v Visible) {
	if len(v.OwnClues) > 0 {
		u.WriteString("\n【你掌握的线索】\n")
		for _, c := range v.OwnClues {
			fmt.Fprintf(u, "- [%s] %s：%s\n", c.ID, c.Title, oneLine(c.Content))
		}
	}
	if len(v.PublicClues) > 0 {
		u.WriteString("\n【公共线索】\n")
		for _, c := range v.PublicClues {
			fmt.Fprintf(u, "- [%s] %s：%s\n", c.ID, c.Title, oneLine(c.Content))
		}
	}
}

// writeFresh lists the events the seat has not seen yet. They are what makes
// an answer feel like a response instead of a monologue.
func writeFresh(u *strings.Builder, fresh []TranscriptLine) {
	if len(fresh) == 0 {
		return
	}
	u.WriteString("\n【刚刚发生】\n")
	for _, l := range fresh {
		speaker := l.Speaker
		if speaker == "" {
			speaker = "系统"
		}
		fmt.Fprintf(u, "- [%s] %s\n", speaker, oneLine(l.Text))
	}
}

func writeTranscript(u *strings.Builder, v Visible) {
	if len(v.Transcript) == 0 {
		u.WriteString("\n【公开发言记录】\n（暂无）\n")
		return
	}
	u.WriteString("\n【公开发言记录】\n")
	for i, l := range v.Transcript {
		speaker := l.Speaker
		if speaker == "" {
			switch l.Kind {
			case domain.EvDM:
				speaker = "主持人"
			default:
				speaker = "系统"
			}
		}
		fmt.Fprintf(u, "%d. [%s] %s\n", i+1, speaker, oneLine(l.Text))
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	return s
}

// stripSelfPrefix removes a leading "<role name>：" that some models add even
// though the UI already shows who is speaking.
func stripSelfPrefix(text, roleName string) string {
	if roleName == "" || text == "" {
		return text
	}
	for _, sep := range []string{"：", ":"} {
		prefix := roleName + sep
		if strings.HasPrefix(text, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(text, prefix))
		}
		// Some models wrap the name in括号: "【沈曼丽】：……".
		bracketed := "【" + roleName + "】" + sep
		if strings.HasPrefix(text, bracketed) {
			return strings.TrimSpace(strings.TrimPrefix(text, bracketed))
		}
	}
	return text
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
