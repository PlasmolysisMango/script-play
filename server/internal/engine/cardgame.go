package engine

// The card table: K19《生日快乐》and any other script that ships a printed deck.
//
// The classic flow hands out clues and lets the table talk; this one hands out
// a hand of cards and runs a turn loop. The rules implemented here are the ones
// printed in the box:
//
//   - whoever is dealt 「此牌最先出」opens, then play goes clockwise;
//   - a card is legal when it matches the pile's colour or number; black skill
//     cards are always legal and may be aimed at any seat;
//   - 天黑请停止搜索 makes the next seat play a random card, 线索中断 names the
//     colour the next seat must follow;
//   - 调查身份 reads one word card of a seat, 陷入僵局 hands two discarded
//     cards to a seat, 交换线索 trades one card with a seat;
//   - a seat that cannot play passes; when the whole table passes, the last
//     player to have played turns over a random card;
//   - the game ends when every hand is empty, then the printed score sheet is
//     filled in and the killer wins if it places in the top three.

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"script-play/internal/domain"
)

// CardView is one card as a client may see it: the printed face only.
type CardView struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Color  string `json:"color,omitempty"`
	Number int    `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text,omitempty"`
}

// TableView is the public half of the card table plus this seat's legal moves.
type TableView struct {
	Top *CardView `json:"top,omitempty"`
	// Base is the card the turn must follow when Top is a black skill card.
	Base        *CardView           `json:"base,omitempty"`
	TurnSeatID  string              `json:"turnSeatId,omitempty"`
	Counts      map[string]int      `json:"counts,omitempty"`
	Finished    []string            `json:"finished,omitempty"`
	ForcedColor string              `json:"forcedColor,omitempty"`
	RandomNext  bool                `json:"randomNext,omitempty"`
	Pending     *domain.CardPending `json:"pending,omitempty"`
	Result      []domain.CardScore  `json:"result,omitempty"`
	Playable    []string            `json:"playable,omitempty"`
	Colors      []string            `json:"colors,omitempty"`
	Deck        int                 `json:"deck,omitempty"`
	// Discard is the whole pile. 陷入僵局 needs it: the table has to see what
	// it may hand out, and everybody can see the printed pile anyway.
	Discard []CardView `json:"discard,omitempty"`
}

func (a *Actor) cardGame() *domain.CardGame { return a.sc.CardGame }

// tableLocked returns the live card table, creating it on first use.
func (a *Actor) tableLocked() *domain.CardState {
	if a.g.Cards == nil {
		a.g.Cards = &domain.CardState{
			Hands:   map[string][]string{},
			Answers: map[string]domain.CardAnswer{},
		}
	}
	if a.g.Cards.Hands == nil {
		a.g.Cards.Hands = map[string][]string{}
	}
	if a.g.Cards.Answers == nil {
		a.g.Cards.Answers = map[string]domain.CardAnswer{}
	}
	return a.g.Cards
}

// dealLocked splits the deck the first time the play phase opens. It is
// idempotent: re-entering the phase never reshuffles a hand in progress.
func (a *Actor) dealLocked() {
	cg := a.cardGame()
	if cg == nil {
		return
	}
	cs := a.tableLocked()
	if len(cs.Order) > 0 {
		return
	}
	for _, s := range a.g.PlayerSeats() {
		cs.Order = append(cs.Order, s.ID)
	}
	n := len(cs.Order)
	if n == 0 {
		return
	}
	deck := append([]domain.Card(nil), cg.Cards...)
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	rnd.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	per := cg.Deal
	if per <= 0 || per*n > len(deck) {
		per = len(deck) / n
	}
	if per <= 0 {
		per = 1
	}
	for i, id := range cs.Order {
		hand := make([]string, 0, per)
		for j := 0; j < per; j++ {
			hand = append(hand, deck[i*per+j].ID)
		}
		cs.Hands[id] = hand
	}
	// The printed deck has one card that says 此牌最先出: its holder opens.
	cs.Turn = 0
	for i, id := range cs.Order {
		for _, cid := range cs.Hands[id] {
			if c := cg.CardByID(cid); c != nil && c.First {
				cs.Turn = i
			}
		}
	}
	first := a.g.SeatByID(cs.Order[cs.Turn])
	names := make([]string, 0, len(cs.Order))
	for _, id := range cs.Order {
		if s := a.g.SeatByID(id); s != nil {
			names = append(names, a.speakerLocked(*s))
		}
	}
	a.appendLocked(domain.Event{
		Type: domain.EvSystem,
		Text: fmt.Sprintf("发牌：每人 %d 张，出牌顺序 %s。%s 抽到「此牌最先出」，由他先出牌",
			per, strings.Join(names, " → "), a.speakerLocked(*first)),
	})
	// 未知词卡里印着「需要公开」的那几张，抽到就要亮出来；「可以不公开」
	// 的只能靠「调查身份」一点点看。
	for _, w := range cg.Words {
		if !w.Open {
			continue
		}
		for _, id := range cs.Order {
			s := a.g.SeatByID(id)
			if s == nil || s.RoleID != w.RoleID {
				continue
			}
			a.appendLocked(domain.Event{
				Type:    domain.EvSystem,
				SeatID:  s.ID,
				Speaker: a.speakerLocked(*s),
				Text:    fmt.Sprintf("摆在面前的未知词卡「%s」印着「需要公开」", w.Word),
			})
		}
	}
	a.persistLocked()
}

