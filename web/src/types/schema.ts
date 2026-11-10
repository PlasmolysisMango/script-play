import { z } from 'zod'

/**
 * Wire types. They mirror docs/protocol.md exactly; the server is the authority
 * and every field the server refuses to send is absent here as well.
 */

export const userSchema = z.object({
  id: z.string(),
  username: z.string(),
  displayName: z.string(),
  /** "admin" unlocks the console and the script library. */
  role: z.enum(['user', 'admin']).optional().default('user'),
})
export type User = z.infer<typeof userSchema>

export const personaSchema = z.object({
  id: z.string(),
  name: z.string(),
  style: z.string(),
  candor: z.number(),
  aggression: z.number(),
  suspicion: z.number(),
  talkativeness: z.number(),
  deceitfulness: z.number(),
  emotionality: z.number(),
  rules: z.array(z.string()).nullish().transform((v) => v ?? []),
})
export type Persona = z.infer<typeof personaSchema>

export const phaseSummarySchema = z.object({
  id: z.string(),
  name: z.string(),
  type: z.string(),
  round: z.number().optional().default(0),
  status: z.enum(['pending', 'current', 'done']),
})
export type PhaseSummary = z.infer<typeof phaseSummarySchema>

export const seatSchema = z.object({
  id: z.string(),
  roleId: z.string().optional().default(''),
  roleName: z.string().optional().default(''),
  displayName: z.string(),
  kind: z.string(),
  personaId: z.string().optional().default(''),
  modelId: z.string().optional().default(''),
  isHost: z.boolean(),
  connected: z.boolean(),
  ready: z.boolean(),
  mine: z.boolean(),
})
export type Seat = z.infer<typeof seatSchema>

export const roleOptionSchema = z.object({
  id: z.string(),
  name: z.string(),
  intro: z.string().optional().default(''),
  personaId: z.string().optional().default(''),
  taken: z.boolean(),
})
export type RoleOption = z.infer<typeof roleOptionSchema>

export const roomSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  status: z.string(),
  gameId: z.string(),
  scriptId: z.string(),
  scriptTitle: z.string().optional().default(''),
  maxSeats: z.number(),
  allowSpectator: z.boolean(),
  /** 0 = server default per-turn speech limit. */
  speechSeconds: z.number().optional().default(0),
  autoSkip: z.boolean().optional().default(true),
  host: z.boolean(),
  seats: z.array(seatSchema),
  roles: z.array(roleOptionSchema),
  mySeatId: z.string().optional().default(''),
  personas: z.array(personaSchema).nullish().transform((v) => v ?? []),
})
export type Room = z.infer<typeof roomSchema>

export const publicSeatSchema = z.object({
  id: z.string(),
  roleId: z.string().optional().default(''),
  roleName: z.string().optional().default(''),
  displayName: z.string(),
  kind: z.string(),
  isHost: z.boolean().optional().default(false),
  connected: z.boolean().optional().default(true),
  ready: z.boolean().optional().default(false),
})
export type PublicSeat = z.infer<typeof publicSeatSchema>

export const clueSchema = z.object({
  id: z.string(),
  title: z.string(),
  location: z.string().optional().default(''),
  // 实体卡背面的字（地点＋编号）；搜证选项只下发它，不含 title/content。
  back: z.string().optional().default(''),
  content: z.string(),
  imageUrl: z.string().optional().default(''),
})
export type Clue = z.infer<typeof clueSchema>

export const factSchema = z.object({ id: z.string(), text: z.string() })
export type Fact = z.infer<typeof factSchema>

export const secretSchema = z.object({
  id: z.string(),
  text: z.string(),
  revealCost: z.number().optional().default(0.5),
})

export const ownRoleSchema = z.object({
  id: z.string(),
  name: z.string(),
  personaId: z.string().optional().default(''),
  public: z.object({ intro: z.string().optional().default(''), appearance: z.string().optional().default('') }),
  private: z.object({
    script: z.string(),
    secrets: z.array(secretSchema).nullish().transform((v) => v ?? []),
    goals: z.array(z.string()).nullish().transform((v) => v ?? []),
    knownFactIds: z.array(z.string()).nullish().transform((v) => v ?? []),
  }),
})
export type OwnRole = z.infer<typeof ownRoleSchema>

export const transcriptLineSchema = z.object({
  seq: z.number(),
  kind: z.string(),
  seatId: z.string().optional().default(''),
  roleId: z.string().optional().default(''),
  speaker: z.string().optional().default(''),
  text: z.string(),
  at: z.string().optional().default(''),
})
export type TranscriptLine = z.infer<typeof transcriptLineSchema>

