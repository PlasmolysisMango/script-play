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
}

const minLeakRun = 8 // runes; shorter overlaps are usually generic phrasing

// NewLeakGuard collects the texts this seat must never quote: the truth and
// every other role's secrets.
func NewLeakGuard(sc *domain.Script, seat *domain.Seat) *LeakGuard {
	g := &LeakGuard{}
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
		if run := longestCommonRun(cand, fr); run >= minLeakRun {
			return string(fr[:min(run+2, len(fr))]), true
		}
	}
	return "", false
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

func longestCommonRun(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	best := 0
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > best {
					best = cur[j]
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
	return best
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
