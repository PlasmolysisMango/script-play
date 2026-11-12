// Package domain holds the data model shared by the engine, storage and API.
//
// The script schema mirrors docs/script.schema.json. Everything an AI seat may
// never observe (Truth, other seats' Private blocks, unrevealed clues) lives in
// this package but is only reachable through engine.VisibleFor.
package domain

import (
	"strings"
	"time"
)

// Phase types supported by the state machine.
const (
	// PhaseReading is the opening beat every script starts with: each seat
	// reads its own role book, nothing is collected, the host moves on.
	PhaseReading    = "reading"
	PhaseOpening    = "opening"
	PhaseSearch     = "search"
	PhaseDiscussion = "discussion"
	PhaseVote       = "vote"
	PhaseReveal     = "reveal"
)

// Turn order policies. Any phase that hands the table one turn at a time uses
// one of these; the vote phase is simultaneous and ignores it.
const (
	// OrderRoundRobin walks the seats in join order, host first.
	OrderRoundRobin = "round_robin"
	// OrderRandom shuffles the seats once when the phase starts and records the
	// resulting order in the log, so a replay still reads in the played order.
	OrderRandom = "random"
	// OrderHostAssigned leaves the order to the host, who lays out the whole
	// round on one screen before anybody acts.
	OrderHostAssigned = "host_assigned"
	// OrderFixed follows the printed running order. K6 lists it in the rules
	// (涌江 → 北照君 → 流兮 → 失忆少年 → 千秋), so Phase.OrderRoleIDs carries it and
	// nobody has to retype the sequence at the table.
	OrderFixed = "fixed"
	// OrderFavor walks the table by the live point balance, richest first
	// (K12「按照当前各位娘娘的恩宠值排序，从高到低搜证」). Ties keep the
	// seating order so a replay reads the same way.
	OrderFavor = "favor"
)

// Skill effects the engine can resolve on its own. Anything else belongs in the
// printed text and is settled at the table.
const (
	// SkillStealDraws moves the target's remaining draws this round to the caster
	// (K6 北照君 「君子如玉」).
	SkillStealDraws = "steal_draws"
	// SkillBurnPoints zeroes the target's remaining points (千秋 「随心所欲」).
	SkillBurnPoints = "burn_points"
	// SkillShowClues makes the target show clues: Value cards, or all of them when
	// Value is 0, privately to the caster or to the whole table.
	SkillShowClues = "show_clues"
	// SkillGrantPoints hands the target points back (流兮 「情深不寿」).
	SkillGrantPoints = "grant_points"
)

// Search modes: how a seat obtains its clues in a search phase.
const (
	// SearchPick shows the seat the list of clues it may still take.
	SearchPick = "pick"
	// SearchRandom hides the list: the seat presses "draw" and the server deals
	// one clue uniformly among the ones still available to it.
	SearchRandom = "random"
)

// Script is one published script version.
type Script struct {
	SchemaVersion int     `json:"schemaVersion"`
	Meta          Meta    `json:"meta"`
	Truth         Truth   `json:"truth"`
	Phases        []Phase `json:"phases"`
	Roles         []Role  `json:"roles"`
	Clues         []Clue  `json:"clues"`
	Facts         []Fact  `json:"facts,omitempty"`
	// Mode picks the flow: empty is the classic role-book script, cardgame runs
	// the hand-of-cards table in CardGame.
	Mode     string    `json:"mode,omitempty"`
	CardGame *CardGame `json:"cardGame,omitempty"`
}

