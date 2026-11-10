import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { useGame } from '../../store/game'
import { RoomSocket } from '../../realtime/client'
import { Empty, Hint, SectionTitle, Spinner, StatusDot, Tag } from '../../components/ui'
import { Markdown } from '../../components/Markdown'
import { styleOf, type Line } from '../../lib/transcript'
import { ReadingPage } from './ReadingPage'
import { CardTable } from './CardTable'
import { narrationFor, probeVoices, speak, stopSpeaking, warmVoices, type VoiceCapabilities } from '../../voice'
import { useVoiceCapabilities } from '../../lib/useVoice'
import type { GameEvent, SeatState, SpeechSlot, TurnOption, TurnPrompt, Visible } from '../../types/schema'

/** One command frame. `replyTo` makes a line an answer to a specific seat. */
type SendCmd = {
  type: string
  text?: string
  clueId?: string
  roleId?: string
  seatIds?: string[]
  replyTo?: string
  /** Target of a whisper. */
  to?: string
  /** 卡牌本：出的哪张牌、挑的哪两张、指定的颜色。 */
  cardId?: string
  cardIds?: string[]
  color?: string
  /** 卡牌本：积分卡上的认人结果（seatId → roleId）。 */
  guesses?: Record<string, string>
  /** 技能指向的道友。 */
  targetId?: string
  /** 主持人命令所属阶段，防止跨阶段误发。 */
  phaseId?: string
}

/** Events that change what this seat may see, so the snapshot must be refetched. */
const REFRESH_EVENTS = new Set([
  'phase',
  'clue.found',
  'clue.revealed',
  'vote',
  'vote.result',
  'reveal',
  'seat.updated',
  'system',
])

/**
 * The table itself.
 *
 * Everything rendered comes from `GET /state` (already filtered per seat) plus
 * the websocket stream, which the server also filters per seat. The client has
 * no code path that could reveal another seat's script.
 */
