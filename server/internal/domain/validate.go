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
	// hasPoints says whether this script runs on a point currency at all; the
	// favour phases (penalty / reward / settle) are meaningless without one.
	hasPoints := s.Meta.StartPoints > 0
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
		if r.Points < 0 {
			add(p+".points", "初始圣宠值不能为负")
		}
		if r.Points > 0 {
			hasPoints = true
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
	// clueByID backs the phase-level checks (privateClues) that must know who
	// owns a card, not just that it exists.
	clueByID := map[string]*Clue{}
	// clueKinds is what a phase's draws may name; a typo here would silently
	// make a round deal nothing at all.
	clueKinds := map[string]bool{}
	for i, c := range s.Clues {
		p := fmt.Sprintf("clues[%d]", i)
		if c.ID == "" {
			add(p+".id", "线索 id 不能为空")
		} else if clueIDs[c.ID] {
			add(p+".id", "线索 id 重复：%s", c.ID)
		}
		clueIDs[c.ID] = true
		clueByID[c.ID] = &s.Clues[i]
		if c.Kind != "" {
			clueKinds[c.Kind] = true
		}
		if c.OwnerRoleID != "" && !roleIDs[c.OwnerRoleID] {
			add(p+".ownerRoleId", "未知角色：%s", c.OwnerRoleID)
		}
		if c.SubjectRoleID != "" && !roleIDs[c.SubjectRoleID] {
			add(p+".subjectRoleId", "未知角色：%s", c.SubjectRoleID)
		}
		if c.SubjectRoleID != "" && c.SubjectRoleID == c.OwnerRoleID {
			add(p+".subjectRoleId", "搜身线索不能同时是本人私人物品（那样谁都搜不到）")
		}
		for j, item := range c.RequiresItems {
			if strings.TrimSpace(item) == "" {
				add(fmt.Sprintf("%s.requiresItems[%d]", p, j), "物品名不能为空")
			}
		}
		if len(c.Keywords) > 0 && c.OwnerRoleID == "" {
			add(p+".keywords", "关键词只能用来解锁私人物品（需同时填 ownerRoleId）")
		}
		for _, rid := range c.Links.Roles {
			if !roleIDs[rid] {
				add(p+".links.roles", "未知角色：%s", rid)
			}
		}
	}
	// UnlockedBy may name any clue, so it needs the full id set first.
	for i, c := range s.Clues {
		p := fmt.Sprintf("clues[%d]", i)
		for _, cid := range c.UnlockedBy {
			if !clueIDs[cid] {
				add(p+".unlockedBy", "未知线索：%s", cid)
			}
			if cid == c.ID {
				add(p+".unlockedBy", "线索不能以自己为前提，否则永远打不开")
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

	// Card-game scripts carry a deck instead of a clue pool. The checks below
	// are about the deck only: every id unique, every kind known, every skill
	// reachable by the engine, and enough cards to deal.
	switch s.Mode {
	case "", ModeCardGame:
	default:
		add("mode", "未知模式：%s（可选空 / cardgame）", s.Mode)
	}
	if s.Mode == ModeCardGame {
		cg := s.CardGame
		if cg == nil || len(cg.Cards) == 0 {
			add("cardGame.cards", "cardgame 剧本必须给出牌堆")
		} else {
			if cg.Deal < 0 {
				add("cardGame.deal", "每人发牌数不能为负")
			}
			if n := s.Meta.PlayerCount.Max; cg.Deal > 0 && cg.Deal*n > len(cg.Cards) {
				add("cardGame.deal", "牌不够发：%d 张 × %d 人 > 牌堆 %d 张", cg.Deal, n, len(cg.Cards))
			}
			cardIDs := map[string]bool{}
			for i, c := range cg.Cards {
				cp := fmt.Sprintf("cardGame.cards[%d]", i)
				if c.ID == "" {
					add(cp+".id", "牌 id 不能为空")
				} else if cardIDs[c.ID] {
					add(cp+".id", "牌 id 重复：%s", c.ID)
				}
				cardIDs[c.ID] = true
				switch c.Kind {
				case CardClue, CardStop, CardRecolor, CardInspect, CardStall, CardSwap:
				case "":
					add(cp+".kind", "牌的类别不能为空")
				default:
					add(cp+".kind", "未知牌类：%s", c.Kind)
				}
				if c.Kind == CardClue && (c.Color == "" || c.Number <= 0) {
					add(cp, "线索牌必须有颜色和数字")
				}
				if (c.Kind == CardStop || c.Kind == CardRecolor) && c.Color == "" {
					add(cp+".color", "功能牌必须带颜色")
				}
			}
			for i, w := range cg.Words {
				if !roleIDs[w.RoleID] {
					add(fmt.Sprintf("cardGame.words[%d].roleId", i), "未知角色：%s", w.RoleID)
				}
				if strings.TrimSpace(w.Word) == "" {
					add(fmt.Sprintf("cardGame.words[%d].word", i), "未知词不能为空")
				}
			}
		}
		for i, ph := range s.Phases {
			if ph.Type == PhaseSearch {
				add(fmt.Sprintf("phases[%d].type", i), "cardgame 剧本用 play 阶段出牌，不用 search")
			}
		}
	}

	if s.Meta.StartPoints < 0 {
		add("meta.startPoints", "起始行动点不能为负")
	}
	if s.Meta.StartPoints > 0 && len(s.Phases) > 0 {
		// A point pool with no draw cost would let a seat take every card for free;
		// the whole point of the currency is that searching competes with skills.
		charged := false
		for _, ph := range s.Phases {
			if ph.Type == PhaseSearch && ph.DrawCost > 0 {
				charged = true
				break
			}
		}
		if !charged {
			add("meta.startPoints", "有行动点的剧本至少要有一个搜证阶段写明 drawCost")
		}
	}
	for i, r := range s.Roles {
		if r.Skill == nil {
			continue
		}
		sp := fmt.Sprintf("roles[%d].skill", i)
		if r.Skill.ID == "" || r.Skill.Name == "" {
			add(sp, "技能 id 与名字不能为空")
		}
		switch r.Skill.Effect {
		case SkillStealDraws, SkillBurnPoints, SkillShowClues, SkillGrantPoints:
		default:
			add(sp+".effect", "未知技能效果：%s", r.Skill.Effect)
		}
		switch r.Skill.Target {
		case "", "other", "next":
		default:
			add(sp+".target", "技能目标只能是 other / next 或留空")
		}
		if r.Skill.TargetRoleID != "" && !roleIDs[r.Skill.TargetRoleID] {
			add(sp+".targetRoleId", "未知角色：%s", r.Skill.TargetRoleID)
		}
		if r.Skill.AllCost && r.Skill.ExactCost > 0 {
			add(sp, "allCost 与 exactCost 不能同时使用")
		}
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
		case PhaseReading, PhaseOpening, PhaseSearch, PhaseDiscussion, PhaseVote, PhaseReveal, PhasePlay, PhaseScore:
		case "":
			add(p+".type", "阶段类型不能为空")
		default:
			add(p+".type", "未知阶段类型：%s", ph.Type)
		}
		if (ph.Type == PhasePlay || ph.Type == PhaseScore) && s.Mode != ModeCardGame {
			add(p+".type", "%s 阶段只在 mode=cardgame 的剧本里可用", ph.Type)
		}
		if ph.Type == PhaseReveal {
			hasReveal = true
		}
		if ph.Type == PhaseSearch {
			if len(ph.Draws) == 0 && ph.PerPlayerPick <= 0 {
				add(p+".perPlayerPick", "搜证阶段每人可搜次数必须大于 0")
			}
			if len(ph.Draws) == 0 && len(ph.CluePool) == 0 {
				add(p+".cluePool", "搜证阶段要么给线索池，要么给抽取规则（draws）")
			}
			for _, cid := range ph.CluePool {
				if !clueIDs[cid] {
					add(p+".cluePool", "未知线索：%s", cid)
				}
			}
			for j, d := range ph.Draws {
				dp := fmt.Sprintf("%s.draws[%d]", p, j)
				if d.Count <= 0 {
					add(dp+".count", "每次抽取的张数必须大于 0")
				}
				if d.Kind != "" && !clueKinds[d.Kind] {
					add(dp+".kind", "没有这类线索卡：%s", d.Kind)
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
		case "", OrderRoundRobin, OrderRandom, OrderHostAssigned, OrderFixed, OrderFavor:
		default:
			add(p+".order", "未知行动顺序策略：%s（可选 round_robin / random / host_assigned / fixed / favor）", ph.Order)
		}
		if ph.Penalty < 0 || ph.Reward < 0 {
			add(p+".penalty", "投票后的加减分不能为负")
		}
		if ph.Penalty > 0 && ph.Reward > 0 {
			add(p+".reward", "同一个投票阶段不能既扣分又加分")
		}
		if (ph.Penalty > 0 || ph.Reward > 0 || ph.SettleFavor) && !hasPoints {
			add(p+".penalty", "要算恩宠值的阶段必须有行动点货币（meta.startPoints 或 role.points）")
		}
		if ph.SettleFavor && ph.ThroneRoleID == "" {
			add(p+".throneRoleId", "settleFavor 阶段要指明后位角色")
		}
		if ph.ThroneRoleID != "" && !roleIDs[ph.ThroneRoleID] {
			add(p+".throneRoleId", "未知角色：%s", ph.ThroneRoleID)
		}
		for i, n := range ph.PickByRank {
			if n <= 0 {
				add(fmt.Sprintf("%s.pickByRank[%d]", p, i), "按名次分配的搜证次数必须大于 0")
			}
		}
		if len(ph.PickByRank) > 0 && ph.Type != PhaseSearch {
			add(p+".pickByRank", "只有搜证阶段用得上按名次分配次数")
		}
		if ph.Order == OrderFixed && len(ph.OrderRoleIDs) == 0 {
			add(p+".orderRoleIds", "order=fixed 必须给出印在剧本上的顺序")
		}
		if len(ph.OrderRoleIDs) > 0 && ph.Order != OrderFixed {
			add(p+".orderRoleIds", "只有 order=fixed 才用得上 orderRoleIds")
		}
		for _, rid := range ph.OrderRoleIDs {
			if !roleIDs[rid] {
				add(p+".orderRoleIds", "未知角色：%s", rid)
			}
		}
		if ph.DrawCost < 0 {
			add(p+".drawCost", "每次抽卡消耗不能为负")
		}
		for _, rid := range ph.OnlyRoleIDs {
			// A typo here would silently turn the phase into an empty turn, which
			// looks like a stuck room at the table rather than a bad import.
			if !roleIDs[rid] {
				add(p+".onlyRoleIds", "未知角色：%s", rid)
			}
		}
		for _, cid := range ph.PrivateClues {
			c, ok := clueByID[cid]
			if !ok {
				add(p+".privateClues", "未知线索：%s", cid)
				continue
			}
			// 私发的前提是「这张卡属于谁」；没有主人就没人收得到，现场只会
			// 少一张卡而看不出错在哪。
			if c.OwnerRoleID == "" {
				add(p+".privateClues", "私发线索必须写 ownerRoleId：%s", cid)
			}
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
