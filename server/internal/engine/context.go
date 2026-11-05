package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

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

// Per-prompt budgets. The transcript is the only elastic part: a seat's own
// book and the instruction always survive.
const (
	// recapClip is how much of a stored recap one prompt pastes.
	recapClip = 700
	// digestClip is how many characters a folded line keeps.
	digestClip = 60
	// digestLimit caps how many speakers the folded echo names.
	digestLimit = 6
)

// NewPromptBuilder returns a builder with sane defaults.
//
// MaxChars sits above the longest published role book (K7 is ~7000 runes) plus
// a real argument, so the budget bites on memory instead of on identity.
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{MaxTranscript: 40, MaxChars: 12000}
}

// Build renders the speaking request of one turn.
func (b *PromptBuilder) Build(in PromptInput) llm.Request {
	v := in.Visible
	if v.RecapUpto > 0 {
		// Those lines live inside the recap now: echoing both pays twice for the
		// same history, and that is what used to push the book out of the window.
		v.Transcript = afterSeq(v.Transcript, v.RecapUpto)
	}
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
2. 只输出角色台词，不要写括号里的动作、神态或舞台提示（例如（拍桌子）、（叹气）、（笑）），也不要输出分析过程、编号、标题、或任何“作为AI”的自述。
3. 不要复述整段角色本，按你的性格与目的选择性透露。
4. 输出不超过 %d 句，直接说话，不要重复别人刚说过的话。

%s`, roleName, in.Persona.MaxSentences(), in.Persona.Describe())

	head := b.head(v, in)
	tail := b.tail(v, in)
	if b.MaxChars > 0 {
		// A book that eats the whole budget turns the agent into a reader who
		// never heard the table, so the book gives way first — at its middle,
		// never at its opening, which is who the character is.
		limit := b.MaxChars * 7 / 10
		if fixed := runes(head) + runes(tail); fixed > limit && v.OwnRole != nil {
			cp := *v.OwnRole
			cp.Private.Script = clipMiddle(cp.Private.Script, runes(cp.Private.Script)-(fixed-limit))
			v.OwnRole = &cp
			head = b.head(v, in)
		}
	}
	avail := 1 << 30
	if b.MaxChars > 0 {
		avail = b.MaxChars - runes(head) - runes(tail)
	}
	middle := renderMiddle(v, in.Fresh, avail)

	body := head + middle + tail
	if b.MaxChars > 0 && runes(body) > b.MaxChars {
		// Last resort: nothing else is droppable, keep both ends so the
		// instruction still reaches the model.
		body = clipMiddle(body, b.MaxChars)
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

// head is the identity half of the prompt: the seat, its book, and everything
// it knows privately. It is assembled before the budget is split because it is
// the part that must survive a long game untouched.
func (b *PromptBuilder) head(v Visible, in PromptInput) string {
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
	return u.String()
}

// tail frames the turn: where the story stands, what can be called, and what
// the seat is asked to do. It is appended last and kept whole.
func (b *PromptBuilder) tail(v Visible, in PromptInput) string {
	var u strings.Builder
	fmt.Fprintf(&u, "\n【当前阶段】%s（第 %d 轮）\n", v.Phase.Name, maxInt(v.Round, 1))
	writeTools(&u, in.Tools)
	u.WriteString("\n【指令】\n")
	u.WriteString(strings.TrimSpace(in.Instruction))
	u.WriteString("\n")
	return u.String()
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
		// Nobody else can see these: the only ways out are describing what they
		// showed, or the explicit reveal tool.
		u.WriteString("\n【你掌握的线索（只有你看得到；要说出来别人才知道，也可以调用 reveal 公开）】\n")
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

// renderMiddle assembles the elastic half of the prompt — what the table said —
// against a character budget. It spends in order of usefulness: the recap of
// history that no longer fits, the ticker of what just happened, whole recent
// lines, and finally a digest of the lines that had to go.
//
// A line is kept whole or dropped altogether: half a sentence reads as a
// different accusation, which is worse than not remembering it at all.
func renderMiddle(v Visible, fresh []TranscriptLine, avail int) string {
	if avail <= 0 {
		return ""
	}
	var u strings.Builder
	spent := 0

	if recap := strings.TrimSpace(v.Recap); recap != "" {
		// Oldest material, smallest slice: enough to remind, never enough to
		// crowd the table out.
		if budget := min(recapClip, avail/3); budget >= 80 {
			text := clipText(recap, budget)
			u.WriteString("\n【前情提要（更早的发言已压缩）】\n")
			u.WriteString(text)
			u.WriteString("\n")
			spent += runes(text)
		}
	}

	if len(fresh) > 0 {
		// The ticker answers "what am I replying to", so it keeps the newest
		// lines, then puts them back in reading order.
		budget := max(avail/4, 240)
		start := len(fresh)
		used := 0
		for i := len(fresh) - 1; i >= 0 && len(fresh)-i <= 12; i-- {
			cost := lineCost(fresh[i])
			if used+cost > budget && start < len(fresh) {
				break
			}
			start = i
			used += cost
		}
		if start < len(fresh) {
			u.WriteString("\n【刚刚发生】\n")
			for _, l := range fresh[start:] {
				fmt.Fprintf(&u, "- [%s] %s\n", speakerOf(l), oneLine(l.Text))
			}
			spent += used
		}
	}

	remaining := avail - spent
	if len(v.Transcript) == 0 {
		if spent == 0 {
			u.WriteString("\n【公开发言记录】\n（暂无）\n")
		}
		return u.String()
	}
	if remaining <= 0 {
		return u.String()
	}

	// Whole lines first, newest backwards. If they all fit, done; otherwise
	// hold back 30% of the budget so the lines that fall off can still be
	// summarised instead of silently vanishing.
	keep, used := tailFit(v.Transcript, remaining)
	if keep < len(v.Transcript) {
		keep, used = tailFit(v.Transcript, remaining*7/10)
	}
	_ = used
	dropped := v.Transcript[:len(v.Transcript)-keep]
	shown := v.Transcript[len(v.Transcript)-keep:]

	if len(dropped) > 0 {
		if digest, folded := digestLines(dropped); digest != "" {
			u.WriteString("\n【更早的发言（要点）】\n")
			u.WriteString(digest)
			if rest := len(dropped) - folded; rest > 0 {
				fmt.Fprintf(&u, "（另有 %d 条更早的发言未列出）\n", rest)
			}
		}
	}

	u.WriteString("\n【公开发言记录】\n")
	for i, l := range shown {
		fmt.Fprintf(&u, "%d. [%s] %s\n", i+1, speakerOf(l), oneLine(l.Text))
	}
	return u.String()
}

// tailFit counts how many trailing lines fit in budget.
func tailFit(lines []TranscriptLine, budget int) (int, int) {
	keep, used := 0, 0
	for i := len(lines) - 1; i >= 0; i-- {
		cost := lineCost(lines[i])
		if used+cost > budget && keep > 0 {
			break
		}
		keep++
		used += cost
	}
	return keep, used
}

// digestLines folds lines into one bullet per speaker, keeping each speaker's
// newest line: the question a seat asks of an old log is "where does so-and-so
// stand", not "what did they say third".
func digestLines(lines []TranscriptLine) (string, int) {
	order := make([]string, 0, digestLimit)
	latest := make(map[string]string, digestLimit)
	for _, l := range lines {
		sp := speakerOf(l)
		if _, seen := latest[sp]; !seen {
			if len(order) >= digestLimit {
				continue
			}
			order = append(order, sp)
		}
		latest[sp] = clipText(oneLine(l.Text), digestClip)
	}
	var u strings.Builder
	for _, sp := range order {
		fmt.Fprintf(&u, "- %s：%s\n", sp, latest[sp])
	}
	return u.String(), len(order)
}

// lineCost estimates the rendered width of one transcript line.
func lineCost(l TranscriptLine) int {
	return runes(oneLine(l.Text)) + runes(speakerOf(l)) + 12
}

// speakerOf names the line's author for the prompt.
func speakerOf(l TranscriptLine) string {
	if l.Speaker != "" {
		return l.Speaker
	}
	if l.Kind == domain.EvDM {
		return "主持人"
	}
	return "系统"
}

// afterSeq drops the lines a recap already covers. The transcript is ordered by
// seq, so a scan is enough.
func afterSeq(lines []TranscriptLine, upto int) []TranscriptLine {
	i := 0
	for i < len(lines) && lines[i].Seq <= upto {
		i++
	}
	return lines[i:]
}

// runes is the budget unit: characters, not bytes, because a Chinese line costs
// one unit per character to the reader and three to the wire.
func runes(s string) int { return utf8.RuneCountInString(s) }

// clipText shortens a block to n characters, marked in place.
func clipText(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// clipMiddle shortens a block by dropping its middle, marked in place. A role
// book opens with who the character is and then recounts events; losing the
// middle of that account is survivable, losing the opening is not.
func clipMiddle(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	const mark = "\n……（中段省略）……\n"
	markLen := utf8.RuneCountInString(mark)
	if limit <= markLen+40 {
		return string(r[:limit])
	}
	keep := limit - markLen
	headN := keep * 7 / 10
	tailN := keep - headN
	return string(r[:headN]) + mark + string(r[len(r)-tailN:])
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