export function GamePage({ code }: { code: string }) {
  const navigate = useApp((s) => s.navigate)
  const notify = useApp((s) => s.notify)
  const token = useApp((s) => s.token)

  const room = useGame((s) => s.room)
  const state = useGame((s) => s.state)
  const lines = useGame((s) => s.lines)
  const conn = useGame((s) => s.conn)
  const tts = useGame((s) => s.tts)
  const setRoom = useGame((s) => s.setRoom)
  const setState = useGame((s) => s.setState)
  const pushEvent = useGame((s) => s.pushEvent)
  const setConn = useGame((s) => s.setConn)
  const setTts = useGame((s) => s.setTts)
  // Follows `voiceschanged`, so a voice list that lands after mount is not
  // mistaken for "this machine cannot read Chinese aloud".
  const voice = useVoiceCapabilities()

  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  /** Seat id this line answers, kept until the line is sent or cancelled. */
  const [replyTo, setReplyTo] = useState('')
  /** Seat id a private line goes to; empty means "say it to the table". */
  const [whisperTo, setWhisperTo] = useState('')
  const [reading, setReading] = useState(false)
  const socketRef = useRef<RoomSocket | null>(null)
  const refreshTimer = useRef<number | null>(null)
  const snapshotRead = useRef(false)

  const seatId = state?.visible.self.id ?? ''
  const isSpectator = state?.visible.self.kind === 'spectator'
  const isHost = state?.visible.self.isHost ?? false
  const phase = state?.visible.phase
  const floorMine = !!state?.floor && state.floor.seatId === seatId
  // Private talk only exists while the table is discussing: a whisper during
  // search or voting would be a hole in the flow the engine rejects anyway.
  const canWhisper = !!state && state.status === 'running' && phase?.type === 'discussion' && !isSpectator
  const whisperTargets = useMemo(
    () => (state?.visible.seats ?? []).filter((s) => s.roleId && s.id !== seatId && s.kind !== 'spectator'),
    [state?.visible.seats, seatId],
  )

  // Lines that arrived before this page opened only exist in the snapshot, so
  // the host's opening monologue would be silent. Read the newest one once,
  // and only when it is fresh: never replay a whole backlog.
  const readSnapshotNarration = useCallback((st: SeatState) => {
    if (snapshotRead.current || !useGame.getState().tts) return
    snapshotRead.current = true
    const lines = st.visible.transcript ?? []
    const last = [...lines].reverse().find((l) => l.kind === 'dm' && l.text.trim())
    if (!last) return
    const at = Date.parse(last.at ?? '')
    if (Number.isFinite(at) && Date.now() - at > 3 * 60_000) return
    speak(`主持人：${last.text}`, { seed: 'narrator' })
  }, [])

  const refresh = useCallback(async () => {
    try {
      const res = await api.roomState(code)
      setRoom(res.room)
      setState(res.state)
      setLoadError(null)
      readSnapshotNarration(res.state)
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return
      setLoadError(err instanceof ApiError ? err.message : '读取对局状态失败')
    } finally {
      setLoading(false)
    }
  }, [code, setRoom, setState, readSnapshotNarration])

  // Initial snapshot, then the websocket carries increments. A different room
  // means the transcript on screen belongs to some other game, so drop it.
  useEffect(() => {
    useGame.getState().reset()
    setDraft('')
    setReplyTo('')
    setWhisperTo('')
    setLoading(true)
    snapshotRead.current = false
    // Chrome resolves the voice list lazily; ask now so the first line has a voice.
    warmVoices()
    void refresh()
    return () => {
      if (refreshTimer.current !== null) window.clearTimeout(refreshTimer.current)
    }
  }, [refresh])

  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current !== null) return
    refreshTimer.current = window.setTimeout(() => {
      refreshTimer.current = null
      void refresh()
    }, 350)
  }, [refresh])

  useEffect(() => {
    if (!token || !seatId) return
    const socket = new RoomSocket({
      code,
      token,
      seatId,
      fetchTicket: () => api.seatTicket(code).catch(() => null),
      onStatus: (status) => setConn(status === 'open' ? 'open' : status === 'connecting' ? 'connecting' : 'closed'),
      onError: (message) => notify(message, 'error'),
      onResync: scheduleRefresh,
      onEvent: (ev: GameEvent) => {
        pushEvent(ev)
        if (useGame.getState().tts) {
          const line = narrationFor(ev, seatId)
          if (line) speak(line.text, { seed: line.seed })
        }
        // A found clue changes "my clues" immediately; waiting for the debounce
        // makes the panel look stale right after the user clicked.
        if (ev.type === 'clue.found') void refresh()
        else if (REFRESH_EVENTS.has(ev.type)) scheduleRefresh()
      },
    })
    socketRef.current = socket
    socket.connect()
    return () => {
      socket.close()
      socketRef.current = null
    }
  }, [code, token, seatId, notify, pushEvent, scheduleRefresh, setConn])

  const send = useCallback(
    (cmd: SendCmd) => {
      const ok = socketRef.current ? socketRef.current.send(cmd) : false
      if (!ok) notify('连接还没准备好，请稍等一秒再试', 'error')
      return ok
    },
    [notify],
  )

  // Leaving the discussion must not leave the box in private mode: the next
  // phase would send frames the engine rejects.
  useEffect(() => {
    if (!canWhisper && whisperTo) setWhisperTo('')
  }, [canWhisper, whisperTo])

  const submitSpeech = () => {
    const text = draft.trim()
    if (!text) return
    // A private line has its own frame: it must not consume the floor, and the
    // other seats never receive it.
    if (whisperTo) {
      if (send({ type: 'whisper.send', to: whisperTo, text })) {
        setDraft('')
        setReplyTo('')
      }
      return
    }
    if (send({ type: 'speak.text', text, replyTo: replyTo || undefined })) {
      setDraft('')
      setReplyTo('')
    }
  }

  if (loading && !state) return <Spinner label="正在进入牌桌…" />
  if (!state || !room) {
    return (
      <div className="card mx-auto max-w-lg space-y-3 text-center">
        <p className="text-sm text-slate-300">{loadError ?? '还拿不到这个座位的对局状态。'}</p>
        <p className="text-xs text-slate-500">
          如果你还没入座，请先回房间选角色；如果刚开局，重新加载即可。
        </p>
        <div className="flex justify-center gap-2">
          <button type="button" className="btn-primary text-xs" onClick={() => void refresh()}>
            重新加载
          </button>
          <button type="button" className="btn-ghost text-xs" onClick={() => navigate('room', code)}>
            回到房间
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="grid gap-4 xl:grid-cols-[300px_minmax(0,1fr)_280px]">
      <RolePanel
        visible={state.visible}
        onReveal={(clueId) => send({ type: 'clue.reveal', clueId })}
        onRead={() => setReading(true)}
      />

      {/* On a phone the table comes first and the side panels follow it, so the
          transcript is not pushed below a screenful of role book. */}
      <section className="card order-1 flex max-h-[85vh] min-h-[60vh] flex-col xl:order-none xl:max-h-none xl:min-h-[70vh]">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm tracking-widest text-accent">{room.code}</span>
            <Tag tone={state.status === 'finished' ? 'slate' : 'green'}>
              {phase?.name || '等待开始'} {phase?.round ? `· 第 ${phase.round} 轮` : ''}
            </Tag>
            {isSpectator ? <Tag tone="amber">观战中</Tag> : null}
            {floorMine ? <Tag tone="green">{state.floor?.kind === 'reply' ? '轮到你回应' : '轮到你发言'}</Tag> : null}
            {state.order.length > 0 && !floorMine ? <Tag>{orderLabel(phase?.order)}</Tag> : null}
          </div>
          <div className="flex items-center gap-3">
            <StatusDot state={conn} />
            {state.visible.ownRole ? (
              <button type="button" className="btn-ghost text-xs" onClick={() => setReading(true)}>
                阅读剧本
              </button>
            ) : null}
            <button
              type="button"
              className="btn-ghost text-xs"
              aria-pressed={voice.tts ? tts : false}
              disabled={!voice.tts}
              title={voiceTitle(voice)}
              onClick={() => {
                stopSpeaking()
                warmVoices()
                // Phones only enumerate voices after the engine runs once, so the
                // tap that turns reading on is also the tap that wakes it.
                probeVoices()
                setTts(!tts)
              }}
            >
              {voiceLabel(voice, tts)}
            </button>
           {isHost && state.status === 'running' ? (
              <button
                type="button"
                className="btn-ghost text-xs text-rose-300 hover:text-rose-200"
                onClick={() => {
                  // A play/score script has no reveal phase of its own, so the table
                  // can only leave "running" through the host's explicit ending.
                  if (window.confirm('结束本局？牌桌会立即停下并释放名额，所有人仍能看复盘。')) send({ type: 'host.end' })
                }}
              >
                结束对局
              </button>
            ) : null}
            <button type="button" className="btn-ghost text-xs" onClick={() => navigate('room', code)}>
              房间
            </button>
          </div>
        </div>

        <ThinkingBar visible={state.visible} />

        {state.visible.phase.audioUrl ? (
          <div className="mt-3 rounded-xl border border-line/70 bg-ink/40 px-3 py-2">
            <p className="mb-2 text-xs text-slate-400">{state.visible.phase.audioLabel || '本阶段音频'}</p>
            <audio className="w-full" controls preload="metadata" src={state.visible.phase.audioUrl} />
          </div>
        ) : null}

        <Transcript
          lines={lines}
          selfSeatId={seatId}
          onReply={isSpectator ? undefined : (id) => setReplyTo(id === replyTo ? '' : id)}
          onAdvance={isHost && state.status === 'running' ? () => send({ type: 'host.skip' }) : undefined}
        />

        {state.status === 'finished' ? (
          <div className="mt-3 flex flex-wrap items-center gap-2 rounded-xl border border-line/70 bg-ink/40 px-3 py-2 text-sm">
            <span>本局结束。</span>
            {isHost ? (
              <button type="button" className="btn-primary text-xs" onClick={() => navigate('replay', code)}>
                查看复盘与真相
              </button>
            ) : (
              <span className="text-xs text-slate-500">主持人可以在复盘里公开完整真相。</span>
            )}
            <button type="button" className="btn-ghost text-xs" onClick={() => navigate('lobby')}>
              回到大厅
            </button>
          </div>
        ) : null}

        {isHost && state.status === 'running' && state.prompt?.kind === 'order' ? (
          <div className="mt-3 border-t border-line/60 pt-3">
            <OrderPanel prompt={state.prompt} send={send} />
          </div>
        ) : null}

        {isHost && state.status === 'running' && !state.phaseDone && state.visible.phase.type === 'search' && state.visible.hostExtraLeft > 0 ? (
          <div className="mt-3 rounded-xl border border-amber-500/40 bg-amber-500/5 p-3">
            <p className="mb-2 text-xs text-amber-100">
              额外搜证（演技奖励）：本阶段还剩 {state.visible.hostExtraLeft} 次，点一名玩家给他加一次。
            </p>
            <div className="flex flex-wrap gap-2">
              {state.visible.seats
                .filter((s) => !s.isHost && s.kind !== 'spectator')
                .map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    className="btn-ghost text-xs"
                    onClick={() => send({ type: 'search.grant', targetId: s.id, phaseId: state.visible.phase.id })}
                  >
                    {s.roleName || s.displayName}
                  </button>
                ))}
            </div>
          </div>
        ) : null}

        {/* 卡牌本的牌桌：手牌、台面、技能指向和积分卡。 */}
        <CardTable state={state} seatId={seatId} isSpectator={isSpectator} send={send} />

        {!isSpectator && state.status === 'running' ? (
          <div className="mt-3 space-y-2 border-t border-line/60 pt-3">
            {replyTo ? (
              <div className="flex flex-wrap items-center gap-2 rounded-xl border border-accent/40 bg-accent/10 px-3 py-2 text-xs text-accent">
                <span>正在回应 {seatName(state.visible, replyTo)}</span>
                {floorMine ? <span className="text-slate-400">· 对方会先回答你</span> : null}
                <button type="button" className="btn-ghost ml-auto min-h-0 px-2 py-1 text-[11px]" onClick={() => setReplyTo('')}>
                  取消
                </button>
              </div>
            ) : null}
            {canWhisper ? (
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <label htmlFor="whisper-to" className="text-slate-500">
                  私聊对象
                </label>
                <select
                  id="whisper-to"
                  className="input w-auto max-w-full"
                  value={whisperTo}
                  onChange={(e) => setWhisperTo(e.target.value)}
                >
                  <option value="">说给所有人</option>
                  {whisperTargets.map((s) => (
                    <option key={s.id} value={s.id}>
                      只对 {s.roleName || s.displayName} 说
                    </option>
                  ))}
                </select>
                {whisperTo ? <span className="text-slate-500">只有你们两个看得到</span> : null}
              </div>
            ) : null}
            <div className="flex items-end gap-2">
              <textarea
                className="input min-h-[64px] resize-none"
                placeholder={
                  whisperTo
                    ? `只给 ${seatName(state.visible, whisperTo)} 看：不占麦，也不进公共发言记录`
                    : floorMine
                      ? '轮到你了，说点什么…'
                      : state.talkOpen
                        ? '自由讨论中：可以直接说完，也可以先排队等麦克风'
                        : '随时可以补充发言'
                }
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  // Enter sends, Shift+Enter keeps the newline: this is a chat box
                  // first, a multi-line editor second.
                  if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                    e.preventDefault()
                    submitSpeech()
                  }
                }}
              />
              <div className="flex flex-col gap-2">
                <button type="button" className="btn-primary" onClick={submitSpeech} disabled={!draft.trim()}>
                  {whisperTo ? '私聊' : '发送'}
                </button>
              </div>
            </div>
            <VoiceHint caps={voice} />
            <PhaseActions state={state} send={send} seatId={seatId} isHost={isHost} onReplyPass={setReplyTo} />
          </div>
        ) : null}
      </section>

      <aside className="order-3 space-y-4 xl:order-none">
        <div className="sheet">
          <SectionTitle>阶段</SectionTitle>
          <ol className="space-y-1 text-xs">
            {(state.phases ?? []).map((p) => (
              <li
                key={p.id}
                className={`flex items-center justify-between rounded-lg px-2 py-1 ${
                  p.status === 'current'
                    ? 'bg-accent/15 text-strong'
                    : p.status === 'done'
                      ? 'text-slate-500 line-through'
                      : 'text-slate-400'
                }`}
              >
                <span>
                  {p.name}
                  {p.round ? `（第 ${p.round} 轮）` : ''}
                </span>
                <span className="text-[10px] uppercase tracking-wide">{p.type}</span>
              </li>
            ))}
          </ol>
          {state.order.length > 0 ? (
            <p className="mt-2 rounded-lg border border-line/60 px-2 py-1 text-[11px] leading-relaxed text-slate-400">
              本阶段顺序：{state.order.map((id) => seatName(state.visible, id)).join(' → ')}
            </p>
          ) : null}
        </div>

        <div className="sheet">
          <SectionTitle>座位</SectionTitle>
          <ul className="space-y-1 text-xs">
            {(state.visible.seats ?? []).map((s) => (
              <li key={s.id} className="flex items-center justify-between rounded-lg px-2 py-1">
                <span className={s.id === seatId ? 'text-accent' : 'text-slate-300'}>
                  {s.roleName || s.displayName}
                  {s.id === seatId ? '（我）' : ''}
                </span>
                <span className="flex items-center gap-2 text-slate-500">
                  {state.pointsLabel && state.tablePoints?.[s.id] !== undefined ? (
                    <span className="text-warm">
                      {state.tablePoints[s.id]} 根{state.pointsLabel}
                    </span>
                  ) : null}
                  {s.kind === 'agent' ? 'AI' : s.kind === 'spectator' ? '观战' : '真人'}
                </span>
              </li>
            ))}
          </ul>
        </div>

        {state.votes ? (
          <div className="sheet">
            <SectionTitle>计票</SectionTitle>
            <ul className="space-y-1 text-xs">
              {Object.entries(state.votes).map(([roleId, n]) => (
                <li key={roleId} className="flex justify-between px-2">
                  <span>{roleNameOf(state.visible, roleId)}</span>
                  <span className="text-slate-400">{n} 票</span>
                </li>
              ))}
              {Object.keys(state.votes).length === 0 ? <li className="px-2 text-slate-500">暂无</li> : null}
            </ul>
          </div>
        ) : null}
      </aside>

      {reading && state.visible.ownRole ? (
        <ReadingPage role={state.visible.ownRole} onClose={() => setReading(false)} />
      ) : null}
    </div>
  )
}

