/**
 * The script body as the studio edits it.
 *
 * These are the same fields docs/script.schema.json declares, but every one of
 * them is optional here: a draft is edited field by field, and a half-typed role
 * must not disappear because it does not validate yet. Only the server decides
 * what a valid script is.
 */

export type Secret = { id?: string; text?: string; revealCost?: number }

export type Role = {
  id?: string
  name?: string
  personaId?: string
  /** 角色卡上印的字母（A–E），卡牌本用它对齐积分卡。 */
  letter?: string
  /** 角色房间所在的楼层（一楼/二楼/三楼），搜证时不能抽自己楼层的卡。 */
  floor?: string
  public?: { intro?: string; appearance?: string }
  private?: {
    script?: string
    secrets?: Secret[]
    goals?: string[]
    knownFactIds?: string[]
    /** 角色卡正面印的「你的关键词」，只说一次，不能给旁人看。 */
    keyword?: string
  }
}

export type Clue = {
  id?: string
  title?: string
  location?: string
  back?: string
  content?: string
  ownerRoleId?: string
  /** 搜身线索：说的是谁，本人抽不到。 */
  subjectRoleId?: string
  /** 这张卡所属的楼层；与角色 floor 相同的座位抽不到。 */
  floor?: string
  /** 卡面「可以抽取一张额外的线索」：抽到的座位本轮多一次。 */
  extraDraw?: boolean
  /** 本线索必须公开（卡面写「本条线索必须公开」）。 */
  mustReveal?: boolean
  /** 搜到即获得的物品。 */
  grantsItem?: string
  /** 持有这些物品才能搜到。 */
  requiresItems?: string[]
  /** 必须先搜到的前置线索。 */
  unlockedBy?: string[]
  /** 关键词触发的私密线索（配合 ownerRoleId）。 */
  keywords?: string[]
  isPublic?: boolean
  imageUrl?: string
  /** 卡类（「随身物品」「轮船线索」），阶段按类抽卡时配对用。 */
  kind?: string
  links?: { roles?: string[]; secrets?: string[]; facts?: string[] }
}

/** 一段抽卡规则：从某一类线索里抽几张。 */
export type DrawRule = { kind: string; count: number }

export type Phase = {
  id?: string
  type?: string
  name?: string
  round?: number
  dmScript?: string
  cluePool?: string[]
  perPlayerPick?: number
  speechPerPlayer?: number
  searchMode?: string
  order?: string
  timeoutSec?: number
  /** 只由这些角色行动（如「查理的记忆」只有黑猫能抽）。 */
  onlyRoleIds?: string[]
  /** 本轮搜到的线索立即公开。 */
  revealOnFind?: boolean
  /** 分类抽卡：按顺序从这些卡类里各抽几张，取代 cluePool + perPlayerPick。 */
  draws?: DrawRule[]
}

export type Fact = { id?: string; text?: string }

export type Truth = {
  killerRoleId?: string
  method?: string
  motive?: string
  timeline?: string[]
  summary?: string
}

export type Meta = {
  title?: string
  author?: string
  /** 系列标签，如「K的游戏」「AI创作」；没有特定系列时留空。 */
  series?: string
  summary?: string
  difficulty?: string
  tags?: string[]
  playerCount?: { min?: number; max?: number }
  durationMin?: number
}

export type ScriptBody = {
  schemaVersion?: number
  meta?: Meta
  truth?: Truth
  phases?: Phase[]
  roles?: Role[]
  clues?: Clue[]
  facts?: Fact[]
  /** 空 = 角色本剧本；cardgame = 手牌 + 出牌 + 积分卡。 */
  mode?: string
  cardGame?: CardGame
}

/** 一张实体牌。技能牌没有颜色和数字，所以随时能出、可以点任何人。 */
export type Card = {
  id?: string
  kind?: string
  color?: string
  number?: number
  title?: string
  text?: string
  /** 印着「此牌最先出」的那一张：抽到的人先出牌。 */
  first?: boolean
}

/** 角色面前扣着的两张未知词卡。 */
export type CardWord = { roleId?: string; word?: string; open?: boolean }

export type CardGame = {
  /** 每人发几张；0 = 牌堆平均分。 */
  deal?: number
  /** 名次分：第一名、第二名…… */
  points?: number[]
  /** 凶手排进前几名算赢。 */
  killerRank?: number
  cards?: Card[]
  words?: CardWord[]
}

export const SCRIPT_MODES = [
  ['', '角色本（经典）'],
  ['cardgame', '卡牌本（手牌 + 积分卡）'],
] as const

export const PHASE_TYPES = [
  ['reading', '阅读剧本'],
  ['opening', '开场'],
  ['search', '搜证'],
  ['play', '出牌（卡牌本）'],
  ['score', '填积分卡（卡牌本）'],
  ['discussion', '讨论'],
  ['vote', '投票'],
  ['reveal', '揭晓'],
] as const

