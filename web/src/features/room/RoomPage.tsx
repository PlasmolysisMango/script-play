import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { Empty, Field, SectionTitle, Spinner, Tag } from '../../components/ui'
import { isUnlimitedSpeech, speechLimitLabel } from '../../lib/speech'
import type { Room, Seat } from '../../types/schema'

/** Pseudo persona: the server rolls a concrete preset when the seat is created. */
const RANDOM_PERSONA = { id: 'random', name: '随机', style: '入座时随机抽一种性格' }

/**
 * Lobby of one room: seat grid, host controls and the join form.
 *
 * Role assignment happens here (never mid-game), which is what lets the server
 * bind every socket to exactly one role.
 */
export function RoomPage({ code }: { code: string }) {
  const navigate = useApp((s) => s.navigate)
  const notify = useApp((s) => s.notify)
  const queryClient = useQueryClient()

  const roomQuery = useQuery({ queryKey: ['room', code], queryFn: () => api.getRoom(code), refetchInterval: 4000 })
  const room = roomQuery.data
  const personas = room?.personas ?? []

  const applyRoom = (next: Room) => {
    queryClient.setQueryData(['room', code], next)
    queryClient.invalidateQueries({ queryKey: ['rooms'] })
  }
  const onError = (err: unknown) => notify(err instanceof ApiError ? err.message : String(err), 'error')

  const join = useMutation({
    mutationFn: (body: { displayName?: string; roleId?: string; personaId?: string; spectator?: boolean }) =>
      api.joinRoom(code, body),
    onSuccess: (next) => {
      applyRoom(next)
      notify(next.mySeatId ? '已入座' : '已进入房间')
    },
    onError,
  })
  const addSeat = useMutation({
    mutationFn: (body: { roleId?: string; personaId?: string; displayName?: string }) => api.addSeat(code, body),
    onSuccess: applyRoom,
    onError,
  })
  const patchSeat = useMutation({
    mutationFn: ({ seatId, body }: { seatId: string; body: Parameters<typeof api.patchSeat>[2] }) =>
      api.patchSeat(code, seatId, body),
    onSuccess: applyRoom,
    onError,
  })
  const removeSeat = useMutation({
    mutationFn: (seatId: string) => api.deleteSeat(code, seatId),
    onSuccess: applyRoom,
    onError,
  })
  const start = useMutation({
    mutationFn: () => api.startRoom(code),
    onSuccess: (next) => {
      applyRoom(next)
      navigate('game', code)
    },
    onError,
  })

  if (roomQuery.isLoading) return <Spinner label="正在读取房间…" />
  if (!room) return <Empty>房间不存在，或你的账号无权查看。</Empty>

  const freeRoles = room.roles.filter((r) => !r.taken)
  const mySeat = room.seats.find((s) => s.id === room.mySeatId)
  const players = room.seats.filter((s) => s.kind !== 'spectator')
  const freeSeats = Math.max(0, room.maxSeats - players.length)
  // Only as many roles can be auto-assigned as there are empty seats.
  const unclaimed = Math.min(freeRoles.length, freeSeats)
  const canAddAgent = room.host && room.status === 'lobby' && players.length < room.maxSeats
  const canStart = room.host && room.status === 'lobby' && players.length >= 2

  return (
    <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_340px]">
      <section className="space-y-5">
        <div className="card">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0">
              <p className="text-xs uppercase tracking-widest text-slate-500">房间号</p>
              <p className="font-mono text-2xl tracking-[0.3em] text-accent">{room.code}</p>
              <h1 className="mt-1 text-lg font-semibold">{room.name || room.scriptTitle || '未命名房间'}</h1>
              <p className="text-xs text-slate-500">
                剧本：{room.scriptTitle || '未知'} · {players.length}/{room.maxSeats} 座 ·{' '}
                {room.allowSpectator ? '允许观战' : '禁止观战'} · 发言限时{' '}
                {speechLimitLabel(room.speechSeconds)}
                {isUnlimitedSpeech(room.speechSeconds) ? '' : `（${room.autoSkip ? '超时自动跳过' : '等主持人推进'}）`}
              </p>
            </div>
            {/* Status, host badge and copy used to be three stacked rows on a
                phone; side by side they cost one line. */}
            <div className="flex flex-wrap items-center gap-2">
              <Tag tone={room.status === 'running' ? 'green' : room.status === 'finished' ? 'slate' : 'blue'}>
                {room.status === 'running' ? '对局进行中' : room.status === 'finished' ? '已结束' : '等待开局'}
              </Tag>
              {room.host ? <Tag tone="amber">我是主持人</Tag> : mySeat ? <Tag>我的座位：{mySeat.displayName}</Tag> : null}
              <button
                type="button"
                className="btn-ghost px-3 text-xs"
                onClick={() => {
                  void navigator.clipboard?.writeText(room.code)
                  notify('房间号已复制，发给朋友一起入座')
                }}
              >
                复制房间号
              </button>
            </div>
          </div>

          {room.status !== 'lobby' ? (
            <div className="mt-4 flex flex-wrap items-center gap-2 rounded-xl border border-line/70 bg-ink/40 px-3 py-2 text-sm">
              <span className="text-slate-400">
                {room.status === 'running' ? '对局已经开始，只有观战者可以后入场。' : '本局已结束。'}
              </span>
              {mySeat ? (
                <button type="button" className="btn-primary text-xs" onClick={() => navigate('game', code)}>
                  进入牌桌
                </button>
              ) : null}
              {room.status === 'finished' && room.host ? (
                <button type="button" className="btn-ghost text-xs" onClick={() => navigate('replay', code)}>
                  查看复盘
                </button>
              ) : null}
            </div>
          ) : null}
        </div>

        <div className="sheet">
          <SectionTitle aside={<span className="text-xs text-slate-500">座位身份由连接绑定，发言归属不靠文本推断</span>}>
            座位（{room.seats.length}）
          </SectionTitle>
          <div className="grid gap-3 md:grid-cols-2">
            {room.seats.map((seat) => (
              <SeatCard
                key={seat.id}
                room={room}
                seat={seat}
                personas={personas}
                onPatch={(body) => patchSeat.mutate({ seatId: seat.id, body })}
                onRemove={() => removeSeat.mutate(seat.id)}
              />
            ))}
            {Array.from({ length: Math.max(0, room.maxSeats - players.length) }).map((_, i) => (
              <div key={`empty-${i}`} className="rounded-xl border border-dashed border-line/60 p-3 text-xs text-slate-600">
                空座位 · 等待真人加入或由主持人补 AI
              </div>
            ))}
          </div>

          {canAddAgent ? (
            <AddAgentForm
              freeRoles={freeRoles}
              personas={personas}
              busy={addSeat.isPending}
              onSubmit={(body) => addSeat.mutate(body)}
            />
          ) : null}

          {room.host && room.status === 'lobby' ? (
            <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line/60 pt-4">
              <button
                type="button"
                className="btn-primary"
                disabled={!canStart || start.isPending}
                onClick={() => start.mutate()}
              >
                {start.isPending ? '开局中…' : '开始游戏'}
              </button>
              <span className="text-xs text-slate-500">
                {players.length < 2
                  ? '至少需要 2 个玩家座位（真人或 AI）'
                  : unclaimed > 0
                    ? `还有 ${unclaimed} 个角色没人认领（空座 ${freeSeats} 个），开局时会按加入顺序自动分配`
                    : '所有角色已就位'}
              </span>
            </div>
          ) : null}
        </div>
      </section>

      <aside className="space-y-5">
        {!room.mySeatId ? (
          <JoinCard
            personas={personas}
            freeRoles={freeRoles}
            allowSpectator={room.allowSpectator}
            lobby={room.status === 'lobby'}
            busy={join.isPending}
            onJoin={(body) => join.mutate(body)}
          />
        ) : (
          <div className="card space-y-3">
            <SectionTitle>我的座位</SectionTitle>
            <p className="text-sm">
              {mySeat?.displayName} · {mySeat?.kind === 'spectator' ? '观战' : '玩家'}
            </p>
            <p className="text-xs text-slate-500">
              角色：{mySeat?.roleName || '未分配'}
              {mySeat?.kind === 'spectator'
                ? ' · 观战没有角色本'
                : mySeat?.kind === 'agent'
                  ? ` · 性格：${personas.find((p) => p.id === mySeat?.personaId)?.name ?? '默认'}`
                  : ''}
            </p>
            {mySeat && mySeat.kind !== 'spectator' && room.status === 'lobby' ? (
              <Field label="更换角色" hint="同一角色只能被一个座位占用">
                <select
                  className="input"
                  value={mySeat.roleId}
                  onChange={(e) => patchSeat.mutate({ seatId: mySeat.id, body: { roleId: e.target.value } })}
                >
                  <option value="">（开局自动分配）</option>
                  {room.roles
                    .filter((r) => !r.taken || r.id === mySeat.roleId)
                    .map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.name}
                      </option>
                    ))}
                </select>
              </Field>
            ) : null}
            {mySeat && mySeat.kind !== 'spectator' ? (
              <button
                type="button"
                className={mySeat.ready ? 'btn-ghost w-full' : 'btn-primary w-full'}
                onClick={() => patchSeat.mutate({ seatId: mySeat.id, body: { ready: !mySeat.ready } })}
                disabled={room.status !== 'lobby'}
              >
                {mySeat.ready ? '取消准备' : '我准备好了'}
              </button>
            ) : null}
            <button type="button" className="btn-ghost w-full" onClick={() => navigate('lobby')}>
              回到大厅
            </button>
          </div>
        )}

        <div className="sheet">
          <SectionTitle>角色卡</SectionTitle>
          <ul className="space-y-2 text-xs">
            {room.roles.map((r) => (
              <li key={r.id} className="rounded-xl border border-line/60 px-3 py-2">
                <div className="flex items-center justify-between">
                  <span className="text-sm text-slate-200">{r.name}</span>
                  {r.taken ? <Tag tone="green">已占</Tag> : <Tag>空闲</Tag>}
                </div>
                {r.intro ? <p className="mt-1 leading-relaxed text-slate-500">{r.intro}</p> : null}
              </li>
            ))}
          </ul>
        </div>
      </aside>
    </div>
  )
}