// currentSeatLocked returns the seat whose turn it is, skipping finished hands.
func (a *Actor) currentSeatLocked(cs *domain.CardState) *domain.Seat {
	if len(cs.Order) == 0 {
		return nil
	}
	done := map[string]bool{}
	for _, id := range cs.Finished {
		done[id] = true
	}
	for i := 0; i < len(cs.Order); i++ {
		idx := (cs.Turn + i) % len(cs.Order)
		id := cs.Order[idx]
		if done[id] || len(cs.Hands[id]) == 0 {
			continue
		}
		cs.Turn = idx
		return a.g.SeatByID(id)
	}
	return nil
}

func (a *Actor) advanceTurnLocked(cs *domain.CardState) {
	if len(cs.Order) == 0 {
		return
	}
	cs.Turn = (cs.Turn + 1) % len(cs.Order)
}

func (a *Actor) topCardLocked(cs *domain.CardState) *domain.Card {
	if len(cs.Discard) == 0 {
		return nil
	}
	return a.cardGame().CardByID(cs.Discard[len(cs.Discard)-1])
}

// matchBaseLocked is the card the next player actually has to follow. A black
// skill card has neither colour nor number, and the printed rule says the next
// player keeps following the card before it ("若上家打出技能牌，下家需遵循第 2
// 条规则") — without this the whole table could only answer skill with skill.
func (a *Actor) matchBaseLocked(cs *domain.CardState) *domain.Card {
	for i := len(cs.Discard) - 1; i >= 0; i-- {
		if c := a.cardGame().CardByID(cs.Discard[i]); c != nil && (c.Color != "" || c.Number > 0) {
			return c
		}
	}
	return nil
}

// playableLocked answers "may this card go on this pile right now".
func (a *Actor) playableLocked(c *domain.Card, top *domain.Card, forced string) bool {
	if c == nil {
		return false
	}
	// Skill cards are black: always legal, always aimed by hand.
	switch c.Kind {
	case domain.CardInspect, domain.CardStall, domain.CardSwap:
		return true
	}
	if top == nil {
		return true
	}
	if forced != "" {
		return c.Color != "" && c.Color == forced
	}
	if c.Color != "" && c.Color == top.Color {
		return true
	}
	return c.Number > 0 && c.Number == top.Number
}

func (a *Actor) handContainsLocked(seatID, cardID string) bool {
	cs := a.tableLocked()
	for _, id := range cs.Hands[seatID] {
		if id == cardID {
			return true
		}
	}
	return false
}

func removeOne(cards []string, id string) []string {
	out := cards[:0]
	for _, c := range cards {
		if c != id {
			out = append(out, c)
		}
	}
	return out
}

func (a *Actor) handViewsLocked(seatID string) []CardView {
	cs := a.tableLocked()
	out := make([]CardView, 0, len(cs.Hands[seatID]))
	for _, id := range cs.Hands[seatID] {
		if c := a.cardGame().CardByID(id); c != nil {
			out = append(out, viewOf(c))
		}
	}
	return out
}

func viewOf(c *domain.Card) CardView {
	return CardView{ID: c.ID, Kind: c.Kind, Color: c.Color, Number: c.Number, Title: c.Title, Text: c.Text}
}

// tableViewLocked builds what everyone may see, plus the playable set of one
// seat (the viewer) so the client never has to re-implement the rules.
func (a *Actor) tableViewLocked(seat *domain.Seat) *TableView {
	if a.g.Cards == nil || len(a.g.Cards.Order) == 0 {
		return nil
	}
	cs := a.g.Cards
	tv := &TableView{
		Counts:      map[string]int{},
		Finished:    append([]string{}, cs.Finished...),
		ForcedColor: cs.ForcedColor,
		RandomNext:  cs.RandomNext,
		Pending:     cs.Pending,
		Result:      cs.Result,
		Deck:        len(a.cardGame().Cards) - len(cs.Discard),
	}
	if top := a.topCardLocked(cs); top != nil {
		v := viewOf(top)
		tv.Top = &v
	}
	if base := a.matchBaseLocked(cs); base != nil {
		if tv.Top == nil || base.ID != tv.Top.ID {
			v := viewOf(base)
			tv.Base = &v
		}
	}
	for _, id := range cs.Discard {
		if c := a.cardGame().CardByID(id); c != nil {
			tv.Discard = append(tv.Discard, viewOf(c))
		}
	}
	if cur := a.currentSeatLocked(cs); cur != nil {
		tv.TurnSeatID = cur.ID
	}
	for _, id := range cs.Order {
		tv.Counts[id] = len(cs.Hands[id])
	}
	seen := map[string]bool{}
	for _, c := range a.cardGame().Cards {
		if c.Color != "" && !seen[c.Color] {
			seen[c.Color] = true
			tv.Colors = append(tv.Colors, c.Color)
		}
	}
	if seat != nil && seat.Kind != domain.SeatSpectator {
		top := a.matchBaseLocked(cs)
		for _, id := range cs.Hands[seat.ID] {
			if c := a.cardGame().CardByID(id); a.playableLocked(c, top, cs.ForcedColor) {
				tv.Playable = append(tv.Playable, id)
			}
		}
	}
	return tv
}

// wordsOfLocked is what 调查身份 reads out loud: the still-covered word cards in
// front of a seat. A card that already printed "需要公开" was announced at the
// deal, so it is no longer a secret and the skill must not pretend otherwise.
func (a *Actor) wordsOfLocked(roleID string) []string {
	var out []string
	for _, w := range a.cardGame().Words {
		if w.RoleID == roleID && !w.Open {
			out = append(out, w.Word)
		}
	}
	return out
}

