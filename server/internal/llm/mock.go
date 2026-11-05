package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"script-play/internal/domain"
)

// Mock is a deterministic provider used by tests and by SP_LLM_PROVIDER=mock.
// It never touches the network so the whole game loop can run in CI.
type Mock struct {
	// Reply builds the answer for a request. When nil a default line is used.
	Reply func(Request) string
	// Delay is inserted between chunks so streaming can be observed.
	Delay time.Duration
}

// NewMock returns a mock provider.
func NewMock() *Mock { return &Mock{Delay: 0} }

// Name implements Provider.
func (m *Mock) Name() string { return "mock" }

// Stream implements Provider.
func (m *Mock) Stream(ctx context.Context, req Request, sink func(string) error) error {
	text := m.reply(req)
	if req.JSON {
		// Import jobs, tool calls and votes all ask for a JSON object; the
		// prompt says which one.
		prompt := lastMessage(req)
		switch {
		case strings.Contains(prompt, `"tool":"speak"`):
			text = mockAction(req)
		case strings.Contains(prompt, "vote"):
			text = mockVote(req)
		default:
			text = MockScriptJSON(req)
		}
	}
	chunks := chunk(text, 24)
	for _, c := range chunks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if m.Delay > 0 {
			time.Sleep(m.Delay)
		}
		if sink != nil {
			if err := sink(c); err != nil {
				return err
			}
		}
	}
	return nil
}

// mockLines keep the offline demo from looking like a broken record: every
// seat says something different, and the choice rotates with the phase. The
// lines stay generic on purpose — a canned line that names a prop the imported
// script never mentioned reads like a bug.
var mockLines = []string{
	"我只记得灯灭的那十分钟里，走廊尽头有人在跑。你们谁愿意先说说自己的时间线？",
	"先别急着怀疑我。我手上的东西可以解释，但我得先听听别人怎么说。",
	"我把自己的时间线说清楚了，剩下的部分我想听听别人怎么说。",
	"如果按时间线排，我和你们其中一个人对不上，我想先确认这一点。",
	"有人从刚才到现在一直在回避同一个问题，不如请他先回答。",
}

func (m *Mock) reply(req Request) string {
	if m.Reply != nil {
		return m.Reply(req)
	}
	// No name prefix: the UI already labels the speaker.
	return mockLines[(seatSeed(req.SeatID)+len(req.PhaseID))%len(mockLines)]
}

func seatSeed(seatID string) int {
	sum := 0
	for _, r := range seatID {
		sum += int(r)
	}
	return sum
}

// mockAction answers a tool-call turn. It always speaks: a canned "pass" would
// make the offline demo look like the table has gone quiet.
func mockAction(req Request) string {
	line := mockLines[(seatSeed(req.SeatID)+len(req.PhaseID))%len(mockLines)]
	return fmt.Sprintf(`{"tool":"speak","text":%q}`, line)
}

// mockVote picks somebody who actually spoke, so the tally is not all abstains.
func mockVote(req Request) string {
	for _, name := range bracketNames(lastMessage(req)) {
		if name == "主持人" || name == "系统" || name == req.SpeakerName {
			continue
		}
		return fmt.Sprintf(`{"vote":%q,"reason":"mock 依据发言记录投票"}`, name)
	}
	return `{"vote":"","reason":"mock 没有可投票的对象"}`
}

// bracketNames pulls "[名字]" entries out of the rendered transcript, skipping
// secret ids such as [s1].
func bracketNames(prompt string) []string {
	var out []string
	for i := 0; i < len(prompt); i++ {
		if prompt[i] != '[' {
			continue
		}
		end := strings.IndexByte(prompt[i:], ']')
		if end < 0 {
			break
		}
		name := prompt[i+1 : i+end]
		i += end
		if name == "" || (name[0] >= 'a' && name[0] <= 'z') {
			continue
		}
		out = append(out, name)
	}
	return out
}

func lastMessage(req Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Text
}

