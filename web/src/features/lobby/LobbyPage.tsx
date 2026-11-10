import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { Empty, Field, Hint, SectionTitle, Spinner, Tag } from '../../components/ui'
import { SPEECH_PRESETS, SPEECH_UNLIMITED, isUnlimitedSpeech, speechLimitLabel } from '../../lib/speech'
import type { Room } from '../../types/schema'

export function LobbyPage() {
  const navigate = useApp((s) => s.navigate)
  const notify = useApp((s) => s.notify)
  const isAdmin = useApp((s) => s.user?.role === 'admin')
  const queryClient = useQueryClient()

  const [name, setName] = useState('')
  const [scriptId, setScriptId] = useState('')
  const [maxSeats, setMaxSeats] = useState(4)
  const [allowSpectator, setAllowSpectator] = useState(true)
  const [speechSeconds, setSpeechSeconds] = useState(0)
  const [autoSkip, setAutoSkip] = useState(true)
  // 固定人数本（几乎全部正式剧本）min == max，建局人再占一座就少一个人：
  // 只当主持人时建局人以观战座位入房，把座位全部留给玩家。
  const [hostOnly, setHostOnly] = useState(false)
  const [joinCode, setJoinCode] = useState('')

  const health = useQuery({ queryKey: ['health'], queryFn: api.health })
  const scripts = useQuery({ queryKey: ['scripts'], queryFn: api.listScripts })
  const rooms = useQuery({ queryKey: ['rooms'], queryFn: api.listRooms, refetchInterval: 5000 })

  const published = useMemo(() => (scripts.data ?? []).filter((s) => s.status === 'published'), [scripts.data])
  // Three groups, so a finished room never reads as "进行中".
  const groups = useMemo(() => {
    const live = (rooms.data ?? []).filter((r) => r.status === 'running')
    const lobby = (rooms.data ?? []).filter((r) => r.status !== 'running' && r.status !== 'finished')
    const done = (rooms.data ?? []).filter((r) => r.status === 'finished')
    return [
      { key: 'lobby', title: '大厅中', hint: '还在等人，可以直接加入', rooms: lobby },
      { key: 'running', title: '进行中', hint: '对局已经开始，可加入观战', rooms: live },
      { key: 'finished', title: '已结束', hint: '可以看复盘', rooms: done },
    ]
  }, [rooms.data])
  const chosenId = scriptId || published[0]?.id || ''
  const chosen = useMemo(() => published.find((s) => s.id === chosenId), [published, chosenId])
  // The script decides the cast: a fixed count locks the room to exactly that
  // many seats, a flexible one only allows its advertised range. The server
  // rejects anything else, so the form mirrors the same rule.
  const seatMin = chosen?.playerCount?.min ?? 2
  const seatMax = chosen?.playerCount?.max ?? 12
  const seatFixed = seatMin === seatMax
  const speechDefault = health.data?.floorTimeoutSec ?? 90
  const speechUnlimited = isUnlimitedSpeech(speechSeconds)
  // The limit is per turn, not per round: every time a seat is handed the floor
  // the clock restarts, and the same value also bounds its search/vote waits.
  const speechHint = speechUnlimited
    ? '每次轮到某人都不计时，只有本人操作或主持人跳过后才推进'
    : `每次轮到该角色单独计时${speechSeconds ? `（${speechSeconds} 秒）` : `（服务端默认 ${speechDefault} 秒）`}，搜证与投票也用它`
  useEffect(() => {
    const clamped = Math.min(seatMax, Math.max(seatMin, maxSeats))
    if (clamped !== maxSeats) setMaxSeats(clamped)
  }, [seatMin, seatMax, maxSeats])

  const createRoom = useMutation({
    mutationFn: async () => {
      const chosen = scriptId || published[0]?.id
      if (!chosen) throw new Error('请先准备一个已发布的剧本')
      return api.createRoom({
        name: name.trim() || undefined,
        scriptId: chosen,
        maxSeats: seatFixed ? seatMax : maxSeats,
        allowSpectator,
        speechSeconds,
        autoSkip,
        hostOnly,
      })
    },
    onSuccess: (room) => {
      queryClient.invalidateQueries({ queryKey: ['rooms'] })
      navigate('room', room.code)
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : String(err), 'error'),
  })

  const seed = useMutation({
    mutationFn: api.seedSample,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['scripts'] })
      notify('示例剧本《雨夜灯塔》已就绪')
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : String(err), 'error'),
  })

  const join = useMutation({
    mutationFn: () => api.joinRoom(joinCode.trim().toUpperCase(), {}),
    onSuccess: (room) => navigate('room', room.code),
    onError: (err) => notify(err instanceof ApiError ? err.message : String(err), 'error'),
  })

  return (
    <div className="grid gap-5 lg:grid-cols-[1.4fr_1fr]">
      <section className="space-y-5">
        <div className="card space-y-4">
          <SectionTitle
            aside={
              <span className="hidden truncate text-xs text-slate-500 sm:inline">
                {health.data ? `provider: ${health.data.default}（${health.data.providers.join(' / ')}）` : '检测服务中…'}
              </span>
            }
          >
            房间（大厅 / 进行中 / 已结束）
          </SectionTitle>
          {rooms.isLoading ? <Spinner /> : null}
          {rooms.data && rooms.data.length === 0 ? <Empty>还没有房间，右侧开一局试试。</Empty> : null}
          {groups.map((group) =>
            group.rooms.length === 0 ? null : (
              <div key={group.key} className="space-y-2">
                <div className="flex items-baseline gap-2">
                  <h3 className="text-xs font-semibold text-slate-400">{group.title}</h3>
                  <span className="text-[11px] text-slate-500">{group.rooms.length} 个 · {group.hint}</span>
                </div>
                <div className="grid gap-3 md:grid-cols-2">
                  {group.rooms.map((room) => (
                    <RoomCard key={room.id} room={room} onOpen={() => navigate('room', room.code)} />
                  ))}
                </div>
              </div>
            ),
          )}
        </div>

        <div className="card">
          <SectionTitle>剧本库</SectionTitle>
          {scripts.isLoading ? <Spinner /> : null}
          {published.length === 0 ? (
            <div className="space-y-3">
              <Empty>还没有已发布的剧本。</Empty>
              {isAdmin ? (
                <div className="flex flex-wrap gap-2">
                  <button type="button" className="btn-primary" onClick={() => seed.mutate()} disabled={seed.isPending}>
                    导入内置示例《雨夜灯塔》
                  </button>
                  <button type="button" className="btn-ghost" onClick={() => navigate('studio')}>
                    去剧本工作台新建
                  </button>
                </div>
              ) : (
                <p className="text-xs text-slate-500">剧库由管理员维护，等管理员发布剧本后就能建房了。</p>
              )}
            </div>
          ) : (
            <ul className="space-y-2">
              {published.map((s) => (
                <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 border-b border-line py-2 last:border-0">
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{s.title || '未命名剧本'}</p>
                    <p className="text-xs text-slate-500">
                      {s.roles} 角色 · {s.clues} 线索 · {s.playerCount ? (s.playerCount.min === s.playerCount.max ? `${s.playerCount.max} 人` : `${s.playerCount.min}-${s.playerCount.max} 人`) : '人数待定'} · v{s.version}
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    {s.tags?.slice(0, 2).map((t) => (
                      <Tag key={t}>{t}</Tag>
                    ))}
                    <button type="button" className="btn-ghost text-xs" onClick={() => setScriptId(s.id)}>
                      选中
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      <aside className="space-y-5">
        <div className="card space-y-4">
          <SectionTitle>开一个新房间</SectionTitle>
          <Field label="剧本">
            <select className="input" value={scriptId || published[0]?.id || ''} onChange={(e) => setScriptId(e.target.value)}>
              {published.length === 0 ? <option value="">（暂无已发布剧本）</option> : null}
              {published.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.title}
                </option>
              ))}
            </select>
          </Field>
          <Field label="房间名" hint="留空则使用剧本名">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="周五晚上的局" />
          </Field>
          <Field
            label="我的身份"
            hint={
              hostOnly
                ? `只当主持人：不占玩家座位，${seatFixed ? `固定 ${seatMax} 座` : '座位数'}全留给玩家`
                : '我也入座一起玩：主持人占 1 个玩家座位'
            }
          >
            <select className="input" value={hostOnly ? 'host' : 'player'} onChange={(e) => setHostOnly(e.target.value === 'host')}>
              <option value="player">我入座，一起玩</option>
              <option value="host">只当主持人（不占座）</option>
            </select>
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="座位数" hint={seatFixed ? `剧本固定 ${seatMax} 人` : `${seatMin}~${seatMax} 座（剧本人数范围）`}>
              <input
                className="input"
                type="number"
                min={seatMin}
                max={seatMax}
                disabled={seatFixed}
                value={seatFixed ? seatMax : Math.min(seatMax, Math.max(seatMin, maxSeats))}
                onChange={(e) => setMaxSeats(Math.min(seatMax, Math.max(seatMin, Number(e.target.value) || seatMin)))}
              />
            </Field>
            <Field label="允许观战">
              <select className="input" value={allowSpectator ? 'yes' : 'no'} onChange={(e) => setAllowSpectator(e.target.value === 'yes')}>
                <option value="yes">允许</option>
                <option value="no">不允许</option>
              </select>
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="发言限时" hint={speechHint}>
              <select className="input" value={speechSeconds} onChange={(e) => setSpeechSeconds(Number(e.target.value))}>
                <option value={0}>服务端默认（{speechDefault} 秒）</option>
                <option value={SPEECH_UNLIMITED}>不限制</option>
                {SPEECH_PRESETS.map((s) => (
                  <option key={s} value={s}>
                    {s} 秒
                  </option>
                ))}
              </select>
            </Field>
            <Field
              label="超时处理"
              hint={speechUnlimited ? '不限时，无需超时处理' : autoSkip ? '到点自动跳过该座位' : '保留发言权，等主持人推进阶段'}
            >
              <select
                className="input disabled:opacity-50"
                value={autoSkip ? 'skip' : 'host'}
                disabled={speechUnlimited}
                onChange={(e) => setAutoSkip(e.target.value === 'skip')}
              >
                <option value="skip">自动跳过</option>
                <option value="host">等主持人推进</option>
              </select>
            </Field>
          </div>
          <button type="button" className="btn-primary w-full" onClick={() => createRoom.mutate()} disabled={createRoom.isPending || published.length === 0}>
            {createRoom.isPending ? '创建中…' : hostOnly ? '创建房间（我只当主持人）' : '创建房间并占座'}
          </button>
        </div>

        <div className="card space-y-3">
          <SectionTitle aside={<Hint align="right" text="加入后座位会记住在这台设备上，断线重连自动回到原座位。" />}>用房号加入</SectionTitle>
          <input
            className="input text-center text-lg font-mono tracking-[0.3em]"
            value={joinCode}
            maxLength={6}
            onChange={(e) => setJoinCode(e.target.value.toUpperCase())}
            placeholder="7K3M9Q"
          />
          <div className="flex items-center gap-2">
            <button type="button" className="btn-ghost w-full" onClick={() => join.mutate()} disabled={join.isPending || joinCode.trim().length < 4}>
              {join.isPending ? '加入中…' : '加入房间'}
            </button>
          </div>
        </div>
      </aside>
    </div>
  )
}

function RoomCard({ room, onOpen }: { room: Room; onOpen: () => void }) {
  const players = room.seats.filter((s) => s.kind !== 'spectator')
  const agents = players.filter((s) => s.kind === 'agent').length
  const tone = room.status === 'running' ? 'green' : room.status === 'finished' ? 'slate' : 'blue'
  return (
    <button type="button" onClick={onOpen} className="rounded-xl border border-line/70 p-3 text-left transition hover:border-accent/60">
      <div className="flex items-center justify-between">
        <span className="font-mono text-sm tracking-widest text-accent">{room.code}</span>
        <Tag tone={tone}>{room.status === 'running' ? '进行中' : room.status === 'finished' ? '已结束' : '大厅'}</Tag>
      </div>
      <p className="mt-2 text-sm">{room.name || room.scriptTitle || '未命名房间'}</p>
      <p className="mt-1 text-xs text-slate-500">
        {players.length}/{room.maxSeats} 座 · AI {agents} · {room.allowSpectator ? '可观战' : '不开放观战'} ·{' '}
        发言 {speechLimitLabel(room.speechSeconds)}
        {isUnlimitedSpeech(room.speechSeconds) ? '' : `（${room.autoSkip ? '超时自动跳过' : '等主持人推进'}）`} ·{' '}
        {room.host ? '我是主持人' : room.mySeatId ? '我已入座' : '可加入'}
      </p>
    </button>
  )
}