func (a *Actor) roleNameLocked(roleID string) string {
	if r := a.sc.Tokenize().RoleByID(roleID); r != nil {
		return r.Name
	}
	return roleID
}

// ---- the play phase ----

func (a *Actor) runPlay(ctx context.Context, gen int) {
	a.mu.Lock()
	a.dealLocked()
	a.mu.Unlock()

	for {
		if !a.alive(gen) || ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		cs := a.tableLocked()
		seat := a.currentSeatLocked(cs)
		if seat == nil {
			a.mu.Unlock()
			a.completePhase(ctx, gen)
			return
		}
		a.floor = &Floor{SeatID: seat.ID, ExpiresAt: time.Now().Add(a.cfg.floor())}
		a.mu.Unlock()

		if seat.Kind == domain.SeatAgent {
			a.agentPlay(*seat)
			continue
		}
		a.awaitPlay(ctx, gen, *seat)
	}
}

// awaitPlay parks on one seat's card action. A skill that needs a second step
// keeps the same seat on the floor until both halves are done, which is why the
// function loops instead of returning after the first card.
func (a *Actor) awaitPlay(ctx context.Context, gen int, seat domain.Seat) {
	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	ch := make(chan ClientMessage, 4)
	a.waiters[seat.ID] = ch
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     seat.ID,
		Visibility: SeatVisibility(seat.ID),
		Text:       a.playPromptLocked(seat),
	})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if cur, ok := a.waiters[seat.ID]; ok && cur == ch {
			delete(a.waiters, seat.ID)
		}
		if a.floor != nil && a.floor.SeatID == seat.ID {
			a.floor = nil
		}
		a.mu.Unlock()
	}()

	timer, expired := a.cfg.deadline()
	if timer != nil {
		defer timer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-expired:
			a.autoPlay(seat, "出牌超时，自动处理")
			return
		case msg := <-ch:
			if msg.Type == "host.skip" {
				return
			}
			if msg.Type == "speak.text" {
				if text := strings.TrimSpace(msg.Text); text != "" {
					a.appendSpeech(seat, text)
				}
				continue
			}
			ended, err := a.handleCardCommand(seat, msg)
			if err != nil {
				a.notify(seat, err.Error())
				if timer != nil {
					timer.Reset(a.cfg.floor())
				}
				continue
			}
			if !ended {
				if timer != nil {
					timer.Reset(a.cfg.floor())
				}
				continue
			}
			return
		}
	}
}

func (a *Actor) playPromptLocked(seat domain.Seat) string {
	cs := a.tableLocked()
	top := a.matchBaseLocked(cs)
	head := "轮到你出牌"
	if top != nil {
		head = fmt.Sprintf("轮到你出牌，台面是「%s」", top.Title)
		if cs.ForcedColor != "" {
			head = fmt.Sprintf("轮到你出牌，台面是「%s」，必须出 %s 色", top.Title, cs.ForcedColor)
		}
	}
	if cs.RandomNext {
		return "轮到你出牌：上家打出「天黑请停止搜索」，你只能随机打出一张牌"
	}
	if len(cs.Hands[seat.ID]) == 0 {
		return head + "（你已出完）"
	}
	return head
}

// handleCardCommand applies one card-table command. It reports whether the turn
// is over: a two-step skill returns false so the seat stays on the floor.
func (a *Actor) handleCardCommand(seat domain.Seat, msg ClientMessage) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.matPhase() == nil {
		return false, ErrNotAllowed{Reason: "对局已结束"}
	}
	cs := a.tableLocked()
	cur := a.currentSeatLocked(cs)
	if cur == nil {
		return true, nil
	}
	if cs.Pending != nil && cs.Pending.SeatID == seat.ID {
		return a.applyPendingLocked(seat, msg)
	}
	if cur.ID != seat.ID {
		return false, ErrNotAllowed{Reason: "现在不是你的回合"}
	}
	switch msg.Type {
	case "card.play":
		return a.applyPlayLocked(seat, msg)
	case "card.random":
		// 随机出牌 only exists for 天黑请停止搜索. Letting a client ask for it
		// any time would bypass the colour/number rule and the forced colour.
		if !cs.RandomNext {
			return false, ErrNotAllowed{Reason: "这一轮不用随机出牌，请选一张能出的牌"}
		}
		return a.playRandomLocked(seat)
	case "card.pass":
		return a.passLocked(seat)
	default:
		return false, ErrNotAllowed{Reason: "当前不是这张牌的操作"}
	}
}

func (a *Actor) matPhase() *domain.Phase {
	ph := a.m.Current(a.g)
	if ph == nil {
		return nil
	}
	return ph
}

