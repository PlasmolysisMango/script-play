package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"script-play/internal/domain"
	"script-play/internal/llm"
)

// Recap compression budgets.
//
// A phase of talk is far longer than the prompt that has to remember it, so the
// constraint is not "how much can we read" but "what has to survive": who
// accused whom, what was admitted, and what was made public.
const (
	// recapSource caps how much raw log one compression reads. Everything older
	// is already folded into the previous recap.
	recapSource = 6000
	// recapMinLines is where compressing stops paying for itself: a handful of
	// lines is cheaper to keep whole than to replace with a retelling.
	recapMinLines = 8
	// recapOutput caps the stored recap, which every prompt pastes.
	recapOutput = 500
)

// recapSystem is the compressor's whole instruction. It is deliberately narrow:
// a summariser that is allowed to reason invents a solution and then every seat
// "remembers" it.
const recapSystem = `你是一桌剧本杀的记录员。把已经发生的公开内容压缩成一段前情提要，供后面的人快速回忆。
必须遵守：
1. 只写下面出现过的人名、线索与发言。不要推测、不要补全、不要指出谁是凶手，也不要评价谁可疑。
2. 优先保留：谁指控了谁、谁承认或否认了什么、谁公开了哪条线索、主持人宣布了什么流程。
3. 用「角色名：一句话」的短句，按时间顺序罗列，不用段落，不写开场白与总结。
4. 不超过 240 字。只输出前情提要本身。`

// startRecap folds everything said so far into a short retelling, in the
// background and at most once at a time.
//
// Compression is best-effort by design: if the model is slow, busy or broken
// the phase carries on with the deterministic budget, and the prompt still
// stays bounded. Nothing here may delay a phase from starting.
func (a *Actor) startRecap(gen int) {
	a.mu.Lock()
	if a.finished || gen != a.phaseGen || a.recapBusy || a.reg == nil {
		a.mu.Unlock()
		return
	}
	upto := a.g.NextSeq - 1
	body := a.recapSourceLocked(upto)
	if body == "" {
		a.mu.Unlock()
		return
	}
	phaseID, gameID, roomID := "", a.g.ID, a.g.RoomID
	if ph := a.m.Current(a.g); ph != nil {
		phaseID = ph.ID
	}
	a.recapBusy = true
	a.mu.Unlock()

	go a.runRecap(gameID, roomID, phaseID, body, upto)
}

// runRecap performs one compression and stores the result.
func (a *Actor) runRecap(gameID, roomID, phaseID, body string, upto int) {
	defer func() {
		a.mu.Lock()
		a.recapBusy = false
		a.mu.Unlock()
	}()

	// The actor's own lifetime, not the phase's: a phase that ends while the
	// model is still thinking must not throw away a nearly finished recap.
	ctx := a.base
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, a.cfg.agent())
	defer cancel()

	text, _, err := a.reg.Complete(cctx, llm.Request{
		GameID:      gameID,
		RoomID:      roomID,
		PhaseID:     phaseID,
		System:      recapSystem,
		Messages:    []llm.Message{{Role: "user", Text: body}},
		Temperature: 0.2,
	}, nil)
	if err != nil || strings.TrimSpace(text) == "" {
		// Silent downgrade: the next phase retries, and until then the prompt
		// falls back to rendering the log it has.
		return
	}
	a.applyRecap(text, upto, phaseID)
}

// recapSourceLocked renders what the compressor reads: the previous recap plus
// every public line since. Callers hold mu.
func (a *Actor) recapSourceLocked(upto int) string {
	floor, previous := 0, ""
	if r := a.g.Recap; r != nil {
		floor, previous = r.UptoSeq, strings.TrimSpace(r.Text)
	}

	var lines []string
	for _, ev := range a.g.Events {
		if ev.Seq <= floor || ev.Seq > upto {
			continue
		}
		// A recap is read by every seat, so it may only fold public material:
		// a host-only DM line never enters it.
		if ev.Visibility != "" && ev.Visibility != domain.VisPublic {
			continue
		}
		switch ev.Type {
		case domain.EvSpeech, domain.EvDM, domain.EvReveal, domain.EvClueRevealed:
		default:
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s：%s", recapSpeakerOf(ev), oneLine(ev.Text)))
	}
	if len(lines) < recapMinLines {
		return ""
	}

	// Spend the budget on the newest lines: the previous recap already covers
	// the beginning of the argument.
	budget := recapSource - runes(previous)
	kept := make([]string, 0, len(lines))
	used := 0
	for i := len(lines) - 1; i >= 0; i-- {
		cost := runes(lines[i])
		if used+cost > budget {
			break
		}
		kept = append(kept, lines[i])
		used += cost
	}
	if len(kept) == 0 {
		return ""
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	var u strings.Builder
	u.WriteString("【已有的前情提要】\n")
	if previous == "" {
		u.WriteString("（无）\n")
	} else {
		u.WriteString(previous)
		u.WriteString("\n")
	}
	if dropped := len(lines) - len(kept); dropped > 0 {
		fmt.Fprintf(&u, "\n（另有 %d 条更早的发言未列出）\n", dropped)
	}
	u.WriteString("\n【之后的公开发言与主持人记录】\n")
	u.WriteString(strings.Join(kept, "\n"))
	u.WriteString("\n")
	return u.String()
}

// applyRecap stores a finished retelling. The anchor only ever moves forward:
// a compression that started before the last one landed must not undo it.
func (a *Actor) applyRecap(text string, upto int, phaseID string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if runes(text) > recapOutput {
		text = clipText(text, recapOutput)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return
	}
	if a.g.Recap != nil && a.g.Recap.UptoSeq >= upto {
		return
	}
	a.g.Recap = &domain.Recap{Text: text, UptoSeq: upto, PhaseID: phaseID, At: time.Now()}
	a.persistLocked()
}

// recapSpeakerOf names an event's author the way a reader of the log would.
func recapSpeakerOf(ev domain.Event) string {
	if ev.Speaker != "" {
		return ev.Speaker
	}
	if ev.Type == domain.EvDM {
		return "主持人"
	}
	return "系统"
}
