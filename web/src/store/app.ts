import { create } from 'zustand'
import { api, configureApi } from '../api/client'
import type { User } from '../types/schema'

export type View = 'auth' | 'lobby' | 'room' | 'game' | 'replay' | 'studio' | 'admin'

type AppState = {
  token: string | null
  refreshToken: string | null
  user: User | null
  view: View
  code: string
  toast: string | null
  toastKind: 'info' | 'error'
  setSession: (token: string, user: User, refreshToken?: string) => void
  /** Silent rotation performed by the API client when an access token expires. */
  updateTokens: (token: string, refreshToken: string) => void
  logout: () => void
  navigate: (view: View, code?: string) => void
  notify: (message: string, kind?: 'info' | 'error') => void
  clearToast: () => void
}

const STORAGE_KEY = 'script-play.session'

type Persisted = { token: string; user: User; refreshToken?: string }

function load(): Persisted | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Persisted
    if (!parsed.token || !parsed.user) return null
    return parsed
  } catch {
    return null
  }
}

const initial = typeof localStorage === 'undefined' ? null : load()

export const useApp = create<AppState>((set, get) => ({
  token: initial?.token ?? null,
  refreshToken: initial?.refreshToken ?? null,
  user: initial?.user ?? null,
  view: initial ? 'lobby' : 'auth',
  code: '',
  toast: null,
  toastKind: 'info',
  setSession: (token, user, refreshToken) => {
    const refresh = refreshToken ?? get().refreshToken
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ token, user, refreshToken: refresh }))
    set({ token, refreshToken: refresh, user, view: 'lobby' })
  },
  updateTokens: (token, refreshToken) => {
    const user = get().user
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ token, user, refreshToken }))
    set({ token, refreshToken })
  },
  logout: () => {
    localStorage.removeItem(STORAGE_KEY)
    set({ token: null, refreshToken: null, user: null, view: 'auth', code: '' })
  },
  navigate: (view, code) => {
    set({ view, code: code ?? get().code })
    if (typeof location !== 'undefined') {
      const hash = view === 'room' || view === 'game' || view === 'replay' ? `#/${view}/${get().code}` : `#/${view}`
      if (location.hash !== hash) history.replaceState(null, '', hash)
    }
  },
  notify: (message, kind = 'info') => set({ toast: message, toastKind: kind }),
  clearToast: () => set({ toast: null }),
}))

configureApi({
  token: () => useApp.getState().token,
  unauthorized: () => {
    useApp.getState().logout()
    useApp.getState().notify('登录已过期，请重新登录', 'error')
  },
  refresh: async () => {
    const refreshToken = useApp.getState().refreshToken
    if (!refreshToken) return null
    try {
      const pair = await api.refreshSession(refreshToken)
      useApp.getState().updateTokens(pair.token, pair.refreshToken)
      return pair.token
    } catch {
      // A rejected refresh token means the session is over; the caller logs out.
      return null
    }
  },
})

/** Parse `#/room/ABC123` into a view + room code. */
export function parseHash(hash: string): { view: View; code: string } {
  const clean = hash.replace(/^#\/?/, '')
  const [segment, code = ''] = clean.split('/')
  const views: View[] = ['auth', 'lobby', 'room', 'game', 'replay', 'studio', 'admin']
  const view = (views as string[]).includes(segment) ? (segment as View) : 'lobby'
  return { view, code: code.toUpperCase() }
}