function SeatCard({
  room,
  seat,
  personas,
  onPatch,
  onRemove,
}: {
  room: Room
  seat: Seat
  personas: { id: string; name: string; style: string }[]
  onPatch: (body: { roleId?: string; personaId?: string; displayName?: string; ready?: boolean }) => void
  onRemove: () => void
}) {
  const editable = room.status === 'lobby'
  const hostEditable = room.host && editable
  const persona = personas.find((p) => p.id === seat.personaId)
  return (
    <div className={`rounded-xl border p-3 ${seat.mine ? 'border-accent/60' : 'border-line/70'}`}>
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">
            {seat.displayName}
            {seat.isHost ? <span className="ml-2 text-xs text-amber-300">主持人</span> : null}
            {seat.mine ? <span className="ml-2 text-xs text-accent">我</span> : null}
          </p>
          <p className="text-xs text-slate-500">
            {seat.kind === 'agent' ? 'AI 座位' : seat.kind === 'spectator' ? '观战' : '真人'} ·{' '}
            {seat.roleName || '未分配角色'}
          </p>
        </div>
        <div className="flex flex-col items-end gap-1">
          {seat.ready ? <Tag tone="green">已准备</Tag> : <Tag>{seat.kind === 'agent' ? '由主持人控制' : '未准备'}</Tag>}
          {hostEditable && !seat.mine ? (
            <button type="button" className="text-xs text-rose-300 hover:text-rose-200" onClick={onRemove}>
              移除
            </button>
          ) : null}
        </div>
      </div>

      {hostEditable ? (
        <div className="mt-3 space-y-2">
          <select
            className="input text-xs"
            value={seat.roleId}
            onChange={(e) => onPatch({ roleId: e.target.value })}
            aria-label="角色"
          >
            <option value="">（开局自动分配）</option>
            {room.roles
              .filter((r) => !r.taken || r.id === seat.roleId)
              .map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
          </select>
          {seat.kind === 'agent' ? (
            <select
              className="input text-xs"
              value={seat.personaId}
              onChange={(e) => onPatch({ personaId: e.target.value })}
              aria-label="性格"
            >
              <option value={RANDOM_PERSONA.id}>
                {RANDOM_PERSONA.name} · {RANDOM_PERSONA.style}
              </option>
              {personas.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} · {p.style}
                </option>
              ))}
            </select>
          ) : null}
        </div>
      ) : (
        persona && seat.kind === 'agent' ? <p className="mt-2 text-xs text-slate-500">性格：{persona.name} · {persona.style}</p> : null
      )}
    </div>
  )
}

