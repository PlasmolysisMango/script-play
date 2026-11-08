import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { useApp } from '../../store/app'
import { Empty, SectionTitle, Spinner, Tag } from '../../components/ui'
import { styleOf } from '../../lib/transcript'

/**
 * Host-only replay: the solution plus the full event log.
 *
 * The endpoint refuses non-hosts, so this page never holds the truth for a
 * player seat.
 */
export function ReplayPage({ code }: { code: string }) {
  const navigate = useApp((s) => s.navigate)
  const replayQuery = useQuery({ queryKey: ['replay', code], queryFn: () => api.replay(code) })
  const [roleNames, setRoleNames] = useState<Record<string, string>>({})
  const [namesReady, setNamesReady] = useState(false)

  const data = replayQuery.data

  // Role names come from the published script (redacted copy still has names).
  useEffect(() => {
    if (!data) return
    let cancelled = false
    void (async () => {
      const map: Record<string, string> = {}
      const raw = data.game.scriptId
      const scriptId = typeof raw === 'string' ? raw : ''
      if (scriptId) {
        try {
          const res = await api.getScript(scriptId)
          const body = res.script.body as { roles?: { id: string; name: string }[] } | undefined
          for (const role of body?.roles ?? []) map[role.id] = role.name
        } catch {
          /* fall back to ids */
        }
      }
      if (!cancelled) setRoleNames(map)
      if (!cancelled) setNamesReady(true)
    })()
    return () => {
      cancelled = true
    }
  }, [data])

  const killer = useMemo(() => {
    if (!data) return ''
    // The seat list already carries the role name, so the card never flashes a
    // "loading" placeholder while the script lookup is in flight.
    const seat = data.game.seats.find((s) => s.roleId === data.truth.killerRoleId)
    return roleNames[data.truth.killerRoleId] ?? seat?.roleName ?? ''
  }, [data, roleNames])

  // Event logs carry seat ids; the replay should read as names, not ids.
  const seatNames = useMemo(() => {
    const map: Record<string, string> = {}
    for (const seat of data?.game.seats ?? []) {
      map[seat.id] = roleNames[seat.roleId] || seat.roleName || seat.displayName
    }
    return map
  }, [data, roleNames])

  // Votes are reconstructed from the log so the replay shows the outcome even
  // when the truth block has no timeline to hang it on.
  const votes = useMemo(() => {
    const casts = (data?.replay ?? []).filter((e) => e.type === 'vote')
    const result = [...(data?.replay ?? [])].reverse().find((e) => e.type === 'vote.result')
    return {
      casts: casts.map((e) => ({ seq: e.seq, text: e.text, who: seatNames[e.seatId] || e.speaker || '有人' })),
      result: result?.text ?? '',
    }
  }, [data, seatNames])

  if (replayQuery.isLoading) return <Spinner label="正在读取复盘…" />
  if (!data) {
    return (
      <Empty>
        只有主持人能看完整复盘，而且需要在对局结束后访问。
        <button type="button" className="btn-ghost ml-2 text-xs" onClick={() => navigate('lobby')}>
          回到大厅
        </button>
      </Empty>
    )
  }

  return (
    <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
      <section className="space-y-4">
        <div className="card border-rose-500/40 bg-rose-950/20">
          <SectionTitle aside={<Tag tone="red">含剧透</Tag>}>真相</SectionTitle>
          <p className="text-lg font-semibold text-rose-100">凶手：{killer || (namesReady ? (data.truth.killerRoleId ? '未标注' : 'NPC（见真相）') : '（读取角色名…）')}</p>
          {data.truth.method ? <p className="mt-2 text-sm text-slate-300">手法：{data.truth.method}</p> : null}
          {data.truth.motive ? <p className="mt-1 text-sm text-slate-300">动机：{data.truth.motive}</p> : null}
          {data.truth.summary ? (
            <p className="mt-2 whitespace-pre-wrap text-sm leading-relaxed text-slate-400">{data.truth.summary}</p>
          ) : null}
          {data.truth.timeline.length > 0 ? (
            <div className="mt-3">
              <p className="mb-1 text-xs uppercase tracking-wide text-rose-300/80">案发时间线</p>
              <ol className="list-decimal space-y-1 pl-5 text-sm text-slate-300">
                {data.truth.timeline.map((t) => (
                  <li key={t}>{t}</li>
                ))}
              </ol>
            </div>
          ) : (
            <div className="mt-3">
              <p className="mb-1 text-xs uppercase tracking-wide text-rose-300/80">案发时间线</p>
              <p className="text-xs text-slate-500">这个剧本没有在真相里填写时间线，可以在工作台补上。</p>
            </div>
          )}
        </div>

        <div className="card">
          <SectionTitle aside={<span className="text-xs text-slate-500">共 {data.replay.length} 条事件</span>}>
            完整时间线
          </SectionTitle>
          <ol className="scroll-thin max-h-[60vh] space-y-2 overflow-y-auto pr-1">
            {data.replay.map((ev) => {
              const style = styleOf(ev.type)
              const speaker = ev.speaker || seatNames[ev.seatId] || roleNames[ev.roleId] || ''
              return (
                <li key={ev.seq} className="rounded-xl border border-line/60 px-3 py-2 text-sm">
                  <div className="mb-1 flex items-center gap-2 text-[11px] tracking-wide text-slate-500">
                    <span>#{ev.seq}</span>
                    <span>{style.label}</span>
                    {speaker && speaker !== style.label ? <span className="text-slate-300">{speaker}</span> : null}
                    {ev.visibility !== 'public' ? <Tag tone="amber">{ev.visibility.startsWith('seat:') ? '私密' : ev.visibility}</Tag> : null}
                  </div>
                  <p className="whitespace-pre-wrap text-slate-300">{ev.text || '（无内容）'}</p>
                </li>
              )
            })}
          </ol>
        </div>
      </section>

      <aside className="space-y-4">
        <div className="sheet">
          <SectionTitle aside={<span className="text-xs text-slate-500">{votes.casts.length} 票</span>}>投票结果</SectionTitle>
          {votes.result ? (
            <p className="mb-2 rounded-lg border border-rose-500/30 bg-rose-950/20 px-2 py-1 text-sm text-rose-100">{votes.result}</p>
          ) : null}
          {votes.casts.length === 0 ? (
            <p className="text-xs text-slate-500">本局没有进入投票，或没有人投票。</p>
          ) : (
            <ul className="space-y-1 text-xs">
              {votes.casts.map((v) => (
                <li key={v.seq} className="flex items-start justify-between gap-2 rounded-lg px-2 py-1">
                  <span className="text-slate-300">{v.who}</span>
                  <span className="text-right text-slate-500">{v.text}</span>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="sheet">
          <SectionTitle>阶段复盘</SectionTitle>
          <ol className="space-y-1 text-xs">
            {data.phases.map((p) => (
              <li key={p.id} className="flex items-center justify-between rounded-lg px-2 py-1 text-slate-300">
                <span>{p.name}</span>
                <span className="text-slate-500">{p.status === 'done' ? '已完成' : p.status === 'current' ? '停在这里' : '未进入'}</span>
              </li>
            ))}
          </ol>
        </div>

        <div className="sheet">
          <SectionTitle>座位</SectionTitle>
          <ul className="space-y-1 text-xs">
            {data.game.seats.map((s) => (
              <li key={s.id} className="flex items-center justify-between rounded-lg px-2 py-1">
                <span className="text-slate-300">{roleNames[s.roleId] || s.roleName || s.displayName}</span>
                <span className="text-slate-500">{s.kind === 'agent' ? 'AI' : s.kind === 'spectator' ? '观战' : '真人'}</span>
              </li>
            ))}
          </ul>
        </div>

        <button type="button" className="btn-ghost w-full" onClick={() => navigate('room', code)}>
          返回房间
        </button>
      </aside>
    </div>
  )
}
