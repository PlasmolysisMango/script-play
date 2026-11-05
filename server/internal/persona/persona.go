// Package persona turns numeric behaviour knobs into prompt language and into
// a decision rule for whether an agent reveals a secret.
package persona

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
)

// Persona is a configurable social style for an AI seat.
type Persona struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Style         string   `json:"style"`
	Candor        float64  `json:"candor"`        // 0 隐瞒 .. 1 坦白
	Aggression    float64  `json:"aggression"`    // 0 温和 .. 1 逼问
	Suspicion     float64  `json:"suspicion"`     // 0 轻信 .. 1 多疑
	Talkativeness float64  `json:"talkativeness"` // 0 寡言 .. 1 健谈
	Deceitfulness float64  `json:"deceitfulness"` // 0 诚实 .. 1 撒谎
	Emotionality  float64  `json:"emotionality"`  // 0 冷静 .. 1 情绪化
	Rules         []string `json:"rules,omitempty"`
}

var presets = []Persona{
	{
		ID: "candid", Name: "坦白型", Style: "直来直去，愿意主动提供信息",
		Candor: 0.85, Aggression: 0.45, Suspicion: 0.4, Talkativeness: 0.7, Deceitfulness: 0.1, Emotionality: 0.5,
		Rules: []string{"只要不被直接追问到隐私，就主动补充你知道的线索", "尽量不说谎，但可以隐瞒与自己有关的细节"},
	},
	{
		ID: "guarded", Name: "谨慎型", Style: "只回答被问到的问题，不主动扩散信息",
		Candor: 0.4, Aggression: 0.35, Suspicion: 0.65, Talkativeness: 0.45, Deceitfulness: 0.3, Emotionality: 0.35,
		Rules: []string{"回答简短，只覆盖问题本身", "被逼问时用反问转移话题", "不主动公布对自己不利的线索"},
	},
	{
		ID: "sly", Name: "狡黠型", Style: "擅长暗示、误导与半真半假",
		Candor: 0.25, Aggression: 0.5, Suspicion: 0.75, Talkativeness: 0.7, Deceitfulness: 0.75, Emotionality: 0.4,
		Rules: []string{"用部分真话包裹误导，不要说完整的假话", "把怀疑引向他人，但必须基于你真知道的事", "绝不承认自己藏了关键信息"},
	},
	{
		ID: "aggressive", Name: "激进型", Style: "主动质疑别人，抢话语权",
		Candor: 0.55, Aggression: 0.9, Suspicion: 0.8, Talkativeness: 0.85, Deceitfulness: 0.35, Emotionality: 0.7,
		Rules: []string{"每次发言都点名质疑至少一个玩家", "用时间线和线索细节逼问对方", "被打断也要把话说完"},
	},
	{
		ID: "taciturn", Name: "沉默型", Style: "极少发言，一句话说完",
		Candor: 0.3, Aggression: 0.2, Suspicion: 0.6, Talkativeness: 0.15, Deceitfulness: 0.2, Emotionality: 0.2,
		Rules: []string{"一次发言不超过两句话", "只在必要时补充一条信息", "不解释自己的推理过程"},
	},
	{
		ID: "dramatic", Name: "演剧型", Style: "情绪饱满，叙事性强",
		Candor: 0.6, Aggression: 0.6, Suspicion: 0.5, Talkativeness: 0.9, Deceitfulness: 0.45, Emotionality: 0.95,
		Rules: []string{"用第一人称描述当时的感受与场景", "允许夸张，但不得虚构自己不可能知道的事实", "主动串联别人说过的细节"},
	},
}

// Presets returns all built-in personas sorted by id.
func Presets() []Persona {
	out := make([]Persona, len(presets))
	copy(out, presets)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns a persona by id, falling back to the guarded preset.
func Get(id string) Persona {
	for _, p := range presets {
		if p.ID == id {
			return p
		}
	}
	return presets[1]
}

// RandomID returns a deterministic-by-index persona id.
func RandomID(seed int) string {
	if seed < 0 {
		seed = -seed
	}
	return presets[seed%len(presets)].ID
}

// Random rolls a fresh preset id (math/rand is auto-seeded).
func Random() string {
	return presets[rand.IntN(len(presets))].ID
}

// RandomSentinel is the id a client sends to ask the server to roll the dice.
const RandomSentinel = "random"

// Resolve turns an empty or "random" id into a concrete preset, so a seat never
// stores a placeholder it cannot describe back to the table.
func Resolve(id string) string {
	if id == "" || id == RandomSentinel {
		return Random()
	}
	return id
}

func band(v float64, low, mid, high string) string {
	switch {
	case v < 0.34:
		return low
	case v < 0.67:
		return mid
	default:
		return high
	}
}

// Describe renders the knobs as prompt-ready Chinese constraints.
func (p Persona) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "性格：%s（%s）\n", p.Name, p.Style)
	fmt.Fprintf(&b, "- 坦白度 %.2f：%s\n", p.Candor, band(p.Candor,
		"能不说就不说，被问到才挤出一句",
		"回答被问到的内容，不主动展开",
		"愿意主动交代与自己相关的事实"))
	fmt.Fprintf(&b, "- 攻击性 %.2f：%s\n", p.Aggression, band(p.Aggression,
		"避免正面冲突",
		"适当质疑，但点到为止",
		"直接点名施压，追问到底"))
	fmt.Fprintf(&b, "- 多疑度 %.2f：%s\n", p.Suspicion, band(p.Suspicion,
		"倾向相信别人的说法",
		"对关键说法保留判断",
		"反复校验每个人的时间线，从细节里找矛盾"))
	fmt.Fprintf(&b, "- 话量 %.2f：%s\n", p.Talkativeness, band(p.Talkativeness,
		"每次只说一到两句",
		"控制在三到四句",
		"会铺垫、展开、逐条列举"))
	fmt.Fprintf(&b, "- 欺骗倾向 %.2f：%s\n", p.Deceitfulness, band(p.Deceitfulness,
		"不说谎，最多隐瞒",
		"可以回避，但不编造事实",
		"允许撒谎，但要记得自己说过的版本"))
	fmt.Fprintf(&b, "- 情绪化 %.2f：%s\n", p.Emotionality, band(p.Emotionality,
		"语气平稳克制",
		"偶尔带情绪",
		"有明显情绪起伏，语气会跟着情绪走"))
	for _, r := range p.Rules {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	return strings.TrimRight(b.String(), "\n")
}

// RevealPressure is the normalised pressure an agent feels in a turn.
type RevealPressure struct {
	// Asked is 1 when another seat directly asked this agent about the topic.
	Asked bool
	// Accused is 1 when the agent is currently the prime suspect.
	Accused bool
	// RoundNormalised grows with discussion rounds (0..1).
	RoundNormalised float64
}

func (r RevealPressure) Score() float64 {
	s := 0.0
	if r.Asked {
		s += 0.45
	}
	if r.Accused {
		s += 0.3
	}
	s += math.Max(0, math.Min(1, r.RoundNormalised)) * 0.25
	return math.Min(1, s)
}

// ShouldReveal decides whether a secret of the given cost is spoken aloud.
// High candor lowers the bar, high deceitfulness raises it.
func (p Persona) ShouldReveal(secretCost float64, pressure RevealPressure) bool {
	effective := p.Candor*0.6 + pressure.Score()*0.55 - p.Deceitfulness*0.35
	return effective >= secretCost
}

// MaxSentences is the soft length limit used in prompts and in truncation.
func (p Persona) MaxSentences() int {
	return 1 + int(math.Round(p.Talkativeness*4))
}
