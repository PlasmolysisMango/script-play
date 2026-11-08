package domain

// Card games
//
// Most scripts are a pile of clue cards read aloud. K19《生日快乐》is not: it is
// a 160-card table game where the table holds a hand, plays on a shared pile by
// colour or number, fires skill cards at each other, and only then fills in a
// score sheet. None of that fits "phase + clue", so the script carries an
// optional deck and the engine runs a `play` loop over it.
//
// Everything here is additive: a script without CardGame behaves exactly as
// before, and every new JSON field is omitempty.

// ModeCardGame switches the engine to the hand-of-cards flow. An empty Mode is
// the classic script flow.
const ModeCardGame = "cardgame"

// Phase types specific to a card game.
const (
	// PhasePlay runs the card table: turn by turn, one card per turn.
	PhasePlay = "play"
	// PhaseScore collects the printed score sheet and tallies it.
	PhaseScore = "score"
)

// Card kinds.
const (
	// CardClue is a numbered clue card in one of the five colours.
	CardClue = "clue"
	// CardStop ("天黑请停止搜索") makes the next seat play a random card.
	CardStop = "stop"
	// CardRecolor ("线索中断·换个地方搜索吧") names a colour the next seat
	// must follow.
	CardRecolor = "recolor"
	// CardInspect ("调查身份") lets the player read one word card of any seat.
	CardInspect = "inspect"
	// CardStall ("陷入僵局·再仔细查看线索吧") hands two discarded clue cards
	// to any seat.
	CardStall = "stall"
	// CardSwap ("交换线索") trades one card with any seat.
	CardSwap = "swap"
)

// CardGame is the printed deck of a card-game script.
type CardGame struct {
	// Deal is how many cards each seat is dealt. Zero means "split the deck
	// evenly", which is what the printed rules do (145 cards / 5 seats).
	Deal int `json:"deal,omitempty"`
	// Points is the score for finishing first, second, … (10/7/4/1/0).
	Points []int `json:"points,omitempty"`
	// KillerRank is how high the killer must place to win (3 = 前三名).
	KillerRank int `json:"killerRank,omitempty"`
	// Cards is the playable deck: clue, function and skill cards.
	Cards []Card `json:"cards"`
	// Words are the face-down word cards, two per role. Their owner may not
	// read them; only a CardInspect opens one.
	Words []CardWord `json:"words,omitempty"`
}

// Card is one printed card. Skill cards carry no colour and no number, which is
// what makes them playable at any time and against any seat.
type Card struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Color  string `json:"color,omitempty"`
	Number int    `json:"number,omitempty"`
	// Title is the printed headline ("线索：现场 3"), Text the card face.
	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"`
	// First marks the one card printed "此牌最先出": whoever is dealt it opens
	// the game.
	First bool `json:"first,omitempty"`
}

// CardWord is one of the two word cards laid face down in front of a role.
type CardWord struct {
	RoleID string `json:"roleId"`
	Word   string `json:"word"`
	// Open marks a word the printed card says must be public once drawn.
	Open bool `json:"open,omitempty"`
}

// CardState is the live table: hands, pile, turn pointer and score sheets.
type CardState struct {
	// Order is the clockwise seat order fixed when the cards were dealt.
	Order     []string            `json:"order"`
	Hands     map[string][]string `json:"hands"`
	Discard   []string            `json:"discard"`
	Finished  []string            `json:"finished"`
	Turn      int                 `json:"turn"`
	// Last is the seat that put a card on the pile most recently: the printed
	// rule hands the turn back to that player when the whole table passes.
	Last        string            `json:"last,omitempty"`
	ForcedColor string            `json:"forcedColor,omitempty"`
	RandomNext  bool              `json:"randomNext,omitempty"`
	Passes      int               `json:"passes,omitempty"`
	// Pending is a two-step skill waiting for its second half (挑两张弃牌 /
	// 回赠一张手牌).
	Pending *CardPending `json:"pending,omitempty"`
	Answers map[string]CardAnswer `json:"answers,omitempty"`
	Result  []CardScore           `json:"result,omitempty"`
}

// CardPending is the second half of a skill card.
type CardPending struct {
	SeatID string `json:"seatId"`
	// Kind is CardStall or CardSwap.
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Gained is the card a swap already took from the target; it must come
	// back as a hand card of the same size.
	Gained string `json:"gained,omitempty"`
}

// CardAnswer is one filled-in score sheet.
type CardAnswer struct {
	// KillerRoleID is "你认为凶手是 ___" (one of the five identities).
	KillerRoleID string `json:"killerRoleId"`
	// KillerSeatID is "你认为凶手是哪一位玩家".
	KillerSeatID string `json:"killerSeatId"`
	// Guesses maps seatID -> roleID: "你认为在座的玩家分别是谁".
	Guesses map[string]string `json:"guesses"`
}

// CardScore is one tallied score sheet.
type CardScore struct {
	SeatID         string `json:"seatId"`
	Rank           int    `json:"rank"`
	RankPoints     int    `json:"rankPoints"`
	KillerPoints   int    `json:"killerPoints"`
	PlayerPoints   int    `json:"playerPoints"`
	IdentityPoints int    `json:"identityPoints"`
	Total          int    `json:"total"`
}

// CardByID returns the card or nil.
func (g *CardGame) CardByID(id string) *Card {
	if g == nil {
		return nil
	}
	for i := range g.Cards {
		if g.Cards[i].ID == id {
			return &g.Cards[i]
		}
	}
	return nil
}

// ScoreFor is the printed points for finishing at 1-based place n.
func (g *CardGame) ScoreFor(n int) int {
	if g == nil || n <= 0 || n > len(g.Points) {
		return 0
	}
	return g.Points[n-1]
}