function isHostWait(text: string): boolean {
  return text.includes('等待主持人进入下一阶段') || text.includes('进入自由讨论')
}

/**
 * 本角色的特殊技能（K6：每位仙主一个，整局只能发动一次）。
 *
 * 稿面文字原样排出，能不能发由服务端算好（ready / why），这里不自己猜规则；
 * 需要点人的技能先选一道友，再按「发动」。
 */
function SkillCard({
  skill,
  points,
  pointsLabel,
  seats,
  target,
  onTarget,
  onUse,
}: {
  skill: NonNullable<SeatState['skill']>
  points: number
  pointsLabel: string
  seats: { id: string; roleName?: string; displayName: string }[]
  target: string
  onTarget: (id: string) => void
  onUse: (targetId: string) => void
}) {
  const needsTarget = skill.target === 'other'
  const disabled = !skill.ready || (needsTarget && !target)
  return (
    <div className="rounded-xl border border-warm/40 bg-warm/5 p-3">
      <p className="text-xs font-medium text-warm">
        特殊技能 · {skill.name}
        {skill.used ? '（已用过）' : ''}
      </p>
      <p className="mt-1 text-xs leading-relaxed text-slate-300">{skill.text}</p>
      <p className="mt-1 text-[11px] text-slate-500">
        你现在有 {points} 根{pointsLabel}
        {needsTarget ? '·选一道友：' : ''}
      </p>
      {needsTarget && !skill.used ? (
        <div className="mt-2 flex flex-wrap gap-2">
          {seats.map((s) => (
            <button
              key={s.id}
              type="button"
              className={`btn-ghost text-xs ${target === s.id ? 'border-accent text-accent' : ''}`}
              aria-pressed={target === s.id}
              onClick={() => onTarget(s.id)}
            >
              {s.roleName || s.displayName}
            </button>
          ))}
        </div>
      ) : null}
      {!skill.used ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <button
            type="button"
            className="btn-ghost text-xs"
            disabled={disabled}
            onClick={() => onUse(needsTarget ? target : '')}
          >
            发动技能
          </button>
          {!skill.ready && skill.why ? <span className="text-[11px] text-slate-500">{skill.why}</span> : null}
        </div>
      ) : null}
    </div>
  )
}