func (a *Actor) applyPlayLocked(seat domain.Seat, msg ClientMessage) (bool, error) {
	cs := a.tableLocked()
	cg := a.cardGame()
	card := cg.CardByID(msg.CardID)
	if card == nil {
		return false, ErrNotAllowed{Reason: "没有这张牌"}
	}
	if !a.handContainsLocked(seat.ID, card.ID) {
		return false, ErrNotAllowed{Reason: "这张牌不在你手上"}
	}
	if cs.RandomNext {
		return false, ErrNotAllowed{Reason: "你只能随机打出一张牌"}
	}
	top := a.matchBaseLocked(cs)
	// 天黑请停止搜索 / 全场无牌可出 let the card go on the pile regardless of
	// colour and number: that is the whole point of the effect.
	if msg.Type != "card.random" && !a.playableLocked(card, top, cs.ForcedColor) {
		if cs.ForcedColor != "" {
			return false, ErrNotAllowed{Reason: fmt.Sprintf("上家指定了 %s 色，这张牌出不了", cs.ForcedColor)}
		}
		return false, ErrNotAllowed{Reason: "只能出与台面同色或同数字的牌"}
	}
	var target *domain.Seat
	switch card.Kind {
	case domain.CardInspect, domain.CardStall, domain.CardSwap:
		if msg.To == "" {
			// 只剩自己时没有人可指定：牌照样出去、效果作废，否则最后一人手上
			// 只剩技能牌就永远出不完。
			if a.otherSeatLocked(seat.ID) == nil {
				break
			}
			return false, ErrNotAllowed{Reason: "这张技能牌要指定一位玩家"}
		}
		target = a.g.SeatByID(msg.To)
		if target == nil || target.Kind == domain.SeatSpectator {
			return false, ErrNotAllowed{Reason: "找不到这位玩家"}
		}
		if target.ID == seat.ID {
			return false, ErrNotAllowed{Reason: "这张牌要指定别人"}
		}
		// 已出完的人不会再摸到牌，把牌塞给他就不再可能打出来了。
		if a.finishedLocked(cs, target.ID) {
			return false, ErrNotAllowed{Reason: "这位玩家已经出完牌了"}
		}
	case domain.CardRecolor:
		if !a.knownColorLocked(msg.Color) {
			return false, ErrNotAllowed{Reason: "请指定一个颜色"}
		}
	}

	// The card leaves the hand and lands on the pile.
	cs.Hands[seat.ID] = removeOne(cs.Hands[seat.ID], card.ID)
	cs.Discard = append(cs.Discard, card.ID)
	cs.Last = seat.ID
	cs.ForcedColor, cs.RandomNext, cs.Passes = "", false, 0
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Text:    fmt.Sprintf("打出「%s」%s", card.Title, card.Text),
	})

	switch card.Kind {
	case domain.CardStop:
		cs.RandomNext = true
		a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "下一位玩家要随意打出一张牌"})
	case domain.CardRecolor:
		cs.ForcedColor = msg.Color
		a.appendLocked(domain.Event{Type: domain.EvSystem, Text: fmt.Sprintf("下一位玩家必须出 %s 色的牌", msg.Color)})
	case domain.CardInspect:
		if target == nil {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "没有可以指定的玩家，这张技能没有效果"})
			break
		}
		words := a.wordsOfLocked(target.RoleID)
		if len(words) == 0 {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "对方的未知词已经全部公开了，这张技能没有新消息"})
			break
		}
		a.appendLocked(domain.Event{
			Type:       domain.EvSystem,
			SeatID:     seat.ID,
			Visibility: SeatVisibility(seat.ID),
			Text:       fmt.Sprintf("你查看了 %s 的未知词：%s", a.speakerLocked(*target), strings.Join(words, "、")),
		})
	case domain.CardStall:
		if target == nil {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "没有可以指定的玩家，这张技能没有效果"})
			break
		}
		// A stall with fewer than two clue cards on the pile has nothing to hand
		// out; the card is still spent, the effect just fizzles — otherwise the
		// table would wait forever for a second step that cannot be taken.
		if a.stallableLocked() < 2 {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "牌堆里没有两张非功能牌，这张技能没有效果"})
			break
		}
		cs.Pending = &domain.CardPending{SeatID: seat.ID, Kind: domain.CardStall, Target: target.ID}
		a.notifyLocked(seat, "从已出牌堆里挑两张非功能牌交给 "+a.speakerLocked(*target))
		a.persistLocked()
		return false, nil
	case domain.CardSwap:
		if target == nil {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "没有可以指定的玩家，这张技能没有效果"})
			break
		}
		// 交换线索 needs a card to give back. When the skill itself was the last
		// card in hand there is nothing left to hand over; taking one anyway
		// would open a pending step that can never be closed.
		if len(cs.Hands[seat.ID]) == 0 {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "手上已经没有别的牌可还，这次交换作废"})
			break
		}
		taken := ""
		if len(cs.Hands[target.ID]) > 0 {
			idx := rand.Intn(len(cs.Hands[target.ID]))
			taken = cs.Hands[target.ID][idx]
			cs.Hands[target.ID] = append(append([]string{}, cs.Hands[target.ID][:idx]...), cs.Hands[target.ID][idx+1:]...)
			cs.Hands[seat.ID] = append(cs.Hands[seat.ID], taken)
		}
		if taken == "" {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "对方手上没有牌，这张技能没有效果"})
			break
		}
		cs.Pending = &domain.CardPending{SeatID: seat.ID, Kind: domain.CardSwap, Target: target.ID, Gained: taken}
		a.notifyLocked(seat, fmt.Sprintf("你从 %s 手里抽到了「%s」，现在选一张自己的牌给他",
			a.speakerLocked(*target), a.cardTitleLocked(taken)))
		a.persistLocked()
		return false, nil
	}

	if a.handEmptyLocked(seat) {
		return true, nil
	}
	a.advanceTurnLocked(cs)
	a.persistLocked()
	return true, nil
}

func (a *Actor) notifyLocked(seat domain.Seat, text string) {
	a.appendLocked(domain.Event{Type: domain.EvSystem, SeatID: seat.ID, Visibility: SeatVisibility(seat.ID), Text: text})
}

func (a *Actor) cardTitleLocked(id string) string {
	if c := a.cardGame().CardByID(id); c != nil {
		return c.Title
	}
	return id
}

func (a *Actor) knownColorLocked(color string) bool {
	if color == "" {
		return false
	}
	for _, c := range a.cardGame().Cards {
		if c.Color == color {
			return true
		}
	}
	return false
}