export const truthSchema = z.object({
  killerRoleId: z.string().optional().default(''),
  /** 多案/多凶手剧本：揭晓时按这个列表逐个点名。 */
  killerRoleIds: z.array(z.string()).nullish().transform((v) => v ?? []),
  method: z.string().optional().default(''),
  motive: z.string().optional().default(''),
  timeline: z.array(z.string()).nullish().transform((v) => v ?? []),
  summary: z.string().optional().default(''),
})
export type Truth = z.infer<typeof truthSchema>

export const visibleSchema = z.object({
  gameId: z.string(),
  roomId: z.string(),
  phase: z.object({
    id: z.string().optional().default(''),
    type: z.string().optional().default(''),
    name: z.string().optional().default(''),
    round: z.number().optional().default(0),
    // Strategy the script set for this phase: the table shows them so nobody
    // has to guess why the order is random or why the list is hidden.
    order: z.string().optional().default(''),
    searchMode: z.string().optional().default(''),
    // Set by the script: this round is read aloud, so the find is public at
    // once and the seat never gets a lone "公开" button.
    revealOnFind: z.boolean().nullish().transform((v) => v ?? false),
    perPlayerPick: z.number().optional().default(0),
    speechPerPlayer: z.number().optional().default(0),
    // 分类抽卡：本轮先从哪类卡里抽几张（「随身物品 1 张、轮船线索 3 张」）。
    draws: z
      .array(z.object({ kind: z.string().optional().default(''), count: z.number().optional().default(0) }))
      .nullish()
      .transform((v) => v ?? []),
    // A committed clip that belongs to this phase (a letter read aloud, a sound
    // cue). Served from /script-assets like a clue picture.
    audioUrl: z.string().optional().default(''),
    audioLabel: z.string().optional().default(''),
  }),
  phaseIndex: z.number(),
  round: z.number(),
  self: publicSeatSchema,
  ownRole: ownRoleSchema.nullish().transform((v) => v ?? undefined),
  publicRoles: z.array(z.object({ intro: z.string().optional().default(''), appearance: z.string().optional().default('') })).nullish().transform((v) => v ?? []),
  ownClues: z.array(clueSchema).nullish().transform((v) => v ?? []),
  publicClues: z.array(clueSchema).nullish().transform((v) => v ?? []),
  ownFacts: z.array(factSchema).nullish().transform((v) => v ?? []),
  // Items gate the deep-dive clues; the server sends them per seat.
  items: z.array(z.string()).nullish().transform((v) => v ?? []),
  transcript: z.array(transcriptLineSchema).nullish().transform((v) => v ?? []),
  truth: truthSchema.nullish().transform((v) => v ?? undefined),
  /** 卡牌本：盒盖上印的五个角色（A 谈资源 …），身份分配不在里面。 */
  cardRoles: z
    .array(z.object({ id: z.string(), letter: z.string().optional().default(''), name: z.string(), intro: z.string().optional().default('') }))
    .nullish()
    .transform((v) => v ?? []),
  seats: z.array(publicSeatSchema).nullish().transform((v) => v ?? []),
  /** Host only: acting-bonus draws still available this search phase. */
  hostExtraLeft: z.number().optional().default(0),
})
export type Visible = z.infer<typeof visibleSchema>

export const turnOptionSchema = z.object({
  seatId: z.string(),
  role: z.string().optional().default(''),
  display: z.string().optional().default(''),
  kind: z.string().optional().default(''),
})
export type TurnOption = z.infer<typeof turnOptionSchema>

/** What the engine is blocked on; only the host ever receives one. */
export const turnPromptSchema = z.object({
  kind: z.string(),
  phaseId: z.string().optional().default(''),
  text: z.string().optional().default(''),
  seats: z.array(turnOptionSchema).nullish().transform((v) => v ?? []),
})
export type TurnPrompt = z.infer<typeof turnPromptSchema>

/** One entry of the discussion speaking queue; only the head holds the floor. */
export const speechSlotSchema = z.object({
  seatId: z.string(),
  kind: z.string().optional().default(''),
  replyTo: z.string().optional().default(''),
  note: z.string().optional().default(''),
})
export type SpeechSlot = z.infer<typeof speechSlotSchema>

/**
 * One clue a seat may search. It carries only the hint printed on the back of
 * the physical card, never the title: the title is the evidence itself.
 */
