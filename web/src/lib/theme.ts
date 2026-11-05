import type { CSSProperties } from 'react'

/** Themes are declared in src/index.css as [data-theme="..."] variable blocks. */
export type ThemeId = 'night' | 'light' | 'paper' | 'forest'

export type ThemeMeta = {
  id: ThemeId
  label: string
  hint: string
}

export const THEME_STORAGE_KEY = 'script-play.theme'

export const THEMES: ThemeMeta[] = [
  { id: 'night', label: '暗夜', hint: '默认深色' },
  { id: 'light', label: '白昼', hint: '纯白底色' },
  { id: 'paper', label: '宣纸', hint: '暖白纸感' },
  { id: 'forest', label: '松林', hint: '墨绿' },
]

export function isThemeId(value: string | null | undefined): value is ThemeId {
  return THEMES.some((t) => t.id === value)
}

/** Dark themes need a light scrollbar and white-on-dark selection colours. */
export function isDarkTheme(id: ThemeId): boolean {
  return id === 'night' || id === 'forest'
}

/**
 * A stored choice always wins. Otherwise follow the OS: someone whose desktop is
 * in light mode should not land on a dark page on first visit.
 */
export function resolveTheme(stored: string | null | undefined, prefersLight: boolean): ThemeId {
  if (isThemeId(stored)) return stored
  return prefersLight ? 'light' : 'night'
}

type ThemeRoot = { dataset: Record<string, string | undefined>; style: Partial<CSSProperties> }

/** Writes the theme onto <html>; pass a stub in tests. */
export function applyTheme(id: ThemeId, root?: ThemeRoot): void {
  const el =
    root ?? (typeof document === 'undefined' ? undefined : (document.documentElement as unknown as ThemeRoot))
  if (!el) return
  el.dataset.theme = id
  el.style.colorScheme = isDarkTheme(id) ? 'dark' : 'light'
}

export function readTheme(): ThemeId {
  if (typeof window === 'undefined') return 'night'
  let stored: string | null = null
  try {
    stored = window.localStorage.getItem(THEME_STORAGE_KEY)
  } catch {
    // Private mode can throw on read; fall through to the OS preference.
  }
  const prefersLight =
    typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: light)').matches
  return resolveTheme(stored, prefersLight)
}

export function storeTheme(id: ThemeId): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, id)
  } catch {
    // Persisting is best effort: the theme still applies for this session.
  }
}
