package domain

import (
	"regexp"
	"strings"
)

// A role book may hold back pages until the table reaches a phase:
//
//	<!-- read-hold:p3_search_personal -->
//	pages the player must not read yet
//	<!-- /read-hold -->
//
// Without an end marker a hold runs to the end of the book (the printed
// "请勿翻开下一页" until the next hold). The engine strips either form before
// the text reaches a client or an agent, so future pages cannot leak.
var (
	readHoldStart = regexp.MustCompile(`^<!--\s*read-hold:\s*([A-Za-z0-9_.-]+)\s*-->$`)
	readHoldEnd   = regexp.MustCompile(`^<!--\s*/read-hold\s*-->$`)
)

// ReadHoldPhaseID reports the phase named by a read-hold start line.
func ReadHoldPhaseID(line string) (string, bool) {
	m := readHoldStart.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// IsReadHoldEnd reports whether the line closes a gated section.
func IsReadHoldEnd(line string) bool {
	return readHoldEnd.MatchString(strings.TrimSpace(line))
}

// ReadHoldPhases lists the phases named by a role book's holds, in order.
func ReadHoldPhases(script string) []string {
	var out []string
	for _, line := range strings.Split(script, "\n") {
		if id, ok := ReadHoldPhaseID(line); ok {
			out = append(out, id)
		}
	}
	return out
}

// LooksLikeReadHold reports a line that was meant to be a directive but did
// not match the strict form. Import validation uses it to reject a typo that
// would otherwise show the gated pages instead of hiding them.
func LooksLikeReadHold(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "<!--") && strings.Contains(trimmed, "read-hold")
}
