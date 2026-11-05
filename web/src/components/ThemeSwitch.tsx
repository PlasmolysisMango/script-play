import { useState } from 'react'
import { THEMES, applyTheme, readTheme, storeTheme, type ThemeId } from '../lib/theme'

/**
 * Theme picker. The choice is written to <html data-theme> immediately, so the
 * page recolours without a reload, and remembered in localStorage.
 */
export function ThemeSwitch({ withLabel = true }: { withLabel?: boolean }) {
  const [theme, setTheme] = useState<ThemeId>(() => readTheme())

  const pick = (id: ThemeId) => {
    setTheme(id)
    applyTheme(id)
    storeTheme(id)
  }

  return (
    <label className="inline-flex items-center gap-1.5 text-xs text-slate-400">
      {withLabel ? <span>主题</span> : null}
      <select
        aria-label="界面主题"
        value={theme}
        onChange={(e) => pick(e.target.value as ThemeId)}
        className="h-11 rounded-lg border border-line bg-panel/70 px-2 text-xs text-slate-300 outline-none transition hover:border-accent/60 focus:border-accent sm:h-9"
      >
        {THEMES.map((t) => (
          <option key={t.id} value={t.id} title={t.hint}>
            {t.label}
          </option>
        ))}
      </select>
    </label>
  )
}
