import { useEffect, useMemo, useState } from 'react'
import { Empty, SectionTitle, Tag } from '../../components/ui'
import type { CardView, SeatState } from '../../types/schema'

/**
 * 卡牌本的牌桌。
 *
 * 引擎已经把规则算完了：`table.playable` 是本人现在能出的牌，`table.pending`
 * 是技能牌的第二半。这里只负责把「能出的牌 / 要指定谁 / 积分卡」画出来并回传，
 * 从不自己判断合法性——两边各算一次规则，早晚会算出两个答案。
 */
export type CardCmd = {
  type: string
  cardId?: string
  cardIds?: string[]
  color?: string
  to?: string
  roleId?: string
  guesses?: Record<string, string>
}

/** 五类线索各一个色相，和实体牌的颜色对不上，但牌桌上一眼能分。 */
const COLOR_TONE: Record<string, string> = {
  现场: 'text-rose-300',
  试探身份: 'text-amber-300',
  新闻报道: 'text-sky-300',
  路人证言: 'text-emerald-300',
  短信记录: 'text-violet-300',
}

const KIND_LABEL: Record<string, string> = {
  clue: '线索',
  stop: '功能',
  recolor: '功能',
  inspect: '技能',
  stall: '技能',
  swap: '技能',
}

export function CardTable({
  state,
  seatId,
  isSpectator,
  send,
}: {
  state: SeatState
  seatId: string
  isSpectator: boolean
  send: (cmd: CardCmd) => boolean
}) {
  const table = state.table
  const phase = state.visible.phase
  const hand = state.hand
  const [armed, setArmed] = useState<CardView | null>(null)
  const [picks, setPicks] = useState<string[]>([])
  const [killerRole, setKillerRole] = useState('')
  const [killerSeat, setKillerSeat] = useState('')
  const [guesses, setGuesses] = useState<Record<string, string>>({})

  const playable = useMemo(() => new Set(table?.playable ?? []), [table?.playable])
  const myTurn = !!table && table.turnSeatId === seatId && !isSpectator
  const pending = table?.pending && table.pending.seatId === seatId ? table.pending : undefined
  const players = useMemo(
    () => (state.visible.seats ?? []).filter((s) => s.kind !== 'spectator'),
    [state.visible.seats],
  )
  const roles = state.visible.cardRoles ?? []

  // 换人出牌时收起上一步的选择：一张牌已经打出去，还举着瞄准框只会误点。
  useEffect(() => {
    setArmed(null)
    setPicks([])
  }, [table?.turnSeatId, pending?.kind])

  if (!table) return null

  const nameOf = (id: string) => {
    const seat = players.find((s) => s.id === id)
    return seat?.roleName || seat?.displayName || id
  }

  const play = (card: CardView) => {
    if (card.kind === 'recolor' || card.kind === 'inspect' || card.kind === 'stall' || card.kind === 'swap') {
      setArmed(card)
      return
    }
    setArmed(null)
    send({ type: 'card.play', cardId: card.id })
  }

  const handCount = (id: string) => table.counts[id] ?? 0

  return (
    <div className="mt-3 space-y-3 border-t border-line/60 pt-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <SectionTitle>牌桌</SectionTitle>
        {table.forcedColor ? <Tag tone="amber">必须跟 {table.forcedColor}</Tag> : null}
        {table.randomNext ? <Tag tone="amber">下家随机出牌</Tag> : null}
        <span className="text-xs text-slate-400">
          {table.turnSeatId ? `轮到 ${nameOf(table.turnSeatId)}` : '牌局结束'}
        </span>
        <span className="ml-auto font-mono text-[11px] text-slate-500">出牌堆 {table.discard.length} · 未出 {table.deck}</span>
      </div>

      <div className="flex flex-wrap items-stretch gap-2">
        <div className="min-w-[9rem] flex-1 rounded-xl border border-line/70 bg-ink/40 px-3 py-2">
          <p className="text-[11px] text-slate-500">台面</p>
          {table.top ? (
            <>
              <p className={`text-sm font-medium ${COLOR_TONE[table.top.color] ?? 'text-strong'}`}>
                {table.top.title}
              </p>
              <p className="mt-1 text-xs leading-relaxed text-slate-300">{table.top.text}</p>
              {table.base ? (
                <p className="mt-1 text-[11px] text-accent">
                  技能牌不算颜色和数字，下家跟的是「{table.base.title}」
                </p>
              ) : null}
            </>
          ) : (
            <p className="text-sm text-slate-400">还没人出牌，什么牌都能开</p>
          )}
        </div>
        <ul className="flex flex-wrap gap-x-3 gap-y-1 rounded-xl border border-line/70 bg-ink/40 px-3 py-2 text-[11px] text-slate-400">
          {players.map((s) => (
            <li key={s.id} className={table.turnSeatId === s.id ? 'text-accent' : ''}>
              {s.roleName || s.displayName}
              <span className="ml-1 font-mono text-slate-500">{handCount(s.id)}</span>
              {table.finished.includes(s.id) ? <span className="ml-1 text-emerald-300">出完</span> : null}
            </li>
          ))}
        </ul>
      </div>

      {pending?.kind === 'stall' ? (
        <PendingStall
          table={table}
          target={nameOf(pending.target)}
          picks={picks}
          onPick={(id) =>
            setPicks((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : cur.length >= 2 ? cur : [...cur, id]))
          }
          onSend={() => send({ type: 'card.stall', cardIds: picks })}
        />
      ) : null}

      {pending?.kind === 'swap' ? (
        <div className="rounded-xl border border-accent/30 bg-accent/5 p-3">
          <p className="mb-2 text-xs text-accent">
            交换线索：你从 {nameOf(pending.target)} 手里抽到了
            {pending.gained ? `「${hand.find((c) => c.id === pending.gained)?.title ?? pending.gained}」` : '一张牌'}
            ，现在选一张自己的牌交给他
          </p>
          <div className="flex flex-wrap gap-2">
            {hand
              .filter((c) => c.id !== pending.gained)
              .map((c) => (
                <button
                  key={c.id}
                  type="button"
                  className="btn-ghost min-h-[44px] text-xs xl:min-h-9"
                  onClick={() => send({ type: 'card.give', cardId: c.id })}
                >
                  交出 {c.title}
                </button>
              ))}
          </div>
        </div>
      ) : null}

      {armed ? (
        <div className="rounded-xl border border-accent/40 bg-accent/10 p-3">
          <p className="mb-2 text-xs text-accent">
            {armed.title}：{armed.kind === 'recolor' ? '指定下一位要跟的颜色' : '指定一位玩家'}
          </p>
          <div className="flex flex-wrap gap-2">
            {armed.kind === 'recolor'
              ? table.colors.map((c) => (
                  <button
                    key={c}
                    type="button"
                    className="btn-ghost min-h-[44px] text-xs xl:min-h-9"
                    onClick={() => {
                      send({ type: 'card.play', cardId: armed.id, color: c })
                      setArmed(null)
                    }}
                  >
                    <span className={COLOR_TONE[c] ?? ''}>●</span> {c}
                  </button>
                ))
              : players
                  .filter((s) => s.id !== seatId)
                  .map((s) => (
                    <button
                      key={s.id}
                      type="button"
                      className="btn-ghost min-h-[44px] text-xs xl:min-h-9"
                      onClick={() => {
                        send({ type: 'card.play', cardId: armed.id, to: s.id })
                        setArmed(null)
                      }}
                    >
                      对 {s.roleName || s.displayName}
                    </button>
                  ))}
            <button type="button" className="btn-ghost min-h-[44px] text-xs xl:min-h-9" onClick={() => setArmed(null)}>
              取消
            </button>
          </div>
        </div>
      ) : null}

      {!isSpectator ? (
        <div>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <p className="text-xs text-slate-400">我的手牌 {hand.length} 张</p>
            {myTurn && table.randomNext ? (
              <button type="button" className="btn-primary min-h-[44px] text-xs xl:min-h-9" onClick={() => send({ type: 'card.random' })}>
                随机打出一张
              </button>
            ) : null}
            {myTurn && !table.randomNext && !pending ? (
              <button
                type="button"
                className="btn-ghost min-h-[44px] text-xs xl:min-h-9"
                onClick={() => send({ type: 'card.pass' })}
              >
                过牌
              </button>
            ) : null}
            <span className="text-[11px] text-slate-500">
              同色或同数字才能出；黑技能牌随时能出
            </span>
          </div>
          {hand.length === 0 ? (
            <Empty>手牌出完了，等其他人打完。</Empty>
          ) : (
            // 27 张牌排下来比一屏还高，给它自己的滚动区，否则会把下面的面板顶出去。
            <div className="scroll-thin max-h-[42vh] overflow-y-auto pr-1 xl:max-h-[38vh]">
              <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
                {hand.map((c) => {
                  const ok = myTurn && playable.has(c.id)
                  return (
                    <li key={c.id}>
                      <button
                        type="button"
                        disabled={!ok || !!pending}
                        onClick={() => play(c)}
                        title={c.text}
                        className={`flex h-full min-h-[76px] w-full flex-col gap-0.5 rounded-xl border px-2.5 py-2 text-left text-xs transition-colors ${
                          ok
                            ? 'border-accent/60 bg-accent/10 text-strong hover:border-accent'
                            : 'border-line/60 bg-ink/35 text-slate-400'
                        }`}
                      >
                        <span className="flex items-center justify-between gap-1 font-mono text-[10px] text-slate-500">
                          <span className={COLOR_TONE[c.color] ?? ''}>
                            {c.color ? `${c.color} ${c.number || ''}` : '技能'}
                          </span>
                          <span>{KIND_LABEL[c.kind] ?? ''}</span>
                        </span>
                        <span className="line-clamp-2 font-medium leading-snug">{c.title}</span>
                        <span className="line-clamp-2 text-[11px] leading-snug text-slate-500">{c.text}</span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            </div>
          )}
        </div>
      ) : null}

      {phase?.type === 'score' && !isSpectator ? (
        <ScoreSheet
          roles={roles}
          players={players}
          seatId={seatId}
          done={state.scoreDone}
          killerRole={killerRole}
          killerSeat={killerSeat}
          guesses={guesses}
          onChangeRole={setKillerRole}
          onChangeSeat={setKillerSeat}
          onChangeGuess={(id, roleId) => setGuesses((cur) => ({ ...cur, [id]: roleId }))}
          onSubmit={() => send({ type: 'score.submit', roleId: killerRole, to: killerSeat, guesses })}
        />
      ) : null}

      {table.result.length > 0 ? (
        <div className="rounded-xl border border-line/70 bg-ink/40 p-3">
          <p className="mb-2 text-xs text-slate-400">积分卡结算</p>
          <ol className="space-y-1 text-xs">
            {table.result.map((row, i) => (
              <li key={row.seatId} className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-mono text-slate-500">{i + 1}.</span>
                <span className="text-strong">{nameOf(row.seatId)}</span>
                <span className="ml-auto font-mono text-strong">{row.total} 分</span>
                <span className="w-full text-[11px] text-slate-500">
                  名次 {row.rankPoints} · 凶手身份 {row.killerPoints} · 指认玩家 {row.playerPoints} · 认人 {row.identityPoints}
                </span>
              </li>
            ))}
          </ol>
        </div>
      ) : null}
    </div>
  )
}

function PendingStall({
  table,
  target,
  picks,
  onPick,
  onSend,
}: {
  table: NonNullable<SeatState['table']>
  target: string
  picks: string[]
  onPick: (id: string) => void
  onSend: () => void
}) {
  const clues = table.discard.filter((c) => c.kind === 'clue')
  return (
    <div className="rounded-xl border border-accent/30 bg-accent/5 p-3">
      <p className="mb-2 text-xs text-accent">陷入僵局：从出牌堆里挑两张线索牌交给 {target}（已选 {picks.length}/2）</p>
      {clues.length === 0 ? (
        <p className="text-xs text-slate-500">出牌堆里还没有线索牌。</p>
      ) : (
        <div className="scroll-thin flex max-h-40 flex-wrap gap-2 overflow-y-auto">
          {clues.map((c) => (
            <button
              key={c.id}
              type="button"
              aria-pressed={picks.includes(c.id)}
              onClick={() => onPick(c.id)}
              className={`rounded-lg border px-2 py-1 text-[11px] ${
                picks.includes(c.id) ? 'border-accent bg-accent/20 text-strong' : 'border-line/60 text-slate-300'
              }`}
            >
              {c.title}
            </button>
          ))}
        </div>
      )}
      <button type="button" className="btn-primary mt-2 min-h-[44px] text-xs xl:min-h-9" disabled={picks.length !== 2} onClick={onSend}>
        交给 {target}
      </button>
    </div>
  )
}

function ScoreSheet({
  roles,
  players,
  seatId,
  done,
  killerRole,
  killerSeat,
  guesses,
  onChangeRole,
  onChangeSeat,
  onChangeGuess,
  onSubmit,
}: {
  roles: NonNullable<SeatState['visible']['cardRoles']>
  players: NonNullable<SeatState['visible']['seats']>
  seatId: string
  done: boolean
  killerRole: string
  killerSeat: string
  guesses: Record<string, string>
  onChangeRole: (v: string) => void
  onChangeSeat: (v: string) => void
  onChangeGuess: (seat: string, roleId: string) => void
  onSubmit: () => void
}) {
  if (done) {
    return <p className="rounded-xl border border-emerald-500/30 bg-emerald-500/5 px-3 py-2 text-xs text-emerald-200">已交回积分卡，等待其他玩家。</p>
  }
  return (
    <div className="rounded-xl border border-line/70 bg-ink/40 p-3">
      <p className="mb-2 text-xs text-slate-400">积分卡：凶手是谁、凶手是哪位玩家、在座每个人分别是谁</p>
      <div className="grid gap-2 sm:grid-cols-2">
        <label className="text-[11px] text-slate-500">
          你认为凶手是
          <select className="input mt-1 w-full" value={killerRole} onChange={(e) => onChangeRole(e.target.value)}>
            <option value="">—</option>
            {roles.map((r) => (
              <option key={r.id} value={r.id}>
                {r.letter} {r.name}
              </option>
            ))}
          </select>
        </label>
        <label className="text-[11px] text-slate-500">
          凶手是哪位玩家
          <select className="input mt-1 w-full" value={killerSeat} onChange={(e) => onChangeSeat(e.target.value)}>
            <option value="">—</option>
            {players.map((s) => (
              <option key={s.id} value={s.id}>
                {s.displayName}
                {s.id === seatId ? '（我）' : ''}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="mt-2 grid gap-2 sm:grid-cols-2">
        {players.map((s) => (
          <label key={s.id} className="text-[11px] text-slate-500">
            {s.displayName}
            {s.id === seatId ? '（我）' : ''} 是
            <select
              className="input mt-1 w-full"
              value={guesses[s.id] ?? ''}
              onChange={(e) => onChangeGuess(s.id, e.target.value)}
            >
              <option value="">—</option>
              {roles.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.letter} {r.name}
                </option>
              ))}
            </select>
          </label>
        ))}
      </div>
      <button type="button" className="btn-primary mt-2 min-h-[44px] text-xs xl:min-h-9" onClick={onSubmit} disabled={!killerRole || !killerSeat}>
        交回积分卡
      </button>
    </div>
  )
}
