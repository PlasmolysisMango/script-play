import { useEffect, useMemo } from 'react'
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
export function ReadingPage({ role, onClose }: { role: OwnRole; onClose: () => void }) {
  const blocks = useMemo(() => parseRichText(role.private.script), [role.private.script])

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
    <div className="fixed inset-0 z-40 overflow-y-auto overscroll-contain bg-page/95 backdrop-blur-sm" role="dialog" aria-modal="true">
      <div className="mx-auto max-w-3xl px-4 pb-28 pt-3 sm:px-6 sm:pb-16 sm:pt-4">
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
                  <a href={b.src} target="_blank" rel="noreferrer">
                    <img
                      src={b.src}
                      alt={b.alt}
                      loading="lazy"
                      className="mx-auto w-full max-w-2xl rounded-xl border border-line/60 bg-panel"
                    />
                  </a>
                  {b.alt ? <figcaption className="mt-2 text-center text-xs text-slate-500">{b.alt}</figcaption> : null}
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

        {/* A thumb cannot reach a header two thousand words up. */}
        <div className="fixed bottom-0 left-0 right-0 z-10 border-t border-line/70 bg-page/95 px-4 py-3 backdrop-blur sm:hidden">
          <button type="button" className="btn-primary w-full text-sm" onClick={onClose}>
            合上剧本，回到牌桌
          </button>
        </div>
      </div>
    </div>
  )
}