func (a *Actor) finishedLocked(cs *domain.CardState, seatID string) bool {
	for _, id := range cs.Finished {
		if id == seatID {
			return true
		}
	}
	return false
}

// handEmptyLocked records a finished hand and closes the phase once the whole
// table is out of cards.
func (a *Actor) handEmptyLocked(seat domain.Seat) bool {
	cs := a.tableLocked()
	if len(cs.Hands[seat.ID]) > 0 {
		return false
	}
	if a.finishedLocked(cs, seat.ID) {
		return false
	}
	cs.Finished = append(cs.Finished, seat.ID)
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Text:    fmt.Sprintf("出完了所有手牌，第 %d 位", len(cs.Finished)),
	})
	if len(cs.Finished) >= len(cs.Order) {
		a.persistLocked()
		return true
	}
	a.advanceTurnLocked(cs)
	a.persistLocked()
	return true
}

// passLocked is legal only when nothing in hand can go on the pile. When every
// remaining seat passes in a row, the printed rule hands the turn back to the
// last player, who turns over a random card.
func (a *Actor) passLocked(seat domain.Seat) (bool, error) {
	cs := a.tableLocked()
	top := a.matchBaseLocked(cs)
	for _, id := range cs.Hands[seat.ID] {
		if a.playableLocked(a.cardGame().CardByID(id), top, cs.ForcedColor) {
			return false, ErrNotAllowed{Reason: "你手上还有能出的牌"}
		}
	}
	cs.Passes++
	// 停止搜索 / 线索中断 only bind the next player. Once he has passed they
	// must not jump over him onto the seat after that.
	cs.ForcedColor, cs.RandomNext = "", false
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Text:    "无牌可出，过牌",
	})
	active := 0
	for _, id := range cs.Order {
		if len(cs.Hands[id]) > 0 {
			active++
		}
	}
	if active > 0 && cs.Passes >= active {
		cs.Passes = 0
		// Whoever played last plays again, at random: that is the printed rule
		// ("若所有人都无牌可出，则由最后一位出牌人随意打出一张牌").
		if last := a.g.SeatByID(cs.Last); last != nil && len(cs.Hands[last.ID]) > 0 {
			a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "全场无牌可出，由最后一位出牌人随机打出一张"})
			return a.playRandomLocked(*last)
		}
		// He is already out; the table has exactly one active hand left, which
		// still owes a card. Random play also drops a 线索中断 colour that nobody
		// can follow — without this the last player would pass forever.
		a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "只剩你一个人还有牌，随机打出一张"})
		return a.playRandomLocked(seat)
	}
	a.advanceTurnLocked(cs)
	a.persistLocked()
	return true, nil
}

// playRandomLocked turns over one random card for a seat. It is the effect of
// 天黑请停止搜索 and the escape hatch of a dead table.
func (a *Actor) playRandomLocked(seat domain.Seat) (bool, error) {
	cs := a.tableLocked()
	hand := cs.Hands[seat.ID]
	if len(hand) == 0 {
		a.advanceTurnLocked(cs)
		a.persistLocked()
		return true, nil
	}
	pick := hand[rand.Intn(len(hand))]
	if a.otherSeatLocked(seat.ID) == nil {
		// 只剩自己：技能牌没有可指定的对象，改从能出的牌里挑。
		var ok []string
		for _, id := range hand {
			if c := a.cardGame().CardByID(id); c != nil && c.Kind != domain.CardInspect && c.Kind != domain.CardStall && c.Kind != domain.CardSwap {
				ok = append(ok, id)
			}
		}
		if len(ok) > 0 {
			pick = ok[rand.Intn(len(ok))]
		}
	}
	cs.RandomNext = false
	msg := ClientMessage{Type: "card.random", CardID: pick}
	if c := a.cardGame().CardByID(pick); c != nil {
		switch c.Kind {
		case domain.CardInspect, domain.CardStall, domain.CardSwap:
			if t := a.otherSeatLocked(seat.ID); t != nil {
				msg.To = t.ID
			}
			if msg.To == "" {
				return false, ErrNotAllowed{Reason: "没有可以指定的玩家"}
			}
		case domain.CardRecolor:
			for _, cc := range a.cardGame().Cards {
				if cc.Color != "" {
					msg.Color = cc.Color
					break
				}
			}
		}
	}
	return a.applyPlayLocked(seat, msg)
}

