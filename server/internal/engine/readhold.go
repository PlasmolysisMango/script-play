package engine

import (
	"fmt"
	"strings"

	"script-play/internal/domain"
)

// gateRoleScript removes pages the table has not reached yet. The role book is
// a single markdown string, so holds are inline directives; a locked hold is
// replaced by a short "not unlocked" note that names the phase without leaking
// any of the pages behind it.
func gateRoleScript(script string, sc *domain.Script, phaseIndex int) string {
	if !strings.Contains(script, "read-hold") {
		return script
	}
	names := make(map[string]string, len(sc.Phases))
	unlocked := make(map[string]bool, len(sc.Phases))
	for i, ph := range sc.Phases {
		names[ph.ID] = ph.Name
		unlocked[ph.ID] = phaseIndex >= i
	}

	var b strings.Builder
	locked := false
	for _, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(raw)
		if id, ok := domain.ReadHoldPhaseID(line); ok {
			locked = !unlocked[id]
			if locked {
				name := names[id]
				if name == "" {
					name = "后续阶段"
				}
				if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n\n") {
					b.WriteString("\n")
				}
				fmt.Fprintf(&b, "> **未解锁 · %s**\n>\n> 到达该阶段后可继续阅读。\n", name)
			}
			continue
		}
		if domain.IsReadHoldEnd(line) {
			locked = false
			continue
		}
		if locked {
			continue
		}
		b.WriteString(raw)
		b.WriteByte('\n')
	}
	return b.String()
}