// Meta describes a script for the lobby listing.
type Meta struct {
	Title       string     `json:"title"`
	Author      string     `json:"author,omitempty"`
	Series      string     `json:"series,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Difficulty  string     `json:"difficulty,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	PlayerCount CountRange `json:"playerCount"`
	DurationMin int        `json:"durationMin,omitempty"`
	// StartPoints is the action currency every seat begins with. K6 hands each
	// 仙主 11 根仙骨, spent one per drawn card and a few per skill; a script
	// without such a currency leaves it zero.
	StartPoints int `json:"startPoints,omitempty"`
	// PointsLabel names that currency on screen ("仙骨"); empty reads 行动点.
	PointsLabel string `json:"pointsLabel,omitempty"`
	// HidePoints keeps every balance but the seat's own off the table until the
	// script settles them (K12「初始圣宠值第一阶段结束前请勿暴露」). Without
	// it the balances are face up, which is what K6 wants.
	HidePoints bool `json:"hidePoints,omitempty"`
}

// CountRange is the allowed number of player seats.
type CountRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Truth is the solution of the case. It must never reach an agent prompt.
type Truth struct {
	KillerRoleID string `json:"killerRoleId"`
	// KillerRoleIDs lists every guilty role for scripts that tell more than one
	// case in a single game (K27《女帝攻略》has three separate murders, so a
	// single killerRoleId forced the reveal to announce the wrong person). The
	// singular field above stays the common case and is always included.
	KillerRoleIDs []string `json:"killerRoleIds,omitempty"`
	// KillerKind says how the guilty party sits at the table: "seat" (a played
	// role), "npc" (named in the script but not playable, e.g. K18 阿飞) or
	// "multiple" (more than one guilty role). Empty means it is inferred from
	// the role ids.
	KillerKind string `json:"killerKind,omitempty"`
	// KillerName is the NPC's name when KillerKind is "npc". It never reaches a
	// seat prompt and is only used by the reveal and the host replay.
	KillerName string `json:"killerName,omitempty"`
	// Cases splits a multi-case script (K23 诚如恶魔低语 tells seven cases,
	// K28 女帝攻略 three) into per-case solutions. The flat killer fields above
	// stay the union, because the reveal and the replay still show one verdict;
	// Cases is what lets a vote round be checked against the case it asked.
	Cases    []TruthCase `json:"cases,omitempty"`
	Method   string      `json:"method,omitempty"`
	Motive   string      `json:"motive,omitempty"`
	Timeline []string    `json:"timeline,omitempty"`
	Summary  string      `json:"summary,omitempty"`
}

// TruthCase is one case inside a multi-case script.
type TruthCase struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// PhaseID names the vote phase that asks this case. Empty when the case is
	// concluded outside a vote (K28's first two cases are settled by DM script).
	PhaseID       string   `json:"phaseId,omitempty"`
	KillerRoleID  string   `json:"killerRoleId,omitempty"`
	KillerRoleIDs []string `json:"killerRoleIds,omitempty"`
	KillerKind    string   `json:"killerKind,omitempty"`
	KillerName    string   `json:"killerName,omitempty"`
	Method        string   `json:"method,omitempty"`
	Motive        string   `json:"motive,omitempty"`
}

// Killers returns the case's guilty roles, deduplicated.
func (c TruthCase) Killers() []string {
	return mergeKillerIDs(c.KillerRoleID, c.KillerRoleIDs)
}

// ResolvedKillerKind infers the kind when the case did not state one.
func (c TruthCase) ResolvedKillerKind() string {
	return resolvedKillerKind(c.KillerKind, c.KillerName, len(c.Killers()))
}

// Killers returns every guilty role, in declaration order and without repeats.
func (t Truth) Killers() []string {
	return mergeKillerIDs(t.KillerRoleID, t.KillerRoleIDs)
}

func mergeKillerIDs(primary string, ids []string) []string {
	out := make([]string, 0, len(ids)+1)
	add := func(id string) {
		if id == "" {
			return
		}
		for _, seen := range out {
			if seen == id {
				return
			}
		}
		out = append(out, id)
	}
	add(primary)
	for _, id := range ids {
		add(id)
	}
	return out
}

// Killer kinds. A script may leave KillerKind empty; ResolvedKillerKind then
// infers it from the role list and KillerName.
const (
	KillerKindSeat     = "seat"
	KillerKindNPC      = "npc"
	KillerKindMultiple = "multiple"
)

