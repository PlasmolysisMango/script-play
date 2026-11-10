import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { Empty, SectionTitle, Spinner, Tag } from '../../components/ui'

/**
 * Admin console: accounts, rooms and the script library.
 *
 * Passwords are bcrypt hashes, so there is no "show password" here on purpose.
 * Resetting issues a one-time password instead, which is the only shape that
 * does not require storing a readable secret.
 */
export function AdminPage() {
  const [tab, setTab] = useState<'users' | 'rooms'>('users')
  const navigate = useApp((s) => s.navigate)
  const notify = useApp((s) => s.notify)
  const user = useApp((s) => s.user)

  if (user?.role !== 'admin') {
    return (
      <div className="card mx-auto max-w-lg space-y-3 text-center">
        <p className="text-sm text-slate-300">这个账号没有管理权限。</p>
        <p className="text-xs text-slate-500">
          管理员账号由服务端环境变量 SP_ADMIN_USERNAME 指定，需要的话请联系部署者。
        </p>
        <button type="button" className="btn-ghost text-xs" onClick={() => navigate('lobby')}>
          回大厅
        </button>
      </div>
    )
  }

  return (
    <div className="grid gap-4 xl:grid-cols-[220px_minmax(0,1fr)]">
      <aside className="card h-fit">
        <SectionTitle>管理台</SectionTitle>
        <div className="flex gap-1 rounded-xl border border-line p-1 text-xs xl:flex-col">
          {(
            [
              ['users', '账号'],
              ['rooms', '房间'],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              type="button"
              className={`flex-1 rounded-lg px-2 py-1.5 transition ${
                tab === key ? 'bg-accent/20 text-strong' : 'text-slate-400 hover:text-strong'
              }`}
              onClick={() => setTab(key)}
            >
              {label}
            </button>
          ))}
        </div>
        <button type="button" className="btn-primary mt-3 w-full text-xs" onClick={() => navigate('studio')}>
          进入剧本工作台
        </button>
        <p className="mt-2 text-[11px] leading-relaxed text-slate-500">
          剧本库由管理员维护：普通账号只能选择已发布的剧本建房，不能导入或修改。
        </p>
      </aside>

      <section className="space-y-4">{tab === 'users' ? <UsersPanel notify={notify} /> : <RoomsPanel notify={notify} />}</section>
    </div>
  )
}

