import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { useApp } from '../../store/app'
import { Field } from '../../components/ui'
import { ThemeSwitch } from '../../components/ThemeSwitch'

export function AuthPage() {
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const setSession = useApp((s) => s.setSession)
  const notify = useApp((s) => s.notify)

  const mutation = useMutation({
    mutationFn: async () => {
      if (mode === 'login') return api.login({ username, password })
      return api.register({ username, password, displayName: displayName.trim() || undefined })
    },
    onSuccess: (res) => setSession(res.token, res.user, res.refreshToken),
    onError: (err) => notify(err instanceof ApiError ? err.message : '网络异常，请稍后重试', 'error'),
  })

  return (
    <div className="relative flex min-h-full items-center justify-center px-4 py-10">
      <div className="absolute right-4 top-4">
        <ThemeSwitch />
      </div>
      <div className="w-full max-w-4xl gap-8 lg:grid lg:grid-cols-[1.1fr_1fr]">
        <section className="hidden flex-col justify-center lg:flex">
          <h1 className="text-4xl font-semibold leading-tight">
            和 AI 坐同一张桌子
            <span className="block text-accent">演一场你无法预知结局的剧本杀</span>
          </h1>
          <p className="mt-6 max-w-md text-sm leading-relaxed text-slate-400">
            AI 只拿到自己的角色本和已公开信息，真相不会进入任何 prompt。
          </p>
          <ul className="mt-8 space-y-3 text-sm text-slate-300">
            <li>· 真人 + AI 混座，房号加入、断线重连回到原座位</li>
            <li>· 6 套可调性格：坦白、谨慎、狡黠、激进、沉默、演剧</li>
            <li>· AI 发言可朗读（普通话音色）；输入用你系统输入法的语音听写</li>
            <li>· 剧本三入口导入：标准 JSON、粘贴文本、拍照识别</li>
          </ul>
        </section>

        <section className="card">
          <div className="mb-5 flex rounded-xl border border-line p-1">
            {(['login', 'register'] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={`flex-1 rounded-lg px-3 py-2 text-sm font-medium transition ${
                  mode === m ? 'bg-accent text-on-accent' : 'text-slate-400 hover:text-strong'
                }`}
              >
                {m === 'login' ? '登录' : '注册'}
              </button>
            ))}
          </div>

          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              mutation.mutate()
            }}
          >
            <Field label="用户名" hint="至少 3 个字符">
              <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required />
            </Field>
            {mode === 'register' ? (
              <Field label="昵称（可选）">
                <input className="input" value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
              </Field>
            ) : null}
            <Field label="密码" hint="至少 6 位">
              <input
                className="input"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                required
              />
            </Field>
            <button type="submit" className="btn-primary w-full" disabled={mutation.isPending}>
              {mutation.isPending ? '请稍候…' : mode === 'login' ? '进入大厅' : '创建账号'}
            </button>
          </form>
          <p className="mt-4 text-xs text-slate-500">
            账号只保存在本机服务端（bcrypt + JWT），不接入任何第三方登录。
          </p>
        </section>
      </div>
    </div>
  )
}
