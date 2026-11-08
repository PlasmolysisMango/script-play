package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"script-play/internal/domain"
)

// Every shipped script must survive a mechanical playthrough before it reaches
// the lobby: the test deals one seat per role, walks the official phase list,
// takes every card the gates allow, and insists the table still reaches a
// reveal with a readable truth. It is the cheap version of playing the book —
// no model, no timing, no UI.
func TestEveryScriptCanBePlayedThrough(t *testing.T) {
	paths, err := filepath.Glob("../../../scripts/*/script.json")
	if err != nil {
		t.Fatalf("glob scripts: %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no scripts on disk")
	}
	for _, path := range paths {
		path := path
		name := filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			smokeScript(t, path)
		})
	}
}

func smokeScript(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var sc domain.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if errs := sc.Validate(); len(errs) > 0 {
		t.Fatalf("校验失败 %d 处，第一条：%v", len(errs), errs[0])
	}
	if len(sc.Phases) == 0 || len(sc.Roles) == 0 {
		t.Fatal("剧本没有阶段或角色")
	}

	seats := make([]domain.Seat, 0, len(sc.Roles))
	for i, r := range sc.Roles {
		if strings.TrimSpace(r.Private.Script) == "" {
			t.Fatalf("角色 %s 没有角色本正文", r.Name)
		}
		seats = append(seats, domain.Seat{
			ID:          "seat" + string(rune('0'+i+1)),
			RoomID:      "room-smoke",
			RoleID:      r.ID,
			Kind:        domain.SeatHuman,
			DisplayName: r.Name,
		})
	}
	if len(seats) > 0 {
		seats[0].IsHost = true
	}

	g := domain.NewGame("g-smoke", "room-smoke", sc.Meta.Title, seats)
	g.Status = domain.RoomRunning
	a := NewActor(g, &sc, nil, nil, nil, ActorConfig{})

	var sawVote, sawReveal, sawScore bool
	for i := range sc.Phases {
		ph := &sc.Phases[i]
		g.PhaseIndex = i
		switch ph.Type {
		case domain.PhaseVote:
			sawVote = true
		case domain.PhaseScore:
			sawScore = true
		case domain.PhaseReveal:
			sawReveal = true
		case domain.PhaseSearch:
			takeEveryClue(t, a, ph)
		}
	}

	if sc.Mode == domain.ModeCardGame {
		// A card game has no vote round: the killer is named on the printed
		// score sheet instead, so score + reveal is the whole ending.
		if !sawScore || !sawReveal {
			t.Fatalf("卡牌本缺少积分卡或复盘阶段：%d 段", len(sc.Phases))
		}
	} else if !sawVote || !sawReveal {
		t.Fatalf("流程缺少投票或复盘阶段：%d 段", len(sc.Phases))
	}

	// A card nobody can ever draw is a dead page in the book. Fragments wait for
	// a keyword and personal items open with the role, so only the rest must be
	// reachable — through a named pool, a typed draw plan, a parent card that
	// names it (「可获得曲径通幽 N」), or a phase that hands it out privately.
	pooled := map[string]bool{}
	kinds := map[string]bool{}
	granted := map[string]bool{}
	private := map[string]bool{}
	for _, c := range sc.Clues {
		for _, g := range c.GrantsClue {
			granted[g] = true
		}
	}
	for _, ph := range sc.Phases {
		for _, id := range ph.CluePool {
			pooled[id] = true
		}
		for _, id := range ph.PrivateClues {
			private[id] = true
		}
		for _, d := range ph.Draws {
			if d.Kind == "" {
				pooled["*"] = true
				continue
			}
			kinds[d.Kind] = true
		}
	}
	for _, c := range sc.Clues {
		if c.OwnerRoleID != "" || len(c.Keywords) > 0 {
			continue
		}
		if !pooled[c.ID] && !pooled["*"] && !kinds[c.Kind] && !granted[c.ID] && !private[c.ID] {
			t.Errorf("线索 %s（%s）不在任何阶段的线索池里，线上永远搜不到", c.ID, c.Title)
		}
	}

	// Reveal is the only phase that hands the truth to the table.
	for i := range sc.Phases {
		if sc.Phases[i].Type == domain.PhaseReveal {
			g.PhaseIndex = i
			break
		}
	}
	v := VisibleFor(g, &sc, &g.Seats[0], VisibleOptions{IncludeTruth: true})
	// killerRoleId 为空是合法的：K18 的真凶是 NPC，真相由 summary 交代。
	if v.Truth == nil || (v.Truth.KillerRoleID == "" && strings.TrimSpace(v.Truth.Summary) == "") {
		t.Fatal("复盘阶段拿不到真相")
	}
	if v.Truth.KillerRoleID != "" && sc.Tokenize().RoleByID(v.Truth.KillerRoleID) == nil {
		t.Fatalf("真相里的凶手 %s 不是本本角色", v.Truth.KillerRoleID)
	}
}