function Transcript({
  lines,
  selfSeatId,
  onReply,
  onAdvance,
}: {
  lines: Line[]
  selfSeatId: string
  onReply?: (seatId: string) => void
  /** Host only: put the next-phase action on the line that waits for it. */
  onAdvance?: () => void
}) {
  const bottomRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' })
  }, [lines.length])
  // Only the newest waiting line carries the button: the older ones are history,
  // and a transcript full of live buttons would be unusable. Free discussion is
  // waiting in exactly the same sense — it never ends on its own — so its
  // opening line is the second place the host needs this action.
  const advanceAt = onAdvance
    ? lines.reduce((at, l, i) => (l.kind === 'system' && isHostWait(l.text) ? i : at), -1)
    : -1
  return (
    <div className="scroll-thin flex-1 space-y-2 overflow-y-auto pr-1">
      {lines.length === 0 ? <Empty>对局刚开始，等待主持人开场。</Empty> : null}
      {lines.map((line, index) => {
        const style = styleOf(line.kind)
        const tone =
          style.tone === 'phase'
            ? 'border-accent/40 bg-accent/10 text-accent'
            : style.tone === 'dm'
              ? 'border-amber-500/30 bg-amber-500/5 text-amber-100'
              : style.tone === 'reveal'
                ? 'border-rose-500/40 bg-rose-500/10 text-rose-100'
                : style.tone === 'clue'
                  ? 'border-emerald-500/30 bg-emerald-500/5 text-emerald-100'
                  : style.tone === 'system'
                    ? 'border-line/60 text-slate-400'
                    : style.tone === 'stream'
                      ? 'border-accent/30 bg-accent/5 text-slate-200'
                      : style.tone === 'whisper'
                        ? 'border-dashed border-line bg-ink/45 text-slate-300'
                        : 'border-line/70'
        // Only a spoken line can be answered, and never your own.
        const canReply = !!onReply && line.kind === 'speech' && !!line.seatId && line.seatId !== selfSeatId
        return (
          <div key={line.key} className={`rounded-xl border px-3 py-2 text-sm ${tone}`}>
            <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-slate-500">
              <span>{style.label}</span>
              {line.speaker && line.speaker !== style.label ? <span className="text-slate-300">{line.speaker}</span> : null}
              {line.replyTo ? <span className="normal-case text-accent/80">回应 {line.replyTo}</span> : null}
              {line.streaming ? <span className="text-accent">正在输入…</span> : null}
              {canReply ? (
                <button
                  type="button"
                  className="ml-auto rounded-md border border-line px-2 py-0.5 text-[11px] normal-case text-slate-300 hover:border-accent/60 hover:text-strong"
                  onClick={() => onReply?.(line.seatId ?? '')}
                >
                  回应
                </button>
              ) : null}
            </div>
            <p className="whitespace-pre-wrap break-words leading-relaxed">{line.text}</p>
            {index === advanceAt ? (
              <button type="button" className="btn-primary mt-2 text-xs" onClick={onAdvance}>
                进入下一阶段
              </button>
            ) : null}
          </div>
        )
      })}
      <div ref={bottomRef} />
    </div>
  )
}