// ResolvedKillerKind returns the declared kind, or the obvious one when the
// script did not say: several role ids means multiple, one means a seat, and
// no role ids with a KillerName means an NPC.
func (t Truth) ResolvedKillerKind() string {
	return resolvedKillerKind(t.KillerKind, t.KillerName, len(t.Killers()))
}

func resolvedKillerKind(kind, name string, killers int) string {
	if kind != "" {
		return kind
	}
	switch {
	case killers > 1:
		return KillerKindMultiple
	case killers == 1:
		return KillerKindSeat
	case strings.TrimSpace(name) != "":
		return KillerKindNPC
	default:
		return ""
	}
}

// Phase is one step of the game flow.
type Phase struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	Name            string   `json:"name"`
	Round           int      `json:"round,omitempty"`
	DMScript        string   `json:"dmScript,omitempty"`
	CluePool        []string `json:"cluePool,omitempty"`
	PerPlayerPick   int      `json:"perPlayerPick,omitempty"`
	SpeechPerPlayer int      `json:"speechPerPlayer,omitempty"`
	// Order is how search/discussion hand out turns; SearchMode is how a search
	// turn hands out clues (pick or random). Vote ignores both.
	Order      string `json:"order,omitempty"`
	SearchMode string `json:"searchMode,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
	// OnlyRoleIDs narrows a phase to some of the roles. K5's 「查理的记忆」 is
	// dealt by the cat alone, and its three search rounds are the printed
	// "每位巫师抽 N 张" — the cat never searches. Without it the other seats
	// would be asked for a turn and told there is nothing to draw.
	OnlyRoleIDs []string `json:"onlyRoleIds,omitempty"`
	// RevealOnFind opens a search phase: every card is read aloud the moment
	// it is picked, so the table gets it without the seat pressing "公开".
	// Public-search scripts ("本轮调查到的线索均为公开线索") set it; scripts
	// built on "线索只可转述" leave it false, which is the default everywhere.
	RevealOnFind bool `json:"revealOnFind,omitempty"`
	// Draws splits one round into typed draws ("随身物品 1 张 + 轮船线索 3 张").
	// When it is set it replaces both PerPlayerPick and the pool restriction: the
	// seat draws the listed kinds in order, so the mix a printed rule promises
	// cannot be gamed by taking four of the good kind. Clue.Kind is what pairs
	// a card with a draw.
	Draws []DrawRule `json:"draws,omitempty"`
	// AudioURL points at a clip that belongs to the phase — an in-game recording,
	// a letter read aloud, a sound cue. It is served from the script's committed
	// assets just like a picture and shown in the phase header.
	AudioURL   string `json:"audioUrl,omitempty"`
	AudioLabel string `json:"audioLabel,omitempty"`
	// LeftoverRoleID is who takes every card still in CluePool when the search
	// phase closes ("剩下的归旁白"). Without it the leftover cards stay unfound,
	// which can bury a clue the table was supposed to hold.
	LeftoverRoleID string `json:"leftoverRoleId,omitempty"`
	// LeftoverReveal publishes those leftover cards to the table instead of
	// handing them to the role privately.
	LeftoverReveal bool `json:"leftoverReveal,omitempty"`
	// HostExtraPicks is how many extra draw rights the host may hand out in this
	// phase (the printed acting bonus: 主持人按演技奖励额外搜证). The host spends
	// one per grant with the search.grant command.
	HostExtraPicks int `json:"hostExtraPicks,omitempty"`
	// DrawCost is how many action points one draw spends (K6: 1 根仙骨). It only
	// means something in a script that sets Meta.StartPoints.
	DrawCost int `json:"drawCost,omitempty"`
	// OrderRoleIDs is the printed running order used with Order=OrderFixed. Seats
	// missing from it simply do not act in that phase.
	OrderRoleIDs []string `json:"orderRoleIds,omitempty"`
	// PrivateClues are 「专属线索」: cards handed straight to their owner when
	// the phase opens (K10 曲径通幽 06-10 print "××专属线索"). They never sit in
	// a pool, so a random draw can never hand one to the wrong player; the
	// clue's OwnerRoleID says who receives it.
	PrivateClues []string `json:"privateClues,omitempty"`
	// VotePrompt replaces the default "请选择你认为是凶手的角色" line with the
	// printed question (K12 指着三具尸体各问一遍“谁害死了他”).
	VotePrompt string `json:"votePrompt,omitempty"`
	// Penalty and Reward move points after a vote is counted: Penalty hands the
	// most-voted role that many points of damage (K12「一旦定罪，扣除恩宠值5点」),
	// Reward pays them instead (「今夜侍寝获得5点恩宠值」). Both need a point
	// currency; a phase may set either, never both meaningfully.
	Penalty int `json:"penalty,omitempty"`
	Reward  int `json:"reward,omitempty"`
	// PickByRank hands search quotas by place in the running order: the first
	// seat draws PickByRank[0], the next PickByRank[1], … (K12 初次搜证 9/8/7/6
	// 按恩宠值从高到低). It wins over PerPlayerPick, and pairs with
	// Order=OrderFavor.
	PickByRank []int `json:"pickByRank,omitempty"`
	// SettleFavor closes a favor race: it publishes every balance, works out who
	// is deposed and who is sent to the cold palace, and logs the verdict
	// (K12「水落石出」). It implies Meta.HidePoints until it fires.
	SettleFavor bool `json:"settleFavor,omitempty"`
	// ThroneRoleID is the seat that holds the throne in that race (K12 皇后).
	// A SettleFavor phase needs it to know who can be deposed.
	ThroneRoleID string `json:"throneRoleId,omitempty"`
}

// Skill is one printed special ability (K6: 每位仙主一个，消耗仙骨，全程一次).
// The engine resolves the four effects above; Text stays the printed wording so
// the seat can read exactly what the card says.
type Skill struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Text   string `json:"text"`
	Effect string `json:"effect"`
	// Cost is the points spent. ExactCost means "only when my points are exactly
	// this many" (千秋: 6), AllCost means "every point I have left" (涌江).
	Cost      int  `json:"cost,omitempty"`
	ExactCost int  `json:"exactCost,omitempty"`
	AllCost   bool `json:"allCost,omitempty"`
	// Target is how the caster picks: "other" a seat of its choice, "next" the
	// next seat in this phase's order (北照君), empty no choice at all.
	Target string `json:"target,omitempty"`
	// TargetRoleID pins the effect to one role (流兮 heals only 涌江).
	TargetRoleID string `json:"targetRoleId,omitempty"`
	// Value is the effect's parameter: how many cards show_clues reveals.
	Value int `json:"value,omitempty"`
	// Public is true when the shown cards go to the whole table (涌江) instead of
	// only to the caster (失忆少年).
	Public bool `json:"public,omitempty"`
}

// DrawRule is one line of a search round: how many cards of one kind a seat
// takes. An empty Kind means "any card still in the round".
type DrawRule struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// PickLimit is how many search turns one seat owes this phase. Draws win when
// present because they are the printed rule; PerPlayerPick is the older, flat
// way of saying the same thing.
func (p *Phase) PickLimit() int {
	if len(p.Draws) > 0 {
		total := 0
		for _, d := range p.Draws {
			total += d.Count
		}
		if total > 0 {
			return total
		}
	}
	return p.PerPlayerPick
}

// DrawPlan expands Draws into the exact sequence of kinds one seat works
// through, e.g. [随身物品 轮船线索 轮船线索 轮船线索]. The n-th pick's kind is
// therefore plan[n], which keeps the mix right without any extra bookkeeping.
func (p *Phase) DrawPlan() []string {
	if len(p.Draws) == 0 {
		return nil
	}
	var plan []string
	for _, d := range p.Draws {
		for i := 0; i < d.Count; i++ {
			plan = append(plan, d.Kind)
		}
	}
	return plan
}

// Role is a playable character.
type Role struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PersonaID string `json:"personaId,omitempty"`
	// Letter is the printed letter on the role card ("A".."E"). Card games
	// print it on the box lid next to the name, so it is public.
	Letter string `json:"letter,omitempty"`
	// Floor is the storey this role's room sits on ("二楼"). It is printed on
	// the map, so it is public, and search rounds use it to keep a seat out of
	// its own storey ("不能抽取自己所在楼层").
	Floor   string      `json:"floor,omitempty"`
	Public  RolePublic  `json:"public"`
	Private RolePrivate `json:"private"`
	// Points is the balance this role starts with when the script hands out a
	// different amount per role (K12 每宫的初始圣宠值). Meta.StartPoints is the
	// flat version; this one wins when it is set.
	Points int `json:"points,omitempty"`
	// Skill is the printed special ability, usable once in any search round.
	Skill *Skill `json:"skill,omitempty"`
}

// RolePublic is visible to every seat from the start.
type RolePublic struct {
	Intro      string `json:"intro,omitempty"`
	Appearance string `json:"appearance,omitempty"`
}

// RolePrivate is only ever visible to the seat that plays the role.
type RolePrivate struct {
	Script       string   `json:"script"`
	Secrets      []Secret `json:"secrets,omitempty"`
	Goals        []string `json:"goals,omitempty"`
	KnownFactIDs []string `json:"knownFactIds,omitempty"`
	// Keyword is the one printed word on the role card ("麻烦", "真相", …, on
	// the card front under 「你的关键词」). A card-game role reads it once at
	// the start and keeps it secret.
	Keyword string `json:"keyword,omitempty"`
}

// Secret is a hidden fact the role may choose to reveal (or not).
type Secret struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	RevealCost float64 `json:"revealCost"`
}

// Clue is a searchable piece of evidence.
type Clue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Number is the serial printed on the card (青萍之末 18、曲径通幽 02). The
	// back of a card shows it so a seat can name the exact one it was promised
	// ("抽到这张，可获取曲径通幽 2").
	Number   string `json:"number,omitempty"`
	Location string `json:"location,omitempty"`
	// Back is the text printed on the back of the physical clue card. The table
	// reads it while searching, so it names a place ("背后的培养仓") and never
	// summarises what is there — that is what Content is for, and Content only
	// becomes visible once the seat owns the clue.
	Back        string `json:"back,omitempty"`
	Content     string `json:"content"`
	OwnerRoleID string `json:"ownerRoleId,omitempty"`
	// SubjectRoleID marks a personal search ("搜身线索"): the clue is about
	// that role, so that role may never draw it. Unlike OwnerRoleID the card
	// stays in the common pool — everybody else can find it.
	SubjectRoleID string `json:"subjectRoleId,omitempty"`
	// Floor is the storey the card was printed for ("一楼"). A seat whose room
	// is on that storey may not draw it, which is the printed rule
	// "不能抽取自己所在楼层"; leave it empty on cards outside any storey.
	Floor string `json:"floor,omitempty"`
	// ExtraDraw is the printed "获得这张线索的巫师，可以抽取一张额外的线索":
	// taking the card hands the seat one more pick in the same round. K5's
	// 玛格丽特的房间 is 15 cards for 12 picks plus exactly three of these.
	ExtraDraw bool `json:"extraDraw,omitempty"`
	// GrantsClue names the "深入调查" cards this clue puts in the finder's hand
	// ("获得这张线索，可开启曲径通幽 N"). They are not part of the round's
	// pool: the seat that found the parent may claim one of them directly, in
	// this round or a later one, once its own RequiresItems are met. K10's
	// 曲径通幽 chain is the first book that needs the card to be *named*, not
	// just drawn from a bigger pile.
	GrantsClue []string `json:"grantsClue,omitempty"`
	// MustReveal is the single-card version of RevealOnFind: the card itself
	// says "本条线索必须公开", so finding it can never stay private no matter
	// which phase it sits in.
	MustReveal bool `json:"mustReveal,omitempty"`
	// GrantsItem is the object a seat takes away when it finds this clue
	// ("钳子"), and RequiresItems are the objects it must already hold before
	// it may search this one. Inventory is per seat and never gates speech.
	GrantsItem    string   `json:"grantsItem,omitempty"`
	RequiresItems []string `json:"requiresItems,omitempty"`
	// UnlockedBy lists clues that must be found or revealed first: the
	// "深入调查" chain, where a card only exists after its parent surfaced.
	UnlockedBy []string `json:"unlockedBy,omitempty"`
	// Keywords unlock a private clue (a memory fragment) the first time one
	// of them is spoken at the table or printed on a revealed clue. K6's
	// 「红烟深处和亡青叶」 needs both, which KeywordsAll asks for.
	Keywords    []string  `json:"keywords,omitempty"`
	KeywordsAll bool      `json:"keywordsAll,omitempty"`
	IsPublic    bool      `json:"isPublic"`
	ImageURL    string    `json:"imageUrl,omitempty"`
	Links       ClueLinks `json:"links,omitempty"`
	// Kind is the card family printed on the physical deck ("轮船线索",
	// "随身物品", "尸体线索"). Phase.Draws matches on it; a script that never
	// splits its deck can leave it empty.
	Kind string `json:"kind,omitempty"`
}

// ClueLinks connects a clue to roles, secrets and facts.
type ClueLinks struct {
	Roles   []string `json:"roles,omitempty"`
	Secrets []string `json:"secrets,omitempty"`
	Facts   []string `json:"facts,omitempty"`
}

// Fact is a neutral, citable statement used by prompts and leak detection.
type Fact struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Tokenize returns a lookup table for the ids defined by the script.
func (s *Script) Tokenize() ScriptIndex {
	idx := ScriptIndex{
		Roles:   map[string]*Role{},
		Clues:   map[string]*Clue{},
		Facts:   map[string]*Fact{},
		Secrets: map[string]*Secret{},
	}
	for i := range s.Roles {
		idx.Roles[s.Roles[i].ID] = &s.Roles[i]
		for j := range s.Roles[i].Private.Secrets {
			sec := &s.Roles[i].Private.Secrets[j]
			idx.Secrets[sec.ID] = sec
		}
	}
	for i := range s.Clues {
		idx.Clues[s.Clues[i].ID] = &s.Clues[i]
	}
	for i := range s.Facts {
		idx.Facts[s.Facts[i].ID] = &s.Facts[i]
	}
	return idx
}

// ScriptIndex is an id -> object lookup built per game.
type ScriptIndex struct {
	Roles   map[string]*Role
	Clues   map[string]*Clue
	Facts   map[string]*Fact
	Secrets map[string]*Secret
}

// RoleByID returns the role or nil.
func (i ScriptIndex) RoleByID(id string) *Role { return i.Roles[id] }

// ClueByID returns the clue or nil.
func (i ScriptIndex) ClueByID(id string) *Clue { return i.Clues[id] }

// PhaseByID returns the phase or nil.
func PhaseByID(s *Script, id string) *Phase {
	for i := range s.Phases {
		if s.Phases[i].ID == id {
			return &s.Phases[i]
		}
	}
	return nil
}

// ScriptRecord is a stored script together with its editorial metadata.
type ScriptRecord struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"ownerUserId"`
	Status      string    `json:"status"` // draft | published
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Source      string    `json:"source"` // json | sample
	Body        *Script   `json:"body"`
	// RawSource keeps the text a draft was created from, when there is one.
	RawSource string   `json:"rawSource,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}