// applyPendingLocked finishes a two-step skill: the two discarded cards of
// 陷入僵局, or the return card of 交换线索.
func (a *Actor) applyPendingLocked(seat domain.Seat, msg ClientMessage) (bool, error) {
	cs := a.tableLocked()
	p := cs.Pending
	switch p.Kind {
	case domain.CardStall:
		if msg.Type != "card.stall" || len(msg.CardIDs) != 2 {
			return false, ErrNotAllowed{Reason: "请从出牌堆里挑两张牌"}
		}
		if msg.CardIDs[0] == msg.CardIDs[1] {
			return false, ErrNotAllowed{Reason: "要挑两张不同的牌"}
		}
		for _, id := range msg.CardIDs {
			if !a.inDiscardLocked(id) {
				return false, ErrNotAllowed{Reason: "这两张牌不在出牌堆里"}
			}
			// 只拿线索牌。技能牌也算「非功能牌」的话，被拿走的技能牌会被下一张
			// 陷入僵局再拿回来，牌永远出不完。
			c := a.cardGame().CardByID(id)
			if c == nil || c.Kind != domain.CardClue {
				return false, ErrNotAllowed{Reason: "只能挑已经打出的线索牌"}
			}
		}
		for _, id := range msg.CardIDs {
			cs.Discard = removeOne(cs.Discard, id)
			cs.Hands[p.Target] = append(cs.Hands[p.Target], id)
		}
		a.appendLocked(domain.Event{
			Type:    domain.EvSystem,
			SeatID:  seat.ID,
			RoleID:  seat.RoleID,
			Speaker: a.speakerLocked(seat),
			Text: fmt.Sprintf("把「%s」「%s」交给了 %s", a.cardTitleLocked(msg.CardIDs[0]),
				a.cardTitleLocked(msg.CardIDs[1]), a.seatNameLocked(p.Target)),
		})
	case domain.CardSwap:
		if msg.Type != "card.give" || msg.CardID == "" {
			return false, ErrNotAllowed{Reason: "请选一张自己的牌交还"}
		}
		if !a.handContainsLocked(seat.ID, msg.CardID) {
			return false, ErrNotAllowed{Reason: "这张牌不在你手上"}
		}
		if msg.CardID == p.Gained {
			// 抽到的那张牌不能原样退回去 —— 除非手上真的只剩它，那就等价于
			// 这次交换没发生，总比推进不下去要好。
			others := 0
			for _, id := range cs.Hands[seat.ID] {
				if id != p.Gained {
					others++
				}
			}
			if others > 0 {
				return false, ErrNotAllowed{Reason: "刚换来的牌不能立刻还回去"}
			}
		}
		cs.Hands[seat.ID] = removeOne(cs.Hands[seat.ID], msg.CardID)
		cs.Hands[p.Target] = append(cs.Hands[p.Target], msg.CardID)
		a.appendLocked(domain.Event{
			Type:    domain.EvSystem,
			SeatID:  seat.ID,
			RoleID:  seat.RoleID,
			Speaker: a.speakerLocked(seat),
			Text:    fmt.Sprintf("交换线索：把「%s」交给了 %s", a.cardTitleLocked(msg.CardID), a.seatNameLocked(p.Target)),
		})
	default:
		return false, ErrNotAllowed{Reason: "没有待完成的操作"}
	}
	cs.Pending = nil
	if a.handEmptyLocked(seat) {
		return true, nil
	}
	a.advanceTurnLocked(cs)
	a.persistLocked()
	return true, nil
}

func (a *Actor) inDiscardLocked(id string) bool {
	for _, c := range a.tableLocked().Discard {
		if c == id {
			return true
		}
	}
	return false
}

// stallableLocked counts the cards 陷入僵局 may take: the clue cards already on
// the pile. Function and skill cards stay where they are — recycling a stall
// would refill hands forever and the game would never end.
func (a *Actor) stallableLocked() int {
	n := 0
	for _, id := range a.tableLocked().Discard {
		if c := a.cardGame().CardByID(id); c != nil && c.Kind == domain.CardClue {
			n++
		}
	}
	return n
}

// otherSeatLocked is the fallback target of a skill card: another player who can
// still take cards. A finished seat is skipped — cards handed to him would never
// come back out, and the table would never reach "everyone is out".
func (a *Actor) otherSeatLocked(seatID string) *domain.Seat {
	cs := a.tableLocked()
	for _, id := range cs.Order {
		if id == seatID || a.finishedLocked(cs, id) {
			continue
		}
		if s := a.g.SeatByID(id); s != nil && s.Kind != domain.SeatSpectator {
			return s
		}
	}
	return nil
}

func (a *Actor) seatNameLocked(id string) string {
	if s := a.g.SeatByID(id); s != nil {
		return a.speakerLocked(*s)
	}
	return id
}

// agentPlay gives AI seats a legal move without a model call: the rules are
// mechanical, and a model would only add latency (or cheat by accident).
func (a *Actor) agentPlay(seat domain.Seat) {
	a.mu.Lock()
	cs := a.tableLocked()
	base := a.matchBaseLocked(cs)
	var choice *domain.Card
	for _, id := range cs.Hands[seat.ID] {
		c := a.cardGame().CardByID(id)
		if !a.playableLocked(c, base, cs.ForcedColor) {
			continue
		}
		choice = c
		if c.Kind == domain.CardClue {
			break
		}
	}
	if choice == nil {
		a.mu.Unlock()
		a.autoPlay(seat, "AI 无牌可出，过牌")
		return
	}
	msg := a.messageForCardLocked(seat, choice)
	a.mu.Unlock()
	ended, err := a.handleCardCommand(seat, msg)
	if err != nil {
		a.autoPlay(seat, "AI 出牌失败，自动处理")
		return
	}
	if !ended {
		a.agentFinishPending(seat)
	}
}

// messageForCardLocked fills in the target or colour a card needs. Callers hold
// the lock; the AI and the timeout path must aim exactly like a human would.
func (a *Actor) messageForCardLocked(seat domain.Seat, card *domain.Card) ClientMessage {
	msg := ClientMessage{Type: "card.play", CardID: card.ID}
	switch card.Kind {
	case domain.CardInspect, domain.CardStall, domain.CardSwap:
		if t := a.otherSeatLocked(seat.ID); t != nil {
			msg.To = t.ID
		}
	case domain.CardRecolor:
		for _, c := range a.cardGame().Cards {
			if c.Color != "" {
				msg.Color = c.Color
				break
			}
		}
	}
	return msg
}