function UsersPanel({ notify }: { notify: (message: string, kind?: 'info' | 'error') => void }) {
  const queryClient = useQueryClient()
  const [issued, setIssued] = useState<{ username: string; password: string } | null>(null)

  const users = useQuery({ queryKey: ['admin', 'users'], queryFn: () => api.adminUsers() })

  const reset = useMutation({
    mutationFn: (target: { id: string; username: string }) => api.adminResetPassword(target.id),
    onSuccess: (res, target) => {
      setIssued({ username: target.username, password: res.password })
      notify('已生成临时密码')
      void queryClient.invalidateQueries({ queryKey: ['admin', 'users'] })
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '重置失败', 'error'),
  })

  const purge = useMutation({
    mutationFn: () => api.adminPurgeUsers(),
    onSuccess: (res) => {
      const handed = res.scriptsReassigned > 0 ? `，${res.scriptsReassigned} 个剧本转归管理员` : ''
      notify(`已清理 ${res.deleted} 个普通账号${handed}${res.failed > 0 ? `，${res.failed} 个失败` : ''}`)
      void queryClient.invalidateQueries({ queryKey: ['admin', 'users'] })
      void queryClient.invalidateQueries({ queryKey: ['admin', 'rooms'] })
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '清理账号失败', 'error'),
  })

  if (users.isLoading) return <Spinner label="正在读取账号…" />
  if (users.error) return <Empty>读取账号失败</Empty>
  const rows = users.data ?? []
  const nonAdmins = rows.filter((u) => u.role !== 'admin').length

  return (
    <div className="sheet">
      <SectionTitle aside={<Tag>{rows.length} 个账号</Tag>}>账号</SectionTitle>
      <div className="mb-3 flex flex-wrap items-center justify-end gap-2">
        <button
          type="button"
          className="btn-ghost text-[11px] text-rose-300"
          disabled={purge.isPending || nonAdmins === 0}
          onClick={() => {
            if (
              window.confirm(
                `删除全部 ${nonAdmins} 个普通账号？这些账号会立即无法登录，名下剧本转归当前管理员，管理员账号不受影响。`,
              )
            ) {
              purge.mutate()
            }
          }}
        >
          清理所有普通账号
        </button>
      </div>
      {issued ? (
        <div className="mb-3 rounded-xl border border-amber-500/40 bg-amber-950/30 p-3 text-xs text-amber-100">
          <p>
            <span className="font-semibold">{issued.username}</span> 的新临时密码：
            <code className="ml-1 select-all rounded bg-ink/60 px-1.5 py-0.5 font-mono">{issued.password}</code>
          </p>
          <p className="mt-1 text-amber-200/70">只在这里显示一次，服务端只保存哈希。请让本人登录后立即修改。</p>
          <button type="button" className="btn-ghost mt-2 text-[11px]" onClick={() => setIssued(null)}>
            知道了
          </button>
        </div>
      ) : null}

      <div className="overflow-x-auto">
        <table className="w-full text-left text-xs">
          <thead className="text-slate-500">
            <tr>
              <th className="py-2 pr-3 font-medium">账号</th>
              <th className="py-2 pr-3 font-medium">显示名</th>
              <th className="py-2 pr-3 font-medium">角色</th>
              <th className="py-2 pr-3 font-medium">剧本</th>
              <th className="py-2 pr-3 font-medium">注册</th>
              <th className="py-2 pr-3 font-medium">最近登录</th>
              <th className="py-2 font-medium">密码</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line/60">
            {rows.map((u) => (
              <tr key={u.id} className="text-slate-300">
                <td className="py-2 pr-3 font-mono">{u.username}</td>
                <td className="py-2 pr-3">{u.displayName}</td>
                <td className="py-2 pr-3">
                  {u.role === 'admin' ? <Tag tone="blue">管理员</Tag> : <span className="text-slate-500">普通</span>}
                </td>
                <td className="py-2 pr-3">{u.scripts}</td>
                <td className="py-2 pr-3 text-slate-500">{shortDate(u.createdAt)}</td>
                <td className="py-2 pr-3 text-slate-500">{u.lastLoginAt ? shortDate(u.lastLoginAt) : '—'}</td>
                <td className="py-2">
                  <button
                    type="button"
                    className="btn-ghost text-[11px]"
                    disabled={reset.isPending}
                    onClick={() => {
                      if (window.confirm(`为 ${u.username} 生成新的临时密码？旧密码会立即失效。`)) {
                        reset.mutate({ id: u.id, username: u.username })
                      }
                    }}
                  >
                    重置
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-[11px] leading-relaxed text-slate-500">
        密码以 bcrypt 单向存储，任何地方都看不到原密码。需要帮用户找回时，重置为一次性临时密码。
      </p>
    </div>
  )
}

function RoomsPanel({ notify }: { notify: (message: string, kind?: 'info' | 'error') => void }) {
  const queryClient = useQueryClient()
  const rooms = useQuery({ queryKey: ['admin', 'rooms'], queryFn: () => api.adminRooms(), refetchInterval: 5000 })

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['admin', 'rooms'] })

  const close = useMutation({
    mutationFn: (code: string) => api.adminCloseRoom(code),
    onSuccess: () => {
      notify('房间已结束，玩家仍可查看复盘')
      void refresh()
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '关闭失败', 'error'),
  })

  const remove = useMutation({
    mutationFn: (code: string) => api.adminDeleteRoom(code),
    onSuccess: () => {
      notify('房间已删除')
      void refresh()
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '删除失败', 'error'),
  })

  const purge = useMutation({
    mutationFn: (all: boolean) => api.adminPurgeRooms(all),
    onSuccess: (res, all) => {
      const skipped = res.skipped > 0 ? `，跳过 ${res.skipped} 个运行中的房间` : ''
      const failed = res.failed > 0 ? `，${res.failed} 个失败` : ''
      notify(`${all ? '已清理全部' : '已清理非运行'}房间：删除 ${res.deleted} 个${skipped}${failed}`)
      void refresh()
    },
    onError: (err) => notify(err instanceof ApiError ? err.message : '清理房间失败', 'error'),
  })

  if (rooms.isLoading) return <Spinner label="正在读取房间…" />
  if (rooms.error) return <Empty>读取房间失败</Empty>
  const rows = rooms.data ?? []
  const nonRunning = rows.filter((r) => !r.running).length

  return (
    <div className="sheet">
      <SectionTitle aside={<Tag>{rows.length} 个房间</Tag>}>房间</SectionTitle>
      <div className="mb-3 flex flex-wrap items-center justify-end gap-2">
        <button
          type="button"
          className="btn-ghost text-[11px]"
          disabled={purge.isPending || nonRunning === 0}
          onClick={() => {
            if (window.confirm(`删除 ${nonRunning} 个非运行房间？等待开始和已结束的房间记录都会被删除，运行中的对局不受影响。`)) {
              purge.mutate(false)
            }
          }}
        >
          清理非运行房间
        </button>
        <button
          type="button"
          className="btn-ghost text-[11px] text-rose-300"
          disabled={purge.isPending || rows.length === 0}
          onClick={() => {
            if (
              window.confirm(
                `删除全部 ${rows.length} 个房间？运行中的对局会立即停止，房间记录与对局日志都会消失，且无法恢复。`,
              )
            ) {
              purge.mutate(true)
            }
          }}
        >
          清理所有房间
        </button>
      </div>
      {rows.length === 0 ? (
        <Empty>还没有任何房间。</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <thead className="text-slate-500">
              <tr>
                <th className="py-2 pr-3 font-medium">房号</th>
                <th className="py-2 pr-3 font-medium">剧本</th>
                <th className="py-2 pr-3 font-medium">房主</th>
                <th className="py-2 pr-3 font-medium">状态</th>
                <th className="py-2 pr-3 font-medium">座位</th>
                <th className="py-2 pr-3 font-medium">创建</th>
                <th className="py-2 font-medium">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line/60">
              {rows.map((r) => (
                <tr key={r.id} className="text-slate-300">
                  <td className="py-2 pr-3 font-mono tracking-wider text-accent">{r.code}</td>
                  <td className="py-2 pr-3">{r.scriptTitle || '—'}</td>
                  <td className="py-2 pr-3">{r.hostName || '—'}</td>
                  <td className="py-2 pr-3">
                    <Tag tone={r.status === 'running' ? 'green' : r.status === 'finished' ? 'slate' : 'amber'}>
                      {r.status === 'running' ? '对局中' : r.status === 'finished' ? '已结束' : '等待开始'}
                    </Tag>
                    {r.running ? <span className="ml-1 text-[10px] text-emerald-400">运行中</span> : null}
                  </td>
                  <td className="py-2 pr-3">
                    {r.players} 玩家 / {r.seats} 座
                  </td>
                  <td className="py-2 pr-3 text-slate-500">{shortDate(r.createdAt)}</td>
                  <td className="py-2">
                    <div className="flex gap-1">
                      <button
                        type="button"
                        className="btn-ghost text-[11px]"
                        disabled={r.status === 'finished' || close.isPending}
                        onClick={() => {
                          if (window.confirm(`结束房间 ${r.code}？对局会立即停止，玩家仍可查看复盘。`)) close.mutate(r.code)
                        }}
                      >
                        结束
                      </button>
                      <button
                        type="button"
                        className="btn-ghost text-[11px] text-rose-300"
                        disabled={remove.isPending}
                        onClick={() => {
                          if (window.confirm(`彻底删除房间 ${r.code}？房间记录与对局日志都会消失，无法恢复。`)) remove.mutate(r.code)
                        }}
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function shortDate(value?: string): string {
  if (!value) return '—'
  const at = Date.parse(value)
  if (!Number.isFinite(at)) return '—'
  return new Date(at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