function AddAgentForm({
  freeRoles,
  personas,
  busy,
  onSubmit,
}: {
  freeRoles: { id: string; name: string }[]
  personas: { id: string; name: string; style: string }[]
  busy: boolean
  onSubmit: (body: { roleId?: string; personaId?: string; displayName?: string }) => void
}) {
  const [roleId, setRoleId] = useState('')
  const [personaId, setPersonaId] = useState(RANDOM_PERSONA.id)
  const chosen = useMemo(() => freeRoles.find((r) => r.id === roleId), [freeRoles, roleId])

  return (
    <div className="mt-4 rounded-xl border border-line/70 bg-ink/40 p-3">
      <p className="mb-2 text-xs font-medium uppercase tracking-wide text-slate-400">添加 AI 座位</p>
      <div className="grid gap-2 sm:grid-cols-2">
        <select className="input text-xs" value={roleId} onChange={(e) => setRoleId(e.target.value)} aria-label="AI 角色">
          <option value="">（随机空闲角色）</option>
          {freeRoles.map((r) => (
            <option key={r.id} value={r.id}>
              {r.name}
            </option>
          ))}
        </select>
        <select className="input text-xs" value={personaId} onChange={(e) => setPersonaId(e.target.value)} aria-label="AI 性格">
          <option value={RANDOM_PERSONA.id}>{RANDOM_PERSONA.name} · {RANDOM_PERSONA.style}</option>
          {personas.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      </div>
      <button
        type="button"
        className="btn-ghost mt-2 w-full text-xs"
        disabled={busy}
        onClick={() => {
          onSubmit({
            roleId: roleId || undefined,
            personaId: personaId || undefined,
            displayName: chosen ? `AI·${chosen.name}` : undefined,
          })
          setRoleId('')
        }}
      >
        {busy ? '添加中…' : '添加 AI 座位'}
      </button>
    </div>
  )
}

function JoinCard({
  freeRoles,
  personas,
  allowSpectator,
  lobby,
  busy,
  onJoin,
}: {
  freeRoles: { id: string; name: string; intro: string }[]
  personas: { id: string; name: string }[]
  allowSpectator: boolean
  lobby: boolean
  busy: boolean
  onJoin: (body: { displayName?: string; roleId?: string; personaId?: string; spectator?: boolean }) => void
}) {
  const [name, setName] = useState('')
  const [roleId, setRoleId] = useState('')
  const [personaId, setPersonaId] = useState(personas[0]?.id ?? '')

  return (
    <div className="card space-y-3">
      <SectionTitle>加入这个房间</SectionTitle>
      <Field label="昵称" hint="留空使用账号昵称">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="怎么称呼你" />
      </Field>
      {lobby ? (
        <>
          <Field label="想演的角色">
            <select className="input" value={roleId} onChange={(e) => setRoleId(e.target.value)}>
              <option value="">（开局自动分配）</option>
              {freeRoles.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="发言性格" hint="真人席位复用同一套性格提示，方便复盘">
            <select className="input" value={personaId} onChange={(e) => setPersonaId(e.target.value)}>
              {personas.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
        </>
      ) : null}
      <button
        type="button"
        className="btn-primary w-full"
        disabled={busy || (!lobby && !allowSpectator)}
        onClick={() =>
          onJoin({
            displayName: name.trim() || undefined,
            roleId: roleId || undefined,
            personaId: personaId || undefined,
            spectator: !lobby,
          })
        }
      >
        {lobby ? '占一个座位' : allowSpectator ? '以观战身份进入' : '对局已开始'}
      </button>
      <p className="text-xs text-slate-500">座位与这台设备绑定，断线回来仍是原座位。</p>
    </div>
  )
}