/**
 * "某某正在思考" with the bouncing dots.
 *
 * The engine publishes a transient marker before and after each model call, so
 * the table is never silent for the two or three seconds a model takes. The
 * local tick also expires a marker whose stop frame never arrived.
 */
const THINKING_TTL = 90_000

function ThinkingBar({ visible }: { visible: Visible }) {
  const thinking = useGame((s) => s.thinking)
  const [now, setNow] = useState(() => Date.now())
  const ids = useMemo(() => Object.keys(thinking), [thinking])

  useEffect(() => {
    if (ids.length === 0) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [ids.length])

  const live = ids.filter((id) => now - (thinking[id] ?? 0) < THINKING_TTL)
  if (live.length === 0) return null
  const names = live.map((id) => seatName(visible, id))
  const label = names.length === 1 ? `${names[0]} 正在思考` : `${names.join('、')} 正在思考`
  return (
    <div className="mt-3 flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/5 px-3 py-2 text-xs text-accent">
      <span className="flex items-end gap-0.5" aria-hidden="true">
        <span className="thinking-dot h-1.5 w-1.5 rounded-full bg-current" />
        <span className="thinking-dot h-1.5 w-1.5 rounded-full bg-current" />
        <span className="thinking-dot h-1.5 w-1.5 rounded-full bg-current" />
      </span>
      <span>{label}…</span>
    </div>
  )
}

function PhaseActions({
  state,
  send,
  seatId,
  isHost,
  onReplyPass,
}: {
  state: SeatState
  send: (cmd: SendCmd) => boolean
  seatId: string
  isHost: boolean
  onReplyPass: (seatId: string) => void
}) {
  const phase = state.visible.phase
  const options = state.search ?? []
  const myTurn = state.floor?.seatId === seatId
  // 行动点本（K6 仙骨）：余量、名字都跟着本子走，没有这本子就整块不出现。
  const points = state.points ?? 0
  const pointsLabel = state.pointsLabel || '行动点'
  const hasPoints = state.pointsLabel !== '' || points > 0
  const skill = state.skill
  const [skillTarget, setSkillTarget] = useState('')
  // 分类抽卡时把规律写在面板上，否则玩家只看到「还剩 3 次」却不知下一张是哪类。
  const drawPlan = (phase?.draws ?? []).map((d) => `${d.kind || '任意线索'} ${d.count} 张`).join(' + ')
  const voteTargets = useMemo(
    () => (state.visible.seats ?? []).filter((s) => s.roleId && s.id !== seatId && s.kind !== 'spectator'),
    [state.visible.seats, seatId],
  )

  if (phase?.type === 'search' && phase.searchMode === 'random') {
    const left = state.draw ?? 0
    // 底卡写着「可获得曲径通幽 N」时，那一张不占本轮 2/4/6 张，也不在牌堆里：
    // 轮到自己时直接点名取，次数抽完了也能取。
    const granted = options.filter((o) => o.granted)
    return (
      <div className="space-y-3">
        <div className="rounded-xl border border-line bg-panel p-3">
          <p className="mb-2 text-xs text-slate">
            搜证阶段（随机抽取）：轮到你抽一次，抽到什么算什么。
            {phase.revealOnFind ? '本轮搜到的线索当场公开。' : ''}
          </p>
          {drawPlan ? <p className="mb-2 text-xs text-slate">本轮抽卡：{drawPlan}</p> : null}
          {hasPoints ? (
            <p className="mb-2 text-xs text-warm">
              你还剩 {points} 根{pointsLabel}·每抽一张花 1 根
            </p>
          ) : null}
          {granted.length > 0 ? (
            <div className="mb-2 space-y-2 rounded-lg border border-line bg-panel p-2">
              <p className="text-xs text-warm">
                你手里的线索写着「可获得曲径通幽」：可以点名取下面这张，不占本轮次数，也不必等自己那一手。
              </p>
              <div className="flex flex-wrap gap-2">
                {granted.map((o) => (
                  <button
                    key={o.id}
                    type="button"
                    className="btn-ghost text-xs"
                    onClick={() => send({ type: 'search.clue', clueId: o.id })}
                  >
                    {o.hint || '曲径通幽'}
                  </button>
                ))}
              </div>
            </div>
          ) : null}
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              className="btn-primary text-xs"
              disabled={!myTurn || left <= 0}
              onClick={() => send({ type: 'search.draw' })}
            >
              随机抽取
            </button>
            <button
              type="button"
              className="btn-ghost text-xs"
              disabled={!myTurn}
              onClick={() => send({ type: 'search.pass' })}
            >
              结束本轮
            </button>
            <span className="text-xs text-slate-400">
              {left <= 0
                ? '你本轮的抽取次数已用完，等待阶段推进。'
                : myTurn
                  ? `轮到你，还剩 ${left} 次`
                  : '还没轮到你，按本阶段顺序依次抽取。'}
            </span>
          </div>
        </div>
        {skill && !skill.used ? (
          <SkillCard
            skill={skill}
            points={points}
            pointsLabel={pointsLabel}
            seats={(state.visible.seats ?? []).filter(
              (s) => s.id !== seatId && s.roleId && s.kind !== 'spectator',
            )}
            target={skillTarget}
            onTarget={setSkillTarget}
            onUse={(targetId) => send({ type: 'skill.use', targetId })}
          />
        ) : null}
      </div>
    )
  }

  if (phase?.type === 'search') {
    return (
      <div className="space-y-3">
        <div className="rounded-xl border border-line bg-panel p-3">
          <p className="mb-2 text-xs text-slate">
            搜证阶段：{myTurn ? '选一个位置翻查，背面写着那里有什么' : '还没轮到你，按阶段顺序依次搜证。'}
            {phase.revealOnFind ? '本轮搜到的线索当场公开。' : ''}
          </p>
          {drawPlan ? <p className="mb-2 text-xs text-slate">本轮抽卡：{drawPlan}</p> : null}
          {hasPoints ? (
            <p className="mb-2 text-xs text-warm">
              你还剩 {points} 根{pointsLabel} · 每抽一张花 1 根
            </p>
          ) : null}
          <div className="flex flex-wrap gap-2">
            {options.length === 0 ? (
              <span className="text-xs text-slate-500">本轮线索已被搜完，等待阶段推进。</span>
            ) : (
              options.map((o) => (
                <button
                  key={o.id}
                  type="button"
                  className="btn-ghost text-xs"
                  title={o.granted ? '底卡写着可获得它：不占本轮次数' : undefined}
                  disabled={!myTurn && !o.granted}
                  onClick={() => send({ type: 'search.clue', clueId: o.id })}
                >
                  {o.hint || '未标注位置'}
                </button>
              ))
            )}
            {myTurn ? (
              <button type="button" className="btn-ghost text-xs" onClick={() => send({ type: 'search.pass' })}>
                结束本轮
              </button>
            ) : null}
          </div>
        </div>
        {skill && !skill.used ? (
          <SkillCard
            skill={skill}
            points={points}
            pointsLabel={pointsLabel}
            seats={(state.visible.seats ?? []).filter(
              (s) => s.id !== seatId && s.roleId && s.kind !== 'spectator',
            )}
            target={skillTarget}
            onTarget={setSkillTarget}
            onUse={(targetId) => send({ type: 'skill.use', targetId })}
          />
        ) : null}
      </div>
    )
  }

  if (phase?.type === 'vote') {
    const spectator = state.visible.self.kind === 'spectator'
    return (
      <div className="rounded-xl border border-rose-500/30 bg-rose-500/5 p-3">
        <p className="mb-2 text-xs text-rose-200">
          {state.voted ? '你已投票，等待其他玩家…' : '投票阶段：选出你认为的凶手，只能投一次'}
        </p>
        {!state.voted && !spectator ? (
          <div className="flex flex-wrap gap-2">
            {voteTargets.map((s) => (
              <button
                key={s.id}
                type="button"
                className="btn-ghost text-xs"
                onClick={() => send({ type: 'vote.cast', roleId: s.roleId })}
              >
                投给 {s.roleName || s.displayName}
              </button>
            ))}
            <button type="button" className="btn-ghost text-xs" onClick={() => send({ type: 'vote.cast' })}>
              弃权
            </button>
          </div>
        ) : null}
        <p className="mt-2 text-xs text-slate-500">已投票 {state.voteCount} 人 · 房主/观战者可以看到计票</p>
      </div>
    )
  }

  if (phase?.type === 'discussion') {
    const queued = (state.queue ?? []).findIndex((s) => s.seatId === seatId)
    return (
      <div className="space-y-2 rounded-xl border border-accent/25 bg-accent/5 p-3">
        <p className="text-xs text-accent">
          {myTurn
            ? state.floor?.kind === 'reply'
              ? `轮到你先回应${state.floor?.replyTo ? ` ${seatName(state.visible, state.floor.replyTo)}` : ''}：回答或直接说「不回应」。`
              : '麦克风在你手上：可以发言，或点「结束发言」交给下一位。'
            : state.talkOpen
              ? '自由讨论：想说话就排队，同一时刻只有一个人占麦。'
              : `讨论阶段：${orderSentence(phase.order)}`}
        </p>
        {state.talkOpen || myTurn ? (
          <div className="flex flex-wrap gap-2">
            {myTurn ? (
              <>
                <button type="button" className="btn-ghost text-xs" onClick={() => send({ type: 'speak.pass' })}>
                  {state.floor?.kind === 'reply' ? '不回应' : state.talkOpen ? '不发言' : '结束发言'}
                </button>
                {state.floor?.replyTo ? (
                  <button type="button" className="btn-ghost text-xs" onClick={() => onReplyPass(state.floor?.replyTo ?? '')}>
                    回应 {seatName(state.visible, state.floor?.replyTo ?? '')}
                  </button>
                ) : null}
              </>
            ) : (
              <button
                type="button"
                className="btn-primary text-xs"
                disabled={queued >= 0}
                onClick={() => send({ type: 'speak.request' })}
              >
                {queued >= 0 ? `已排队（第 ${queued + 1} 位）` : '我要发言'}
              </button>
            )}
            {isHost ? <span className="self-center text-[11px] text-slate-500"> 主持人可在发言记录里点「进入下一阶段」。</span> : null}
          </div>
        ) : null}
        <QueueList state={state} seatId={seatId} />
      </div>
    )
  }

  if (phase?.type === 'reading') {
    return (
      <div className="space-y-2">
        <p className="text-xs text-slate-500">
          点右上角「阅读剧本」读完自己的角色本，读完由主持人推进。
        </p>
        {isHost ? (
          <button type="button" className="btn-primary text-xs" onClick={() => send({ type: 'host.skip' })}>
            进入下一阶段
          </button>
        ) : null}
      </div>
    )
  }

  if (phase?.type === 'opening') {
    // 开场不是过场：骰子提问一类的互动都发生在这里，服务端会一直保留本阶段，
    // 直到主持人点「进入下一阶段」。
    return (
      <div className="space-y-2">
        <p className="text-xs text-slate-500">开场互动中：可以自由发言；主持人准备好后进入下一阶段。</p>
        {isHost ? (
          <button type="button" className="btn-primary text-xs" onClick={() => send({ type: 'host.skip' })}>
            进入下一阶段
          </button>
        ) : null}
      </div>
    )
  }

  if (phase?.type === 'reveal') {
    return <p className="text-xs text-slate-500">由主持人推进，你可以继续补充发言。</p>
  }

  return null
}

/**
 * Who holds the microphone and who is waiting for it. The list is the engine's
 * queue: only the head speaks, so it doubles as the floor's explanation.
 */
function QueueList({ state, seatId }: { state: SeatState; seatId: string }) {
  const queue = state.queue ?? []
  const floor = state.floor
  if (!state.talkOpen && queue.length === 0) return null
  return (
    <div className="space-y-1 text-[11px] text-slate-400">
      {floor ? (
        <p>
          <span className="text-slate-500">现在：</span>
          <span className="text-accent">{seatName(state.visible, floor.seatId)}</span>
          {floor.kind === 'reply' && floor.replyTo
            ? `（回应 ${seatName(state.visible, floor.replyTo)}）`
            : ''}
        </p>
      ) : null}
      {queue.length > 0 ? (
        <ol className="flex flex-wrap gap-x-3 gap-y-1">
          {queue.map((slot, i) => (
            <li key={`${slot.seatId}-${i}`} className="flex items-center gap-1">
              <span className="text-slate-600">{i + 1}.</span>
              <span className={slot.seatId === seatId ? 'text-accent' : ''}>{seatName(state.visible, slot.seatId)}</span>
              <span className="text-slate-600">{slotLabel(slot, state.visible)}</span>
            </li>
          ))}
        </ol>
      ) : floor ? null : (
        <p className="text-slate-600">麦克风空闲，谁先开口谁先讲。</p>
      )}
    </div>
  )
}

function slotLabel(slot: SpeechSlot, visible: Visible): string {
  if (slot.kind === 'reply' && slot.replyTo) return `回应 ${seatName(visible, slot.replyTo)}`
  if (slot.note) return slot.note
  if (slot.kind === 'turn') return '按顺序'
  return '想发言'
}

/**
 * Host-assigned turn order. The phase stays blocked until the host submits a
 * full permutation, so this panel sits right above the chat box.
 */
function OrderPanel({
  prompt,
  send,
}: {
  prompt: TurnPrompt
  send: (cmd: { type: string; seatIds?: string[] }) => boolean
}) {
  const seats = prompt.seats
  const [picked, setPicked] = useState<string[]>([])

  // A new phase means a new layout: never carry the previous round's picks.
  useEffect(() => {
    setPicked([])
  }, [prompt.phaseId])

  const nameOf = (seatId: string) => {
    const seat: TurnOption | undefined = seats.find((s) => s.seatId === seatId)
    return seat?.role || seat?.display || seatId
  }
  const toggle = (seatId: string) =>
    setPicked((prev) => (prev.includes(seatId) ? prev.filter((id) => id !== seatId) : [...prev, seatId]))
  const shuffle = () => {
    const next = seats.map((s) => s.seatId)
    for (let i = next.length - 1; i > 0; i -= 1) {
      const j = Math.floor(Math.random() * (i + 1))
      ;[next[i], next[j]] = [next[j], next[i]]
    }
    setPicked(next)
  }
  const complete = seats.length > 0 && picked.length === seats.length

  return (
    <div className="rounded-xl border border-amber-500/40 bg-amber-500/5 p-3">
      <p className="mb-2 text-xs text-amber-100">
        {prompt.text || '本阶段顺序由你指定。'}按顺序点选玩家，全部排好后点「确定顺序」。
      </p>
      <div className="mb-2 flex flex-wrap gap-2">
        {seats.map((s) => {
          const pos = picked.indexOf(s.seatId)
          return (
            <button
              key={s.seatId}
              type="button"
              className={`btn-ghost text-xs ${pos >= 0 ? 'border-amber-400/60 text-amber-100' : ''}`}
              onClick={() => toggle(s.seatId)}
            >
              {pos >= 0 ? `${pos + 1}. ` : ''}
              {s.role || s.display}
              {s.kind === 'agent' ? ' · AI' : ''}
            </button>
          )
        })}
      </div>
      <p className="mb-2 text-[11px] text-slate-400">
        {picked.length > 0 ? `当前顺序：${picked.map(nameOf).join(' → ')}` : '还没排任何人，点上面的玩家开始排。'}
      </p>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          className="btn-primary text-xs"
          disabled={!complete}
          onClick={() => send({ type: 'order.set', seatIds: picked })}
        >
          确定顺序{complete ? '' : `（还差 ${seats.length - picked.length} 人）`}
        </button>
        <button type="button" className="btn-ghost text-xs" onClick={() => setPicked(seats.map((s) => s.seatId))}>
          按入座顺序
        </button>
        <button type="button" className="btn-ghost text-xs" onClick={shuffle}>
          随机打乱
        </button>
        <button
          type="button"
          className="btn-ghost text-xs"
          disabled={picked.length === 0}
          onClick={() => setPicked((prev) => prev.slice(0, -1))}
        >
          撤销
        </button>
      </div>
    </div>
  )
}

function RolePanel({
  visible,
  onReveal,
  onRead,
}: {
  visible: Visible
  onReveal: (clueId: string) => void
  onRead: () => void
}) {
  const role = visible.ownRole
  return (
    <aside className="order-2 space-y-4 xl:order-none">
      <div className="sheet">
        <SectionTitle
          aside={
            <span className="flex items-center gap-2">
              {role ? <Tag tone="blue">{role.name}</Tag> : null}
              <Hint align="right" text="全屏阅读：剧本里的加粗、示意图与时间线会照原作样式排版。" />
            </span>
          }
        >
          我的角色本
        </SectionTitle>
        {role ? (
          <div className="space-y-3 text-sm">
            <button type="button" className="btn-primary w-full text-sm" onClick={onRead}>
              阅读剧本
            </button>
            {role.private.goals.length > 0 ? (
              <div>
                <p className="label">目标</p>
                <ul className="list-disc pl-4 text-xs text-slate-400">
                  {role.private.goals.map((g) => (
                    <li key={g}>{g}</li>
                  ))}
                </ul>
              </div>
            ) : null}
            {role.private.secrets.length > 0 ? (
              <div>
                <p className="label">你的秘密（可以选择隐瞒）</p>
                <ul className="space-y-1 text-xs text-slate-400">
                  {role.private.secrets.map((s) => (
                    <li key={s.id} className="rounded-lg border border-line/60 px-2 py-1">
                      {s.text}
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
          </div>
        ) : (
          <p className="text-sm text-slate-500">观战视角：你没有角色本，只会看到公共信息。</p>
        )}
      </div>

      <div className="sheet">
        <SectionTitle>我的线索</SectionTitle>
        {/* Items come from the cards this seat has already picked up. They are
            the keys to the deep-dive clues, so they sit above the list. */}
        {visible.items.length > 0 ? (
          <div className="mb-3 flex flex-wrap items-center gap-1.5">
            <span className="text-xs text-slate-500">身上物品</span>
            {visible.items.map((item) => (
              <span key={item} className="chip">
                {item}
              </span>
            ))}
          </div>
        ) : null}
        {visible.ownClues.length === 0 ? (
          <p className="text-xs text-slate-500">还没有私密线索，搜证阶段再来看。</p>
        ) : (
          <ul className="space-y-2 text-xs">
            {visible.ownClues.map((c) => (
              <li key={c.id} className="rounded-xl border border-line/70 px-3 py-2">
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <p className="text-sm text-slate-200">{c.title}</p>
                    {c.location ? <p className="text-slate-500">{c.location}</p> : null}
                  </div>
                  <button type="button" className="btn-ghost shrink-0 text-[11px]" onClick={() => onReveal(c.id)}>
                    公开
                  </button>
                </div>
                {/* Clue prose is authored in the studio and may carry markdown and a
                    picture: a scan of the letter *is* the clue. */}
                <div className="mt-1">
                  <Markdown text={c.content} />
                  {c.imageUrl ? (
                    <a href={c.imageUrl} target="_blank" rel="noreferrer">
                      <img
                        src={c.imageUrl}
                        alt={c.title}
                        loading="lazy"
                        className="mt-2 max-h-64 w-auto rounded-lg border border-line/70 bg-panel"
                      />
                    </a>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="sheet">
        <SectionTitle>已公开线索</SectionTitle>
        {visible.publicClues.length === 0 ? (
          <p className="text-xs text-slate-500">还没有公开线索。</p>
        ) : (
          <ul className="space-y-2 text-xs">
            {visible.publicClues.map((c) => (
              <li key={c.id} className="rounded-xl border border-emerald-500/30 px-3 py-2">
                <p className="text-sm text-emerald-100">{c.title}</p>
                <div className="mt-1">
                  <Markdown text={c.content} />
                  {c.imageUrl ? (
                    <a href={c.imageUrl} target="_blank" rel="noreferrer">
                      <img
                        src={c.imageUrl}
                        alt={c.title}
                        loading="lazy"
                        className="mt-2 max-h-64 w-auto rounded-lg border border-line/70 bg-panel"
                      />
                    </a>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      {visible.ownFacts.length > 0 ? (
        <div className="sheet">
          <SectionTitle>你确定的事实</SectionTitle>
          <ul className="list-disc space-y-1 pl-4 text-xs text-slate-400">
            {visible.ownFacts.map((f) => (
              <li key={f.id}>{f.text}</li>
            ))}
          </ul>
        </div>
      ) : null}
    </aside>
  )
}

function VoiceHint({ caps }: { caps: VoiceCapabilities }) {
  const silent = caps.tts && caps.mandarinVoices === 0 && !caps.fallback
  const detail = silent
    ? '这台机器的朗读引擎没有列出普通话音色，所以朗读不会发声（装好普通话语音包后刷新页面即可）。'
    : !caps.tts
      ? '这个浏览器没有朗读引擎。'
      : caps.fallback
        ? '浏览器没有列出任何音色（手机端很常见），朗读会交给系统默认的普通话引擎；如果听着不像普通话，请在系统设置里把中文语音包设为默认。'
        : `朗读只用普通话（zh-CN）音色，粤语与台湾腔会被跳过；当前有 ${caps.mandarinVoices} 个普通话音色，每个角色分到不同音色。`
  return (
    <p className="flex flex-wrap items-center gap-1.5 text-xs text-slate-500">
      <span>语音输入用键盘自带的听写</span>
      <Hint
        label="听写与朗读说明"
        text={`手机输入法的麦克风键、Windows 的 Win+H、macOS 连按 Fn，识别结果直接落进输入框，改完再发；Enter 发送，Shift+Enter 换行。${detail}`}
      />
      {silent ? <span className="text-amber-300">· 无普通话音色</span> : null}
      {caps.fallback ? <span className="text-slate-400">· 系统音色</span> : null}
    </p>
  )
}

function voiceTitle(caps: VoiceCapabilities): string {
  if (!caps.tts) return '这个浏览器没有朗读引擎'
  if (caps.mandarinVoices > 0) return '朗读主持人台词、发言与线索'
  if (caps.fallback) return '未读到音色列表，按系统默认普通话朗读'
  return '系统里没有普通话音色，朗读不会发声'
}

function voiceLabel(caps: VoiceCapabilities, on: boolean): string {
  if (!caps.tts) return '🔇 无朗读'
  if (caps.mandarinVoices > 0) return on ? '🔊 朗读开' : '🔈 朗读关'
  if (caps.fallback) return on ? '🔊 系统音色' : '🔈 朗读关'
  return '🔇 无音色'
}

function roleNameOf(visible: Visible, roleId: string): string {
  // An empty target is an abstention, not a missing role.
  if (!roleId) return '弃权'
  const seat = (visible.seats ?? []).find((s) => s.roleId === roleId)
  return seat?.roleName || seat?.displayName || roleId
}

function seatName(visible: Visible, seatId: string): string {
  const seat = (visible.seats ?? []).find((s) => s.id === seatId)
  return seat?.roleName || seat?.displayName || seatId
}

/** Short tag for the phase's turn-order policy. */
function orderLabel(order?: string): string {
  switch (order) {
    case 'random':
      return '顺序随机'
    case 'host_assigned':
      return '主持人定序'
    default:
      return '按入座轮流'
  }
}

/** The same policy, phrased for the action hint under the transcript. */
function orderSentence(order?: string): string {
  switch (order) {
    case 'random':
      return '顺序在阶段开始时随机排定，按那个顺序发言。'
    case 'host_assigned':
      return '顺序由主持人在阶段开始时排定，按那个顺序发言。'
    default:
      return '按入座顺序轮流发言。'
  }
}
