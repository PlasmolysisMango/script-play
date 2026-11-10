package engine

import (
	"strings"

	"script-play/internal/domain"
)

// LeakGuard inspects generated text for verbatim fragments of information the
// seat is not allowed to know. It is a cheap post-filter, not a semantic
// verifier: the real guarantee comes from VisibleFor.
type LeakGuard struct {
	forbidden []string
	// allowed is everything this seat can see. A phrase that already appears in
	// its own book, a found clue or the public transcript is knowledge, not a
	// leak: a truth summary often quotes the same words as a clue card, and
	// flagging those matches turned ordinary clue talk into leak incidents.
	allowed []string
}

const minLeakRun = 8 // runes; shorter overlaps are usually generic phrasing

// NewLeakGuard collects the texts this seat must never quote: the truth and
// every other role's secrets.
func NewLeakGuard(sc *domain.Script, seat *domain.Seat, visible ...string) *LeakGuard {
	g := &LeakGuard{}
	for _, s := range visible {
		if s = strings.TrimSpace(oneLine(s)); s != "" {
			g.allowed = append(g.allowed, s)
		}
	}
	add := func(s string) {
		s = strings.TrimSpace(oneLine(s))
		if len([]rune(s)) >= minLeakRun {
			g.forbidden = append(g.forbidden, s)
		}
	}
	add(sc.Truth.Method)
	add(sc.Truth.Motive)
	add(sc.Truth.Summary)
	for _, line := range sc.Truth.Timeline {
		add(line)
	}
	for _, r := range sc.Roles {
		if seat != nil && r.ID == seat.RoleID {
			continue
		}
		for _, s := range r.Private.Secrets {
			add(s.Text)
		}
	}
	return g
}

// Check returns the leaked fragment when the text quotes forbidden information.
func (g *LeakGuard) Check(text string) (string, bool) {
	if g == nil {
		return "", false
	}
	// Prompt echoing is always a leak.
	for _, marker := range []string{"【你的秘密", "【可见信息】", "【你的角色本】", "【公开发言记录】"} {
		if strings.Contains(text, marker) {
			return marker, true
		}
	}
	cand := []rune(oneLine(text))
	for _, f := range g.forbidden {
		fr := []rune(strings.TrimSpace(f))
		if len(fr) < minLeakRun {
			continue
		}
		run, frag := longestCommonRun(cand, fr)
		if run < minLeakRun {
			continue
		}
		if g.visible(frag) {
			continue
		}
		return string(frag), true
	}
	return "", false
}

// visible reports whether a matched phrase is already in this seat's knowledge.
func (g *LeakGuard) visible(frag []rune) bool {
	for _, s := range g.allowed {
		if run, _ := longestCommonRun(frag, []rune(s)); run >= minLeakRun {
			return true
		}
	}
	return false
}

// visibleCorpus flattens the seat's information set into the allow-list used by
// the leak guard.
func visibleCorpus(v Visible) []string {
	var out []string
	if v.OwnRole != nil {
		out = append(out, v.OwnRole.Private.Script)
		for _, s := range v.OwnRole.Private.Secrets {
			out = append(out, s.Text)
		}
		out = append(out, v.OwnRole.Private.Goals...)
	}
	for _, c := range v.OwnClues {
		out = append(out, c.Title, c.Content)
	}
	for _, c := range v.PublicClues {
		out = append(out, c.Title, c.Content)
	}
	for _, f := range v.OwnFacts {
		out = append(out, f.Text)
	}
	out = append(out, v.Items...)
	out = append(out, v.Phase.DMScript)
	out = append(out, v.Recap)
	for _, l := range v.Transcript {
		out = append(out, l.Text)
	}
	return out
}

// Sanitize strips forbidden fragments from a reply. It is used when a retry is
// not possible (timeout, provider error) so the game keeps going.
func (g *LeakGuard) Sanitize(text string, hit string) string {
	if hit == "" {
		return text
	}
	out := strings.ReplaceAll(text, hit, "……")
	out = strings.ReplaceAll(out, "【你的秘密", "")
	out = strings.ReplaceAll(out, "【可见信息】", "")
	out = strings.ReplaceAll(out, "【你的角色本】", "")
	out = strings.ReplaceAll(out, "【公开发言记录】", "")
	if strings.TrimSpace(out) == "" {
		out = "……这件事我不方便多说。"
	}
	return strings.TrimSpace(out)
}

func longestCommonRun(a, b []rune) (int, []rune) {
	if len(a) == 0 || len(b) == 0 {
		return 0, nil
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	best, end := 0, 0
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > best {
					best, end = cur[j], i
				}
			} else {
				cur[j] = 0
			}
		}
		prev, cur = cur, prev
		for j := range cur {
			cur[j] = 0
		}
	}
	if best == 0 {
		return 0, nil
	}
	return best, a[end-best : end]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