export const searchOptionSchema = z.object({
  id: z.string(),
  hint: z.string().optional().default(''),
  // 底卡写着「可获得曲径通幽 N」的那一张：不在本轮牌堆里，不占本轮张数，
  // 只能在轮到自己时点名称取（抽到底卡的人拿到的是这张卡，不是随机再抽）。
  granted: z.boolean().optional().default(false),
})
export type SearchOption = z.infer<typeof searchOptionSchema>

export const cardViewSchema = z.object({
  id: z.string(),
  kind: z.string(),
  color: z.string().optional().default(''),
  number: z.number().optional().default(0),
  title: z.string().optional().default(''),
  text: z.string().optional().default(''),
})
export type CardView = z.infer<typeof cardViewSchema>

export const cardPendingSchema = z.object({
  seatId: z.string(),
  kind: z.string(),
  target: z.string().optional().default(''),
  gained: z.string().optional().default(''),
})
export type CardPending = z.infer<typeof cardPendingSchema>

export const cardScoreSchema = z.object({
  seatId: z.string(),
  rank: z.number().optional().default(0),
  rankPoints: z.number().optional().default(0),
  killerPoints: z.number().optional().default(0),
  playerPoints: z.number().optional().default(0),
  identityPoints: z.number().optional().default(0),
  total: z.number().optional().default(0),
})
export type CardScore = z.infer<typeof cardScoreSchema>

/** 卡牌本的台面：大家都能看的部分，加上本人的可出牌集合。 */
export const tableViewSchema = z.object({
  top: cardViewSchema.nullish().transform((v) => v ?? undefined),
  /** 台面是技能牌时，下家真正要跟的那一张（技能牌没有颜色和数字）。 */
  base: cardViewSchema.nullish().transform((v) => v ?? undefined),
  turnSeatId: z.string().optional().default(''),
  counts: z.record(z.number()).nullish().transform((v) => v ?? {}),
  finished: z.array(z.string()).nullish().transform((v) => v ?? []),
  forcedColor: z.string().optional().default(''),
  randomNext: z.boolean().optional().default(false),
  pending: cardPendingSchema.nullish().transform((v) => v ?? undefined),
  result: z.array(cardScoreSchema).nullish().transform((v) => v ?? []),
  playable: z.array(z.string()).nullish().transform((v) => v ?? []),
  colors: z.array(z.string()).nullish().transform((v) => v ?? []),
  deck: z.number().optional().default(0),
  /** 整个出牌堆：陷入僵局要从这里挑两张线索牌。 */
  discard: z.array(cardViewSchema).nullish().transform((v) => v ?? []),
})
export type TableView = z.infer<typeof tableViewSchema>

export const seatStateSchema = z.object({
  visible: visibleSchema,
  phases: z.array(phaseSummarySchema).nullish().transform((v) => v ?? []),
  floor: z
    .object({
      seatId: z.string(),
      phaseId: z.string().optional().default(''),
      kind: z.string().optional().default(''),
      replyTo: z.string().optional().default(''),
      expiresAt: z.string().optional().default(''),
    })
    .nullish()
    .transform((v) => v ?? undefined),
  status: z.string(),
  /** The action part of this phase is over and the host has not advanced yet. */
  phaseDone: z.boolean().optional().default(false),
  voteCount: z.number().optional().default(0),
  /** 本人是否已经投过票（弃权也算）。 */
  voted: z.boolean().optional().default(false),
  votes: z.record(z.number()).nullish().transform((v) => v ?? undefined),
  search: z.array(searchOptionSchema).nullish().transform((v) => v ?? []),
  /** Random search mode: how many draws this seat still owes. */
  draw: z.number().optional().default(0),
  /** Seat ids in this phase's turn order, as the engine will walk them. */
  order: z.array(z.string()).nullish().transform((v) => v ?? []),
  /** Open discussion: anybody may ask for the floor, one speaker at a time. */
  talkOpen: z.boolean().optional().default(false),
  queue: z.array(speechSlotSchema).nullish().transform((v) => v ?? []),
  prompt: turnPromptSchema.nullish().transform((v) => v ?? undefined),
  /** 卡牌本：本人的手牌。 */
  hand: z.array(cardViewSchema).nullish().transform((v) => v ?? []),
  /** 卡牌本：台面（含本人当前可出的牌）。 */
  table: tableViewSchema.nullish().transform((v) => v ?? undefined),
  /** 卡牌本：本人已交回积分卡。 */
  scoreDone: z.boolean().optional().default(false),
  /** 行动点（K6 的「仙骨」）：本人余量，以及这张桌上每个人的余量。 */
  points: z.number().optional().default(0),
  pointsLabel: z.string().optional().default(''),
  tablePoints: z.record(z.number()).nullish().transform((v) => v ?? {}),
  /** 本角色的特殊技能；没写技能的本子不会有这一块。 */
  skill: z
    .object({
      id: z.string(),
      name: z.string(),
      text: z.string().optional().default(''),
      cost: z.number().optional().default(0),
      allCost: z.boolean().optional().default(false),
      exactCost: z.number().optional().default(0),
      /** "other" 由本人挑一名道友，"next" 自动指向下一名，空则不选人。 */
      target: z.string().optional().default(''),
      used: z.boolean().optional().default(false),
      ready: z.boolean().optional().default(false),
      why: z.string().optional().default(''),
    })
    .nullish()
    .transform((v) => v ?? undefined),
})
export type SeatState = z.infer<typeof seatStateSchema>