export const PHASE_ORDERS = [
  ['round_robin', '轮流（按入座）'],
  ['random', '随机'],
  ['host_assigned', '主持人指定'],
] as const

export const SEARCH_MODES = [
  ['pick', '自选（看标题挑）'],
  ['random', '随机抽取（抽到什么算什么）'],
] as const

export const DIFFICULTIES = [
  ['easy', '简单'],
  ['medium', '中等'],
  ['hard', '困难'],
] as const

/** One line of what the chosen order means at the table. */
export function orderHint(order?: string): string {
  switch (order) {
    case 'random':
      return '阶段开始时随机排一次，之后按这个顺序进行'
    case 'host_assigned':
      return '阶段开始由主持人一次性排好整轮顺序'
    default:
      return '按入座顺序轮流，主持人优先'
  }
}

/**
 * The skeleton a new draft starts from: metadata plus the phases the engine
 * expects. Reading comes first because every script opens with the role books in
 * hand; roles and clues stay empty for the author to fill in.
 */
export function blankScript(): ScriptBody {
  return {
    schemaVersion: 1,
    meta: { title: '未命名剧本', series: '', playerCount: { min: 4, max: 6 }, tags: [] },
    truth: { killerRoleId: '' },
    roles: [],
    clues: [],
    facts: [],
    phases: [
      { id: 'p1', type: 'reading', name: '阅读剧本' },
      { id: 'p2', type: 'discussion', name: '自我介绍与案情讨论', round: 1, speechPerPlayer: 1, order: 'round_robin' },
      { id: 'p3', type: 'search', name: '第一轮搜证', round: 1, perPlayerPick: 2, order: 'round_robin', searchMode: 'pick' },
      { id: 'p4', type: 'discussion', name: '第一轮公聊', round: 1, speechPerPlayer: 2, order: 'round_robin' },
      { id: 'p5', type: 'vote', name: '投票' },
      { id: 'p6', type: 'reveal', name: '复盘' },
    ],
  }
}

/** `role_1`, `clue_3`… never colliding with what the draft already uses. */
export function nextId(prefix: string, taken: (string | undefined)[]): string {
  let max = 0
  const withSep = new RegExp(`^${prefix}_(\\d+)$`)
  const bare = new RegExp(`^${prefix}(\\d+)$`)
  let bareStyle = false
  for (const id of taken) {
    if (!id) continue
    const sep = withSep.exec(id)
    if (sep) max = Math.max(max, Number(sep[1]))
    const plain = bare.exec(id)
    if (plain) {
      bareStyle = true
      max = Math.max(max, Number(plain[1]))
    }
  }
  // Phases are written `p1` in every published script; roles and clues carry the
  // underscore. Follow whatever the draft already uses so ids stay readable.
  return bareStyle ? `${prefix}${max + 1}` : `${prefix}_${max + 1}`
}

export function blankRole(roles: Role[]): Role {
  return {
    id: nextId('role', roles.map((r) => r.id)),
    name: '',
    public: { intro: '', appearance: '' },
    private: { script: '', secrets: [], goals: [], knownFactIds: [] },
  }
}

export function blankClue(clues: Clue[]): Clue {
  return {
    id: nextId('clue', clues.map((c) => c.id)),
    title: '',
    location: '',
    back: '',
    content: '',
    isPublic: false,
    links: { roles: [], secrets: [], facts: [] },
  }
}

export function blankPhase(phases: Phase[]): Phase {
  return {
    id: nextId('p', phases.map((p) => p.id)),
    type: 'discussion',
    name: '新阶段',
    round: 1,
    order: 'round_robin',
    speechPerPlayer: 1,
  }
}

export function blankFact(facts: Fact[]): Fact {
  return { id: nextId('fact', facts.map((f) => f.id)), text: '' }
}

export function blankSecret(secrets: Secret[]): Secret {
  return { id: nextId('secret', secrets.map((s) => s.id)), text: '', revealCost: 0.5 }
}

export function replaceAt<T>(list: T[], index: number, value: T): T[] {
  return list.map((item, i) => (i === index ? value : item))
}

export function removeAt<T>(list: T[], index: number): T[] {
  return list.filter((_, i) => i !== index)
}

export function moveAt<T>(list: T[], index: number, delta: number): T[] {
  const target = index + delta
  if (target < 0 || target >= list.length) return list
  const next = list.slice()
  next[index] = list[target]
  next[target] = list[index]
  return next
}

/** Comma / ideographic comma separated ids, the way authors paste them. */
export function splitIds(text: string): string[] {
  return text
    .split(/[，,、\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

/** Every secret id in the script, annotated with the role that owns it. */
export function secretIndex(roles: Role[]): { id: string; roleName: string; text: string }[] {
  const out: { id: string; roleName: string; text: string }[] = []
  for (const role of roles) {
    for (const secret of role.private?.secrets ?? []) {
      if (!secret.id) continue
      out.push({ id: secret.id, roleName: role.name || role.id || '', text: secret.text ?? '' })
    }
  }
  return out
}
