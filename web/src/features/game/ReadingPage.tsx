import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import { InlineRuns } from '../../components/Markdown'
import { parseRichText } from '../../lib/richtext'
import type { OwnRole } from '../../types/schema'

/**
 * The role book, full screen.
 *
 * Reading a script is not a glance: players sit with this page open for minutes
 * while the host talks, so it gets the whole viewport, comfortable measure and
 * real typography instead of a scrollable sidebar. The layout follows the
 * printed book: section headings, `**bold**` emphasis the author used to mark
 * what must not be missed, the ship map, and diary entries aligned by clock.
 */
export function ReadingPage({
  role,
  onClose,
  storageKey,
}: {
  role: OwnRole
  onClose: () => void
  /** Per-game/per-seat row key for "remember where I stopped reading". */
  storageKey?: string
}) {
  const blocks = useMemo(() => parseRichText(role.private.script), [role.private.script])
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const savedTop = useRef<number | null>(null)
  const saveTimer = useRef<number | null>(null)
  const userMoved = useRef(false)

  const persist = useCallback(() => {
    const el = scrollRef.current
    if (!el || !storageKey) return
    try {
      localStorage.setItem(storageKey, String(Math.max(0, Math.round(el.scrollTop))))
    } catch {
      // Private mode can refuse storage; reading still works, it just forgets.
    }
  }, [storageKey])

  const restore = useCallback(() => {
    const el = scrollRef.current
    if (!el || savedTop.current === null || userMoved.current) return
    el.scrollTop = savedTop.current
  }, [])

  // Images above the saved position change the document height after mount.
  // Retrying for a short window (and again on every image load) lands the
  // reader back on the same paragraph instead of clamping to a half-loaded page.
  useLayoutEffect(() => {
    savedTop.current = null
    userMoved.current = false
    let timers: number[] = []
    if (storageKey) {
      try {
        const raw = localStorage.getItem(storageKey)
        const n = raw === null ? NaN : Number(raw)
        if (Number.isFinite(n) && n > 0) savedTop.current = n
      } catch {
        savedTop.current = null
      }
    }
    if (savedTop.current !== null) {
      timers = [0, 120, 350, 800, 1500].map((ms) => window.setTimeout(restore, ms))
    }
    return () => {
      timers.forEach((id) => window.clearTimeout(id))
      if (saveTimer.current !== null) window.clearTimeout(saveTimer.current)
      saveTimer.current = null
      persist()
    }
  }, [storageKey, restore, persist])

  const onScroll = () => {
    if (saveTimer.current !== null) return
    saveTimer.current = window.setTimeout(() => {
      saveTimer.current = null
      persist()
    }, 200)
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      window.removeEventListener('keydown', onKey)
      document.body.style.overflow = overflow
    }
  }, [onClose])

  return (
    // The book scrolls in its own column and the phone's close button is a
    // sibling below it, not an overlay: a fixed bar laid over the text cut
    // through the last lines, and "it covers the page you are reading" is the
    // worst possible failure for this screen.
    <div className="fixed inset-0 z-40 flex flex-col bg-page/95 backdrop-blur-sm" role="dialog" aria-modal="true">
      <div
        ref={scrollRef}
        className="scroll-thin min-h-0 flex-1 overflow-y-auto overscroll-contain"
        onScroll={onScroll}
        onWheel={() => { userMoved.current = true }}
        onTouchMove={() => { userMoved.current = true }}
        onKeyDown={() => { userMoved.current = true }}
      >
        <div className="mx-auto max-w-3xl px-4 pb-6 pt-3 sm:px-6 sm:pb-16 sm:pt-4">
          <div className="sticky top-0 z-10 -mx-4 mb-5 flex items-start justify-between gap-3 border-b border-line/70 bg-page/90 px-4 py-2.5 backdrop-blur sm:-mx-6 sm:mb-6 sm:px-6 sm:py-3">
            <div className="min-w-0">
              <p className="text-[11px] uppercase tracking-[0.2em] text-slate-500">角色本</p>
              <h1 className="break-words text-base font-semibold leading-snug text-strong sm:text-lg">
                {role.name}
                {role.public.intro ? <span className="ml-2 text-xs font-normal text-slate-400">{role.public.intro}</span> : null}
              </h1>
            </div>
            <button type="button" className="btn-ghost shrink-0 text-xs" onClick={onClose}>
              合上剧本
            </button>
          </div>

          {/* Phone screens get larger type and a looser leading: a script read
              half-slouched on a couch is not a glance at a dashboard. */}
          <article className="space-y-5 break-words text-base leading-[1.9] text-slate-200 sm:text-[15px] sm:leading-8">
            {blocks.map((b, i) => {
              if (b.kind === 'h2') {
                return (
                  <h2
                    key={i}
                    className="mt-10 border-l-2 border-accent/70 pl-3 text-base font-semibold tracking-wide text-strong first:mt-0"
                  >
                    {b.text}
                  </h2>
                )
              }
              if (b.kind === 'h3') {
                return (
                  <h3 key={i} className="mt-8 text-sm font-semibold tracking-wide text-accent">
                    {b.text}
                  </h3>
                )
              }
              if (b.kind === 'rule') {
                return <hr key={i} className="border-line/70" />
              }
              if (b.kind === 'image') {
                return (
                  <figure key={i} className="my-6">
                    <a href={b.src} target="_blank" rel="noreferrer" className="block">
                      {/* The ship map is the first block of every role book and the
                          one picture players actually study. It loads eagerly: a
                          lazy image collapsed to a 2px frame while the phone was
                          still fetching it, which reads as "the picture is
                          broken". It takes the full column width and grows to its
                          natural height — an earlier `max-h-[70svh]` squeezed a
                          1435×2252 map down to a ~400px-wide thumbnail, which
                          reads as "the map is cut in half". The column already
                          scrolls, so nothing needs to fit one screen. */}
                      <img
                        src={b.src}
                        alt={b.alt}
                        decoding="async"
                        onLoad={restore}
                        className="mx-auto block h-auto w-full rounded-xl border border-line/60 bg-panel"
                      />
                    </a>
                    <figcaption className="mt-2 text-center text-xs text-slate-500">
                      {b.alt ? `${b.alt} · ` : ''}
                      <a href={b.src} target="_blank" rel="noreferrer" className="text-accent underline decoration-accent/40 underline-offset-2">
                        看原图
                      </a>
                    </figcaption>
                  </figure>
                )
              }
              if (b.kind === 'time') {
                return (
                  <div key={i} className="flex gap-3">
                    <span className="mt-[7px] h-fit shrink-0 rounded-md bg-accent/15 px-1.5 py-0.5 font-mono text-[11px] leading-tight text-accent">
                      {b.time}
                    </span>
                    <p className="min-w-0 flex-1">
                      <InlineRuns parts={b.parts} />
                    </p>
                  </div>
                )
              }
              // The library gained lists, quotes and fenced code so a clue card and
              // a role book can be written the same way. Book typography stays: a
              // script is read sitting down, not skimmed like a dashboard.
              if (b.kind === 'quote') {
                return (
                  <blockquote key={i} className="border-l-2 border-line pl-3 text-slate-400">
                    <InlineRuns parts={b.parts} />
                  </blockquote>
                )
              }
              // Character profiles print as a four-cell grid. Keep it a table:
              // reading 「性别 / 年龄 / 身高 / 体重」 as a run-on sentence loses the shape.
              if (b.kind === 'table') {
                return (
                  <div key={i} className="overflow-x-auto">
                    <table className="w-full border-collapse text-sm">
                      <thead>
                        <tr>
                          {b.head.map((cell, j) => (
                            <th key={j} className="border-b border-line px-2 py-1 text-left font-semibold text-slate-400">
                              <InlineRuns parts={cell} />
                            </th>
                          ))}
                        </tr>
                      </thead>
                      <tbody>
                        {b.rows.map((row, j) => (
                          <tr key={j}>
                            {row.map((cell, k) => (
                              <td key={k} className="border-b border-line/60 px-2 py-1">
                                <InlineRuns parts={cell} />
                              </td>
                            ))}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )
              }
              if (b.kind === 'ul') {
                return (
                  <ul key={i} className="list-disc space-y-1 pl-5 marker:text-accent/70">
                    {b.items.map((item, j) => (
                      <li key={j}>
                        <InlineRuns parts={item} />
                      </li>
                    ))}
                  </ul>
                )
              }
              if (b.kind === 'ol') {
                return (
                  <ol key={i} start={b.start} className="list-decimal space-y-1 pl-5 marker:text-accent/70">
                    {b.items.map((item, j) => (
                      <li key={j}>
                        <InlineRuns parts={item} />
                      </li>
                    ))}
                  </ol>
                )
              }
              if (b.kind === 'code') {
                return (
                  <pre key={i} className="scroll-thin overflow-x-auto rounded-lg border border-line bg-ink/60 p-3 font-mono text-xs leading-relaxed text-slate-300">
                    <code>{b.text}</code>
                  </pre>
                )
              }
              return (
                <p key={i} className="whitespace-pre-wrap">
                  <InlineRuns parts={b.parts} />
                </p>
              )
            })}
          </article>
        </div>
      </div>

      {/* A thumb cannot reach a header two thousand words up. It sits below the
          scrolling column, so it never covers a line of the book. */}
      <div className="border-t border-line/70 bg-page/95 px-4 py-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] backdrop-blur sm:hidden">
        <button type="button" className="btn-primary w-full text-sm" onClick={onClose}>
          合上剧本，回到牌桌
        </button>
      </div>
    </div>
  )
}