export const gameEventSchema = z.object({
  seq: z.number(),
  gameId: z.string().optional().default(''),
  at: z.string().optional().default(''),
  type: z.string(),
  phaseId: z.string().optional().default(''),
  seatId: z.string().optional().default(''),
  roleId: z.string().optional().default(''),
  speaker: z.string().optional().default(''),
  visibility: z.string().optional().default('public'),
  text: z.string().optional().default(''),
  clueId: z.string().optional().default(''),
  meta: z.record(z.string()).optional(),
})
export type GameEvent = z.infer<typeof gameEventSchema>

export const scriptSummarySchema = z.object({
  id: z.string(),
  title: z.string().optional().default(''),
  summary: z.string().optional().default(''),
  status: z.string(),
  version: z.number().optional().default(1),
  source: z.string().optional().default('json'),
  tags: z.array(z.string()).nullish().transform((v) => v ?? []),
  roles: z.number().optional().default(0),
  clues: z.number().optional().default(0),
  playerCount: z.object({ min: z.number(), max: z.number() }).optional(),
  owner: z.boolean().optional().default(false),
  warnings: z.number().optional().default(0),
  updatedAt: z.string().optional().default(''),
})
export type ScriptSummary = z.infer<typeof scriptSummarySchema>

export const validationErrorSchema = z.object({ path: z.string(), message: z.string() })
export type ValidationError = z.infer<typeof validationErrorSchema>

/**
 * One uploaded original: a zip, a docx, a pile of photos. The server keeps the
 * bytes untouched in its inbox so a developer can convert them offline; nothing
 * here is parsed.
 */
export const rawMaterialSchema = z.object({
  id: z.string(),
  name: z.string(),
  path: z.string().optional().default(''),
  mime: z.string().optional().default(''),
  size: z.number().optional().default(0),
  uploadedBy: z.string().optional().default(''),
  createdAt: z.string().optional().default(''),
})
export type RawMaterial = z.infer<typeof rawMaterialSchema>

export const adminUserSchema = z.object({
  id: z.string(),
  username: z.string(),
  displayName: z.string(),
  role: z.enum(['user', 'admin']),
  scripts: z.number().optional().default(0),
  createdAt: z.string(),
  lastLoginAt: z.string().optional(),
  me: z.boolean().optional().default(false),
})
export type AdminUser = z.infer<typeof adminUserSchema>

export const adminRoomSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string().optional().default(''),
  status: z.string(),
  scriptTitle: z.string().optional().default(''),
  gameId: z.string().optional().default(''),
  hostName: z.string().optional().default(''),
  seats: z.number().optional().default(0),
  players: z.number().optional().default(0),
  running: z.boolean().optional().default(false),
  createdAt: z.string().optional().default(''),
})
export type AdminRoom = z.infer<typeof adminRoomSchema>

export const scriptRecordSchema = z.object({
  id: z.string(),
  ownerUserId: z.string().optional().default(''),
  status: z.string(),
  version: z.number().optional().default(1),
  source: z.string().optional().default('json'),
  body: z.unknown().optional(),
  warnings: z.array(z.string()).nullish().transform((v) => v ?? []),
  rawSource: z.string().optional().default(''),
})
export type ScriptRecord = z.infer<typeof scriptRecordSchema>

export function parseRoom(payload: unknown): Room {
  return roomSchema.parse(payload)
}

export const replaySchema = z.object({
  game: z
    .object({
      id: z.string().optional().default(''),
      status: z.string().optional().default(''),
      seats: z.array(publicSeatSchema).nullish().transform((v) => v ?? []),
    })
    .passthrough(),
  truth: truthSchema,
  phases: z.array(phaseSummarySchema).nullish().transform((v) => v ?? []),
  replay: z.array(gameEventSchema).nullish().transform((v) => v ?? []),
})
export type Replay = z.infer<typeof replaySchema>