func chunk(s string, size int) []string {
	if size <= 0 {
		size = 24
	}
	runes := []rune(s)
	var out []string
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// MockScriptJSON is the canned extraction result for mock imports.
//
// It is not a throwaway placeholder: the offline/mock path still has to behave
// like a real extraction, otherwise the studio demo shows 甲/乙/丙 and the user
// has nothing to review. So it reads the pasted material for "角色：…" and
// "线索：…" sections, keeps the original names and clue texts, derives the seat
// range from the role count, and gives every phase a host line so the table is
// never silent.
func MockScriptJSON(req Request) string {
	source := mockSource(req)
	names, intros := mockRoles(source)
	clues := mockClues(source)
	title := mockTitle(source)

	roles := make([]domain.Role, 0, len(names))
	for i, name := range names {
		intro := intros[i]
		if intro == "" {
			intro = name + "，本局的关键人物之一。"
		}
		roles = append(roles, domain.Role{
			ID:     fmt.Sprintf("r%d", i+1),
			Name:   name,
			Public: domain.RolePublic{Intro: intro},
			Private: domain.RolePrivate{
				Script: fmt.Sprintf("你是%s。%s\n\n本局背景：%s\n\n你不知道凶手是谁，只能依据自己的时间线与搜到的线索发言。", name, intro, title),
				Goals:  []string{"洗清对自己的怀疑，并找出真正的凶手"},
			},
		})
	}

	clueList := make([]domain.Clue, 0, len(clues))
	pool := make([]string, 0, len(clues))
	for i, text := range clues {
		id := fmt.Sprintf("c%d", i+1)
		pool = append(pool, id)
		clueList = append(clueList, domain.Clue{
			ID:      id,
			Title:   mockClueTitle(text),
			Content: text,
		})
	}

	sc := domain.Script{
		SchemaVersion: 1,
		Meta: domain.Meta{
			Title:       title,
			Difficulty:  "easy",
			Summary:     mockFirstSentence(source),
			PlayerCount: domain.CountRange{Min: len(roles), Max: len(roles)},
		},
		Truth: domain.Truth{
			KillerRoleID: roles[0].ID,
			Method:       "（mock 抽取）手法待人工在校对页补全。",
			Summary:      "（mock 抽取）真相摘要待人工在校对页补全。",
		},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本",
				DMScript: fmt.Sprintf("欢迎来到《%s》。请先读完自己的角色本，再开始自我介绍。", mockBookTitle(title))},
			{ID: "p2", Type: domain.PhaseDiscussion, Name: "自我介绍与案情讨论", Round: 1, SpeechPerPlayer: 1, Order: domain.OrderRoundRobin,
				DMScript: "请各位依次介绍自己的角色与案发时的时间线，有疑问先聊清楚，随后进入第一轮搜证。"},
			{ID: "p3", Type: domain.PhaseSearch, Name: "第一轮搜证", Round: 1, PerPlayerPick: 1, CluePool: pool,
				DMScript: "第一轮搜证开始，每人可以查证一处线索，搜到的内容只有你自己能看到。"},
			{ID: "p4", Type: domain.PhaseDiscussion, Name: "第一轮公聊", Round: 1, SpeechPerPlayer: 1, Order: domain.OrderRoundRobin,
				DMScript: "开始第一轮公聊，请按顺序发言，亮出你愿意公开的线索。"},
			{ID: "p5", Type: domain.PhaseVote, Name: "投票", DMScript: "投票开始，请选出你认为的凶手，每人只能投一次。"},
			{ID: "p6", Type: domain.PhaseReveal, Name: "复盘", DMScript: "复盘时间，公布真相与完整时间线。"},
		},
		Roles: roles,
		Clues: clueList,
	}
	// The mock is also the safe fallback, so never hand the importer a script
	// that fails validation.
	if len(sc.Clues) == 0 {
		sc.Clues = []domain.Clue{{ID: "c1", Title: "待补充的线索", Content: "（mock 抽取）原文里没有识别到线索，请在校对页补充。"}}
		sc.Phases[2].CluePool = []string{"c1"}
	}
	blob, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(blob)
}

