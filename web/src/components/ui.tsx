import { useState, type ReactNode } from 'react'

/**
 * Long explanations live behind this "?" so the table stays quiet. Hover and
 * click both open it: hover for a mouse, click for a touch screen where there
 * is no hover at all. `aria-expanded` keeps the state visible to a screen
 * reader, and the box is `pointer-events-none` so it never eats a click.
 */
export function Hint({
  text,
  label = '更多说明',
  align = 'left',
}: {
  text: string
  label?: string
  /** Which edge the box hangs from: `right` for a Hint sitting at the row's end. */
  align?: 'left' | 'right'
}) {
  const [open, setOpen] = useState(false)
  return (
    <span className="group relative inline-flex align-middle">
      <button
        type="button"
        aria-label={label}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        onBlur={() => setOpen(false)}
        className="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full border border-line text-[10px] leading-none text-slate-500 transition-colors hover:border-accent hover:text-accent focus:outline-none focus-visible:border-accent focus-visible:text-accent"
      >
        ?
      </button>
      <span
        role="tooltip"
        // Only laid out while it is being read. An absolutely positioned box that
        // is merely transparent still widens the document, and a 16rem box next
        // to a hint near the right edge made a phone scroll sideways 45px.
        className={`pointer-events-none absolute top-full z-30 mt-1.5 w-64 max-w-[80vw] rounded-lg border border-line bg-panel px-3 py-2 text-xs font-normal leading-relaxed text-slate-300 shadow-lg ${
          align === 'right' ? 'right-0' : 'left-0'
        } ${open ? 'block' : 'hidden group-hover:block'}`}
      >
        {text}
      </span>
    </span>
  )
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="label">{label}</span>
      {children}
      {hint ? <span className="mt-1 block text-xs text-slate-500">{hint}</span> : null}
    </label>
  )
}

export function Tag({ children, tone = 'slate' }: { children: ReactNode; tone?: 'slate' | 'green' | 'amber' | 'red' | 'blue' }) {
  const tones: Record<string, string> = {
    slate: 'border-line text-slate-300',
    green: 'border-emerald-500/40 text-emerald-300',
    amber: 'border-amber-500/40 text-amber-300',
    red: 'border-rose-500/40 text-rose-300',
    blue: 'border-accent/50 text-accent',
  }
  return <span className={`chip ${tones[tone]}`}>{children}</span>
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 text-sm text-slate-400">
      <span className="h-4 w-4 animate-spin rounded-full border-2 border-line border-t-accent" />
      {label ?? '加载中…'}
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-sm text-slate-500">{children}</p>
}

export function Toast({ message, kind, onClose }: { message: string; kind: 'info' | 'error'; onClose: () => void }) {
  return (
    <div className="fixed bottom-5 left-1/2 z-50 -translate-x-1/2">
      <div
        role="status"
        className={`flex items-center gap-3 rounded-xl border px-4 py-2 text-sm shadow-lg backdrop-blur ${
          kind === 'error' ? 'border-rose-500/50 bg-rose-950/80 text-rose-100' : 'border-line bg-panel/90 text-slate-100'
        }`}
      >
        <span>{message}</span>
        <button type="button" onClick={onClose} className="text-xs text-slate-400 hover:text-strong">
          关闭
        </button>
      </div>
    </div>
  )
}

export function StatusDot({ state }: { state: 'idle' | 'connecting' | 'open' | 'closed' }) {
  const color =
    state === 'open' ? 'bg-emerald-400' : state === 'connecting' ? 'bg-amber-400' : state === 'closed' ? 'bg-rose-500' : 'bg-slate-500'
  const label = state === 'open' ? '已连接' : state === 'connecting' ? '连接中' : state === 'closed' ? '已断开' : '未连接'
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-slate-400">
      <span className={`h-2 w-2 rounded-full ${color}`} />
      {label}
    </span>
  )
}

export function SectionTitle({ children, aside }: { children: ReactNode; aside?: ReactNode }) {
  // A hairline rule instead of the tracked-out uppercase label every generated
  // dashboard wears. It repeats the accent bar of `.sheet`, so a panel reads as
  // one object: rule, title, content.
  return (
    <div className="mb-3 flex items-center justify-between gap-2">
      <h2 className="flex min-w-0 items-center gap-2 text-sm font-semibold text-strong">
        <span aria-hidden className="h-3.5 w-[2px] shrink-0 rounded-full bg-accent/70" />
        <span className="truncate">{children}</span>
      </h2>
      {aside}
    </div>
  )
}
