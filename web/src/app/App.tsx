import { useEffect } from 'react'
import { useApp, parseHash } from '../store/app'
import { useGame } from '../store/game'
import { Toast } from '../components/ui'
import { ThemeSwitch } from '../components/ThemeSwitch'
import { AuthPage } from '../features/auth/AuthPage'
import { LobbyPage } from '../features/lobby/LobbyPage'
import { RoomPage } from '../features/room/RoomPage'
import { GamePage } from '../features/game/GamePage'
import { ReplayPage } from '../features/game/ReplayPage'
import { StudioPage } from '../features/studio/StudioPage'
import { AdminPage } from '../features/admin/AdminPage'

export function App() {
  const token = useApp((s) => s.token)
  const user = useApp((s) => s.user)
  const view = useApp((s) => s.view)
  const code = useApp((s) => s.code)
  const navigate = useApp((s) => s.navigate)
  const logout = useApp((s) => s.logout)
  const toast = useApp((s) => s.toast)
  const toastKind = useApp((s) => s.toastKind)
  const clearToast = useApp((s) => s.clearToast)
  const resetGame = useGame((s) => s.reset)

  // Keep the URL hash and the view in sync in both directions.
  useEffect(() => {
    if (!token) return
    const apply = () => {
      const route = parseHash(location.hash)
      if (route.view !== 'auth') navigate(route.view, route.code || code)
    }
    apply()
    window.addEventListener('hashchange', apply)
    return () => window.removeEventListener('hashchange', apply)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token])

  useEffect(() => {
    if (!toast) return
    const timer = window.setTimeout(clearToast, 4200)
    return () => window.clearTimeout(timer)
  }, [toast, clearToast])

  if (!token || !user) {
    return (
      <>
        <AuthPage />
        {toast ? <Toast message={toast} kind={toastKind} onClose={clearToast} /> : null}
      </>
    )
  }

  const isAdmin = user.role === 'admin'

  const go = (next: 'lobby' | 'studio' | 'admin') => {
    resetGame()
    navigate(next)
  }

  return (
    <div className="mx-auto flex min-h-full max-w-[1400px] flex-col px-3 py-3 sm:px-4 sm:py-4 lg:px-8">
      {/** Two rows on a phone (brand + utilities, then the nav), one row from
       *  `sm` up. Every item shares `.nav-item`, so heights never disagree. */}
      <header className="mb-4 flex flex-wrap items-center gap-2 sm:mb-5 sm:gap-3">
        <button
          type="button"
          onClick={() => go('lobby')}
          className="order-1 flex h-11 min-w-0 items-center gap-2 text-left sm:h-9"
        >
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-accent/15 text-base">🎭</span>
          <span className="truncate text-sm font-semibold tracking-wide sm:text-base">剧本杀 · AI 同局</span>
        </button>

        <nav className="nav-bar order-3 w-full sm:order-2 sm:ml-auto sm:w-auto" aria-label="主导航">
          <button type="button" className="nav-item" aria-current={view === 'lobby' ? 'page' : undefined} onClick={() => go('lobby')}>
            大厅
          </button>
          {isAdmin ? (
            <>
              <button
                type="button"
                className="nav-item"
                aria-current={view === 'studio' ? 'page' : undefined}
                onClick={() => go('studio')}
              >
                剧本工作台
              </button>
              <button
                type="button"
                className="nav-item"
                aria-current={view === 'admin' ? 'page' : undefined}
                onClick={() => go('admin')}
              >
                管理台
              </button>
            </>
          ) : null}
          {code && (view === 'room' || view === 'game' || view === 'replay') ? (
            <button
              type="button"
              className="nav-item"
              aria-current={view === 'room' ? 'page' : undefined}
              onClick={() => navigate('room', code)}
            >
              房间 {code}
            </button>
          ) : null}
        </nav>

        <div className="order-2 ml-auto flex items-center gap-2 sm:order-3 sm:ml-0">
          <span className="hidden max-w-[9rem] truncate text-xs text-slate-500 lg:inline">{user.displayName}</span>
          <ThemeSwitch withLabel={false} />
          <button type="button" className="nav-item border border-line" onClick={logout} title="退出登录">
            退出
          </button>
        </div>
      </header>

      <main className="flex-1">
        {view === 'lobby' || (!isAdmin && (view === 'studio' || view === 'admin')) ? <LobbyPage /> : null}
        {isAdmin && view === 'studio' ? <StudioPage /> : null}
        {isAdmin && view === 'admin' ? <AdminPage /> : null}
        {view === 'room' ? <RoomPage code={code} /> : null}
        {view === 'game' ? <GamePage code={code} /> : null}
        {view === 'replay' ? <ReplayPage code={code} /> : null}
      </main>

      {/** The privacy boundary line lived down here as a footer; it repeated
       *  what the login page already says, so the shell stops at the table. */}
      {toast ? <Toast message={toast} kind={toastKind} onClose={clearToast} /> : null}
    </div>
  )
}
