package domain

import (
	"fmt"
	"strings"
)

// ValidationError points at a concrete schema path (e.g. roles[2].private.script).
type ValidationError struct {
	Path string `json:"path"`
	Msg  string `json:"message"`
}

func (e ValidationError) Error() string { return e.Path + ": " + e.Msg }

// Validate checks the invariants the engine relies on. It is deliberately
// strict: a script that imports cleanly can never deadlock the state machine.
func (s *Script) Validate() []ValidationError {
	var errs []ValidationError
	add := func(path, format string, args ...any) {
		errs = append(errs, ValidationError{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	if s.SchemaVersion == 0 {
		s.SchemaVersion = 1
	}
	if s.SchemaVersion != 1 {
		add("schemaVersion", "只支持 schemaVersion 1，收到 %d", s.SchemaVersion)
	}
	if strings.TrimSpace(s.Meta.Title) == "" {
		add("meta.title", "剧本标题不能为空")
	}
	if s.Meta.PlayerCount.Min <= 0 {
		add("meta.playerCount.min", "最少玩家人数必须大于 0")
	}
	if s.Meta.PlayerCount.Max < s.Meta.PlayerCount.Min {
		add("meta.playerCount.max", "最多玩家人数不能小于最少人数")
	}
	if n := len(s.Roles); n == 0 {
		add("roles", "至少需要一个角色")
	} else if s.Meta.PlayerCount.Max > n {
		add("meta.playerCount.max", "角色数 %d 少于最多玩家人数 %d", n, s.Meta.PlayerCount.Max)
	}

	roleIDs := map[string]bool{}
	for i, r := range s.Roles {
		p := fmt.Sprintf("roles[%d]", i)
		if r.ID == "" {
			add(p+".id", "角色 id 不能为空")
		} else if roleIDs[r.ID] {
			add(p+".id", "角色 id 重复：%s", r.ID)
		}
		roleIDs[r.ID] = true
		if strings.TrimSpace(r.Name) == "" {
			add(p+".name", "角色名不能为空")
		}
		if strings.TrimSpace(r.Private.Script) == "" {
			add(p+".private.script", "角色私密剧本不能为空")
		}
		for j, sec := range r.Private.Secrets {
			if sec.ID == "" {
				add(fmt.Sprintf("%s.private.secrets[%d].id", p, j), "秘密 id 不能为空")
			}
			if sec.RevealCost < 0 || sec.RevealCost > 1 {
				add(fmt.Sprintf("%s.private.secrets[%d].revealCost", p, j), "revealCost 必须在 0~1 之间")
			}
		}
	}

	clueIDs := map[string]bool{}
	for i, c := range s.Clues {
		p := fmt.Sprintf("clues[%d]", i)
		if c.ID == "" {
			add(p+".id", "线索 id 不能为空")
		} else if clueIDs[c.ID] {
			add(p+".id", "线索 id 重复：%s", c.ID)
		}
		clueIDs[c.ID] = true
		if c.OwnerRoleID != "" && !roleIDs[c.OwnerRoleID] {
			add(p+".ownerRoleId", "未知角色：%s", c.OwnerRoleID)
		}
		for _, rid := range c.Links.Roles {
			if !roleIDs[rid] {
				add(p+".links.roles", "未知角色：%s", rid)
			}
		}
	}

	factIDs := map[string]bool{}
	for i, f := range s.Facts {
		if f.ID == "" {
			add(fmt.Sprintf("facts[%d].id", i), "事实 id 不能为空")
		}
		factIDs[f.ID] = true
	}
	for i := range s.Roles {
		for _, fid := range s.Roles[i].Private.KnownFactIDs {
			if !factIDs[fid] {
				add(fmt.Sprintf("roles[%d].private.knownFactIds", i), "未知事实：%s", fid)
			}
		}
	}

	if s.Truth.KillerRoleID != "" && !roleIDs[s.Truth.KillerRoleID] {
		add("truth.killerRoleId", "凶手角色不存在：%s", s.Truth.KillerRoleID)
	}

	if len(s.Phases) == 0 {
		add("phases", "至少需要一个阶段")
	}
	var hasReveal bool
	for i, ph := range s.Phases {
		p := fmt.Sprintf("phases[%d]", i)
		if ph.ID == "" {
			add(p+".id", "阶段 id 不能为空")
		}
		switch ph.Type {
		case PhaseReading, PhaseOpening, PhaseSearch, PhaseDiscussion, PhaseVote, PhaseReveal:
		case "":
			add(p+".type", "阶段类型不能为空")
		default:
			add(p+".type", "未知阶段类型：%s", ph.Type)
		}
		if ph.Type == PhaseReveal {
			hasReveal = true
		}
		if ph.Type == PhaseSearch {
			if ph.PerPlayerPick <= 0 {
				add(p+".perPlayerPick", "搜证阶段每人可搜次数必须大于 0")
			}
			for _, cid := range ph.CluePool {
				if !clueIDs[cid] {
					add(p+".cluePool", "未知线索：%s", cid)
				}
			}
			switch ph.SearchMode {
			case "", SearchPick, SearchRandom:
			default:
				add(p+".searchMode", "未知搜证方式：%s（可选 pick / random）", ph.SearchMode)
			}
		}
		if ph.Type == PhaseDiscussion && ph.SpeechPerPlayer <= 0 {
			add(p+".speechPerPlayer", "讨论阶段每人发言次数必须大于 0")
		}
		switch ph.Order {
		case "", OrderRoundRobin, OrderRandom, OrderHostAssigned:
		default:
			add(p+".order", "未知行动顺序策略：%s（可选 round_robin / random / host_assigned）", ph.Order)
		}
	}
	if !hasReveal {
		add("phases", "必须包含一个 reveal 阶段")
	}
	// Every script opens with the role books in hand; the rest of the flow
	// (discussion before search, rounds, votes) is up to the script JSON.
	if len(s.Phases) > 0 && s.Phases[0].Type != PhaseReading {
		add("phases[0].type", "第一个阶段必须是 reading（阅读剧本）")
	}
	return errs
}