// agentFinishPending closes the second half of an AI skill card. When there is
// nothing legal to pick it abandons the pending step instead of calling back into
// autoPlay — those two used to bounce off each other until the stack blew.
func (a *Actor) agentFinishPending(seat domain.Seat) {
	a.mu.Lock()
	cs := a.tableLocked()
	p := cs.Pending
	if p == nil || p.SeatID != seat.ID {
		a.mu.Unlock()
		return
	}
	var msg ClientMessage
	if p.Kind == domain.CardStall {
		var picks []string
		for i := len(cs.Discard) - 1; i >= 0 && len(picks) < 2; i-- {
			if c := a.cardGame().CardByID(cs.Discard[i]); c != nil && c.Kind == domain.CardClue {
				picks = append(picks, cs.Discard[i])
			}
		}
		if len(picks) == 2 {
			msg = ClientMessage{Type: "card.stall", CardIDs: picks}
		}
	} else {
		for _, id := range cs.Hands[seat.ID] {
			if id != p.Gained {
				msg = ClientMessage{Type: "card.give", CardID: id}
				break
			}
		}
		if msg.CardID == "" && len(cs.Hands[seat.ID]) > 0 {
			// 只剩刚换来的那一张：原样退回，等价于这次交换没发生。
			msg = ClientMessage{Type: "card.give", CardID: cs.Hands[seat.ID][0]}
		}
	}
	a.mu.Unlock()
	if msg.Type == "" {
		a.abandonPending(seat)
		return
	}
	if _, err := a.handleCardCommand(seat, msg); err != nil {
		a.abandonPending(seat)
	}
}

// abandonPending drops a half-done skill so the table keeps moving.
func (a *Actor) abandonPending(seat domain.Seat) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cs := a.tableLocked()
	if cs.Pending == nil || cs.Pending.SeatID != seat.ID {
		return
	}
	cs.Pending = nil
	a.appendLocked(domain.Event{Type: domain.EvSystem, Text: "这一步没有可以选的牌，操作作废"})
	if !a.handEmptyLocked(seat) {
		a.advanceTurnLocked(cs)
	}
	a.persistLocked()
}

// autoPlay is the timeout path: random when the rules force it, otherwise play a
// legal card, otherwise pass.
func (a *Actor) autoPlay(seat domain.Seat, why string) {
	a.mu.Lock()
	cs := a.tableLocked()
	if cs.Pending != nil && cs.Pending.SeatID == seat.ID {
		a.mu.Unlock()
		a.agentFinishPending(seat)
		return
	}
	random := cs.RandomNext
	var choice *domain.Card
	if !random {
		base := a.matchBaseLocked(cs)
		for _, id := range cs.Hands[seat.ID] {
			if c := a.cardGame().CardByID(id); a.playableLocked(c, base, cs.ForcedColor) {
				choice = c
				break
			}
		}
	}
	var msg ClientMessage
	if random {
		msg = ClientMessage{Type: "card.random"}
	} else if choice != nil {
		msg = a.messageForCardLocked(seat, choice)
	}
	a.mu.Unlock()

	if msg.Type != "" {
		ended, err := a.handleCardCommand(seat, msg)
		if err == nil {
			// A random skill card still owes its second half (挑两张 / 回赠一张);
			// close it here so a timed-out turn cannot wedge the table.
			if !ended {
				a.agentFinishPending(seat)
			}
			a.notify(seat, why)
			return
		}
	}
	if _, err := a.handleCardCommand(seat, ClientMessage{Type: "card.pass"}); err != nil {
		// Last resort: the hand says a card is playable but the command was
		// refused, so drop one of them explicitly. Never a blind random command —
		// that would need the stopped-search flag and fail again.
		a.mu.Lock()
		var last ClientMessage
		if hand := a.tableLocked().Hands[seat.ID]; len(hand) > 0 {
			if c := a.cardGame().CardByID(hand[rand.Intn(len(hand))]); c != nil {
				last = a.messageForCardLocked(seat, c)
			}
		}
		a.mu.Unlock()
		if last.Type != "" {
			if ended, err := a.handleCardCommand(seat, last); err == nil && !ended {
				a.agentFinishPending(seat)
			}
		}
	}
	a.notify(seat, why)
}

// ---- the score phase ----

func (a *Actor) runScore(ctx context.Context, gen int) {
	for {
		if !a.alive(gen) || ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		cs := a.tableLocked()
		var next *domain.Seat
		for _, id := range cs.Order {
			if _, ok := cs.Answers[id]; ok {
				continue
			}
			if s := a.g.SeatByID(id); s != nil {
				next = s
				break
			}
		}
		if next == nil {
			a.tallyScoreLocked()
			a.mu.Unlock()
			a.completePhase(ctx, gen)
			return
		}
		a.mu.Unlock()
		if next.Kind == domain.SeatAgent {
			a.agentScore(*next)
			continue
		}
		a.awaitScore(ctx, gen, *next)
	}
}

func (a *Actor) awaitScore(ctx context.Context, gen int, seat domain.Seat) {
	a.mu.Lock()
	if gen != a.phaseGen {
		a.mu.Unlock()
		return
	}
	ch := make(chan ClientMessage, 4)
	a.waiters[seat.ID] = ch
	a.appendLocked(domain.Event{
		Type:       domain.EvSystem,
		SeatID:     seat.ID,
		Visibility: SeatVisibility(seat.ID),
		Text:       "请填写积分卡：凶手是谁、凶手是哪位玩家、在座每个人的身份",
	})
	a.persistLocked()
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if cur, ok := a.waiters[seat.ID]; ok && cur == ch {
			delete(a.waiters, seat.ID)
		}
		a.mu.Unlock()
	}()

	timer, expired := a.cfg.deadline()
	if timer != nil {
		defer timer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-expired:
			a.submitScore(seat, domain.CardAnswer{Guesses: map[string]string{}})
			return
		case msg := <-ch:
			if msg.Type == "host.skip" {
				return
			}
			if msg.Type == "speak.text" {
				if text := strings.TrimSpace(msg.Text); text != "" {
					a.appendSpeech(seat, text)
				}
				continue
			}
			if msg.Type != "score.submit" {
				a.notify(seat, "现在请填写积分卡")
				continue
			}
			answer := domain.CardAnswer{
				KillerRoleID: msg.RoleID,
				KillerSeatID: msg.To,
				Guesses:      msg.Guesses,
			}
			if answer.Guesses == nil {
				answer.Guesses = map[string]string{}
			}
			a.submitScore(seat, answer)
			return
		}
	}
}