// mockSource strips the instruction paragraph the importer prepends.
func mockSource(req Request) string {
	source := lastMessage(req)
	if idx := strings.Index(source, "\n\n"); idx >= 0 {
		source = source[idx+2:]
	}
	return strings.TrimSpace(source)
}

// mockBookTitle trims the sentence punctuation so a title that already ends a
// sentence still reads well inside 《》.
func mockBookTitle(title string) string {
	return strings.TrimRight(title, "。．.！!？?")
}

// mockTitle keeps the opening sentence instead of cutting it mid-word.
func mockTitle(source string) string {
	if source == "" {
		return "mock 导入剧本"
	}
	line := strings.TrimSpace(strings.SplitN(source, "\n", 2)[0])
	runes := []rune(line)
	for i, r := range runes {
		if r == '。' || r == '！' || r == '？' {
			// Keep the terminator: the title should read like a sentence.
			if i+1 <= 24 {
				return "导入草稿 · " + string(runes[:i+1])
			}
			return "导入草稿 · " + string(runes[:24]) + "…"
		}
	}
	if len(runes) > 24 {
		return "导入草稿 · " + string(runes[:24]) + "…"
	}
	return "导入草稿 · " + line
}

func mockFirstSentence(source string) string {
	if source == "" {
		return ""
	}
	line := []rune(strings.TrimSpace(strings.SplitN(source, "\n", 2)[0]))
	for i, r := range line {
		if r == '。' || r == '！' || r == '？' {
			return string(line[:i+1])
		}
	}
	if len(line) > 60 {
		return string(line[:60]) + "…"
	}
	return string(line)
}

// mockRoles reads the "角色：A，说明；B，说明" section.
func mockRoles(source string) ([]string, []string) {
	seg := mockSection(source, "角色", "人物", "玩家")
	var names, intros []string
	for _, item := range splitAny(seg, "；;") {
		item = strings.Trim(item, " 。,，、")
		if item == "" {
			continue
		}
		name, intro := item, ""
		if cut := strings.IndexAny(item, "，,、：:"); cut > 0 {
			name = strings.TrimSpace(item[:cut])
			intro = strings.TrimSpace(strings.Trim(item[cut:], "，,、：: "))
		}
		if len([]rune(name)) > 12 {
			continue
		}
		names = append(names, name)
		intros = append(intros, intro)
		if len(names) == 8 {
			break
		}
	}
	if len(names) == 0 {
		// Validation needs at least one role, so fall back to a generic trio.
		return []string{"甲", "乙", "丙"}, []string{"", "", ""}
	}
	return names, intros
}

// mockClues reads the "线索：…；…" section, one clue per clause.
func mockClues(source string) []string {
	seg := mockSection(source, "线索", "证据", "证物")
	var out []string
	for _, item := range splitAny(seg, "；;") {
		item = strings.Trim(item, " 。,，、")
		if len([]rune(item)) < 2 {
			continue
		}
		out = append(out, item)
	}
	return out
}

// mockSection returns the text after the first matching "标签：" marker, up to
// the next section marker or the end of the paragraph.
func mockSection(source string, labels ...string) string {
	start := -1
	for _, label := range labels {
		for _, sep := range []string{"：", ":"} {
			if idx := strings.Index(source, label+sep); idx >= 0 && (start < 0 || idx < start) {
				start = idx + len(label) + len(sep)
			}
		}
	}
	if start < 0 {
		return ""
	}
	rest := source[start:]
	end := len(rest)
	for _, stop := range []string{"线索", "证据", "证物", "阶段", "真相", "时间线"} {
		for _, sep := range []string{"：", ":"} {
			if idx := strings.Index(rest, stop+sep); idx > 0 && idx < end {
				end = idx
			}
		}
	}
	if idx := strings.IndexByte(rest, '\n'); idx > 0 && idx < end {
		end = idx
	}
	return rest[:end]
}

func splitAny(s, seps string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) })
}

func mockClueTitle(text string) string {
	title := text
	if cut := strings.IndexAny(title, "，,、；;。"); cut > 0 {
		title = title[:cut]
	}
	runes := []rune(title)
	if len(runes) > 14 {
		return string(runes[:14]) + "…"
	}
	return title
}