// TestShippedCardGamePlaysThrough finishes a real card-game script with the
// real engine: every seat is an AI, the turn loop runs until the hands are
// empty, then the printed score sheet is filled in and tallied. It is the
// mechanical proof that a card script runs to the end online, not just that it
// parses.
func TestShippedCardGamePlaysThrough(t *testing.T) {
	paths, err := filepath.Glob("../../../scripts/*/script.json")
	if err != nil {
		t.Fatalf("glob scripts: %v", err)
	}
	played := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var sc domain.Script
		if err := json.Unmarshal(raw, &sc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if sc.Mode != domain.ModeCardGame {
			continue
		}
		played++
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			seats := make([]domain.Seat, 0, len(sc.Roles))
			for i, r := range sc.Roles {
				seats = append(seats, domain.Seat{
					ID: fmt.Sprintf("s%d", i+1), RoomID: "room-cards", RoleID: r.ID,
					Kind: domain.SeatAgent, DisplayName: fmt.Sprintf("%d 号位", i+1),
				})
			}
			g := domain.NewGame("g-cards", "room-cards", sc.Meta.Title, seats)
			g.Status = domain.RoomRunning
			g.PhaseIndex = 0
			for i, ph := range sc.Phases {
				if ph.Type == domain.PhasePlay {
					g.PhaseIndex = i
				}
			}
			a := NewActor(g, &sc, nil, nil, nil, ActorConfig{})
			a.mu.Lock()
			a.dealLocked()
			a.mu.Unlock()
			for turn := 0; turn < 20000; turn++ {
				a.mu.Lock()
				cur := a.currentSeatLocked(a.tableLocked())
				a.mu.Unlock()
				if cur == nil {
					break
				}
				a.agentPlay(*cur)
			}
			a.mu.Lock()
			cs := a.tableLocked()
			finished := len(cs.Finished)
			left := 0
			for _, hand := range cs.Hands {
				left += len(hand)
			}
			pending := cs.Pending
			a.mu.Unlock()
			if finished != len(seats) {
				t.Fatalf("只出完 %d/%d 家手牌，牌局卡住了", finished, len(seats))
			}
			// 牌要守恒：所有人都出完时，手上不能还留着牌，也不能剩半个技能。
			if left != 0 || pending != nil {
				t.Fatalf("牌局结束还剩 %d 张手牌、pending=%+v", left, pending)
			}
			for _, s := range seats {
				a.agentScore(s)
			}
			a.mu.Lock()
			a.tallyScoreLocked()
			result := append([]domain.CardScore{}, a.tableLocked().Result...)
			a.mu.Unlock()
			if len(result) != len(seats) {
				t.Fatalf("积分卡结算了 %d 张，应该 %d 张", len(result), len(seats))
			}
		})
	}
	if played == 0 {
		t.Skip("no card-game scripts on disk")
	}
}

// takeEveryClue presses the search button until nothing is left to take,
// re-dealing locked cards as soon as the item or parent they need appears.
func takeEveryClue(t *testing.T, a *Actor, ph *domain.Phase) {
	t.Helper()
	ph.PerPlayerPick = len(ph.CluePool) + 1
	a.searched = map[string]int{}
	if a.drawCost(ph) > 0 {
		// 有行动点的本子（K6）开局手里是有仙骨的；这里只验卡池里的每张卡都能
		// 被摸到，经济性由 points_test.go 单独验，所以直接垫一大笔。
		if a.g.Points == nil {
			a.g.Points = map[string]int{}
		}
		for _, s := range a.g.Seats {
			a.g.Points[s.ID] = 1 << 20
		}
	}
	for round := 0; round < len(ph.CluePool)+1; round++ {
		progress := false
		for i := range a.g.Seats {
			seat := a.g.Seats[i]
			for {
				avail := a.availableCluesLocked(seat, ph)
				if len(avail) == 0 {
					break
				}
				if err := a.claimClue(seat, avail[0].ID, ph); err != nil {
					break
				}
				progress = true
			}
			// 「可获得曲径通幽 N」的卡不在牌堆里，只能由抽到底卡的人点名取；
			// 它的物品往往在同轮别人抽完才凑齐，所以每轮都要重试一次。
			for {
				opts := a.grantedOptionsLocked(seat, ph)
				if len(opts) == 0 {
					break
				}
				if err := a.claimClue(seat, opts[0].ID, ph); err != nil {
					break
				}
				progress = true
			}
		}
		if !progress {
			return
		}
	}
}