func (a *Actor) agentScore(seat domain.Seat) {
	a.mu.Lock()
	cs := a.tableLocked()
	// An AI seat knows no more than a human: it guesses off the public role
	// list. Filling the sheet with the truth would hand it a free full score.
	roles := make([]string, 0, len(a.sc.Roles))
	for _, r := range a.sc.Roles {
		roles = append(roles, r.ID)
	}
	guesses := map[string]string{}
	pick := func() string { return roles[rand.Intn(len(roles))] }
	target := ""
	if len(roles) > 0 {
		for _, id := range cs.Order {
			guesses[id] = pick()
		}
		if len(cs.Order) > 0 {
			target = cs.Order[rand.Intn(len(cs.Order))]
		}
	}
	killerRole := ""
	if len(roles) > 0 {
		killerRole = pick()
	}
	a.mu.Unlock()
	a.submitScore(seat, domain.CardAnswer{
		KillerRoleID: killerRole,
		KillerSeatID: target,
		Guesses:      guesses,
	})
}

func (a *Actor) submitScore(seat domain.Seat, answer domain.CardAnswer) {
	a.mu.Lock()
	cs := a.tableLocked()
	cs.Answers[seat.ID] = answer
	a.appendLocked(domain.Event{
		Type:    domain.EvSystem,
		SeatID:  seat.ID,
		RoleID:  seat.RoleID,
		Speaker: a.speakerLocked(seat),
		Text:    "交回了积分卡",
	})
	a.persistLocked()
	a.mu.Unlock()
}

// tallyScoreLocked fills in the printed score card:
// rank 10/7/4/1/0, killer identity ±5/-3, killer player ±5/-3 (naming yourself
// as the killer scores nothing), and ±5/-3 for every seat's identity.
func (a *Actor) tallyScoreLocked() {
	cg := a.cardGame()
	cs := a.tableLocked()
	if cg == nil {
		return
	}
	rankOf := map[string]int{}
	for i, id := range cs.Finished {
		rankOf[id] = i + 1
	}
	killerSeat := a.g.SeatByRole(a.sc.Truth.KillerRoleID)
	var scores []domain.CardScore
	for _, id := range cs.Order {
		answer := cs.Answers[id]
		rank := rankOf[id]
		sc := domain.CardScore{SeatID: id, Rank: rank, RankPoints: cg.ScoreFor(rank)}
		if answer.KillerRoleID == a.sc.Truth.KillerRoleID {
			sc.KillerPoints = 5
		} else {
			sc.KillerPoints = -3
		}
		switch {
		case killerSeat == nil:
		case answer.KillerSeatID == killerSeat.ID && id == killerSeat.ID:
			// 认定自己是凶手且最终确定是自己：此条不计分
		case answer.KillerSeatID == killerSeat.ID:
			sc.PlayerPoints = 5
		default:
			sc.PlayerPoints = -3
		}
		for _, other := range cs.Order {
			guess := answer.Guesses[other]
			real := ""
			if s := a.g.SeatByID(other); s != nil {
				real = s.RoleID
			}
			if guess == real {
				sc.IdentityPoints += 5
			} else {
				sc.IdentityPoints -= 3
			}
		}
		sc.Total = sc.RankPoints + sc.KillerPoints + sc.PlayerPoints + sc.IdentityPoints
		scores = append(scores, sc)
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Total == scores[j].Total {
			return scores[i].Rank < scores[j].Rank
		}
		return scores[i].Total > scores[j].Total
	})
	cs.Result = scores

	var lines []string
	for i, sc := range scores {
		lines = append(lines, fmt.Sprintf("%d. %s %d 分（名次 %d 分 + 凶手 %d 分 + 指认 %d 分 + 身份 %d 分）",
			i+1, a.seatNameLocked(sc.SeatID), sc.Total, sc.RankPoints, sc.KillerPoints, sc.PlayerPoints, sc.IdentityPoints))
	}
	a.appendLocked(domain.Event{Type: domain.EvDM, Speaker: "主持人", Text: "积分结算：\n" + strings.Join(lines, "\n")})
	if killerSeat != nil && cg.KillerRank > 0 {
		place := 0
		for i, sc := range scores {
			if sc.SeatID == killerSeat.ID {
				place = i + 1
			}
		}
		msg := fmt.Sprintf("凶手是 %s（%s），最终排名第 %d 位，", a.seatNameLocked(killerSeat.ID),
			a.roleNameLocked(killerSeat.RoleID), place)
		if place <= cg.KillerRank {
			msg += fmt.Sprintf("进入前 %d 名，凶手获胜", cg.KillerRank)
		} else {
			msg += fmt.Sprintf("没进前 %d 名，好人获胜", cg.KillerRank)
		}
		a.appendLocked(domain.Event{Type: domain.EvDM, Speaker: "主持人", Text: msg})
	}
	a.persistLocked()
}
