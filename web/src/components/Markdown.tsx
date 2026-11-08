import { useRef, useState, type ReactNode } from 'react'
import { parseRichText, type Inline } from '../lib/richtext'

/**
 * Markdown rendering for script text.
 *
 * The same subset the role book has always understood (see lib/richtext) is now
 * used everywhere an author writes prose: role books, clue cards, the host's
 * notes. Nothing here injects HTML — every block becomes a React element — so a
 * script cannot smuggle markup into another player's page.
 */

export function InlineRuns({ parts }: { parts: Inline[] }) {
  return (
    <>
      {parts.map((p, i) => {
        let node: ReactNode = p.text
        if (p.code) {
          node = (
            <code className="rounded bg-ink/70 px-1 py-[1px] font-mono text-[0.88em] text-accent" key={i}>
              {p.text}
            </code>
          )
        } else if (p.bold) {
          node = (
            <strong className="font-semibold text-strong" key={i}>
              {p.text}
            </strong>
          )
        }
        if (p.italic) node = <em key={i}>{node}</em>
        else if (p.href) {
          node = (
            <a key={i} href={p.href} target="_blank" rel="noreferrer" className="text-accent underline decoration-accent/40 underline-offset-2">
              {node}
            </a>
          )
        } else if (!p.bold && !p.code) {
          node = <span key={i}>{node}</span>
        }
        return node
      })}
    </>
  )
}

/** Renders one script field as blocks. Used by the studio preview and the table. */
export function Markdown({ text, className = '' }: { text: string; className?: string }) {
  const blocks = parseRichText(text ?? '')
  if (blocks.length === 0) return null
  return (
    <div className={`space-y-2 break-words text-sm leading-relaxed text-slate-300 ${className}`}>
      {blocks.map((b, i) => {
        switch (b.kind) {
          case 'h2':
            return (
              <h4 key={i} className="mt-4 border-l-2 border-accent/70 pl-2 text-sm font-semibold text-strong first:mt-0">
                {b.text}
              </h4>
            )
          case 'h3':
            return (
              <h5 key={i} className="mt-3 text-xs font-semibold text-accent first:mt-0">
                {b.text}
              </h5>
            )
          case 'rule':
            return <hr key={i} className="border-line/70" />
          case 'image':
            return (
              <figure key={i} className="my-2">
                <a href={b.src} target="_blank" rel="noreferrer">
                  <img src={b.src} alt={b.alt} loading="lazy" className="w-full rounded-lg border border-line/70 bg-panel" />
                </a>
                {b.alt ? <figcaption className="mt-1 text-center text-[11px] text-slate-500">{b.alt}</figcaption> : null}
              </figure>
            )
          case 'code':
            return (
              <pre key={i} className="scroll-thin overflow-x-auto rounded-lg border border-line bg-ink/60 p-2.5 font-mono text-[11px] leading-relaxed text-slate-300">
                <code>{b.text}</code>
              </pre>
            )
          case 'quote':
            return (
              <blockquote key={i} className="border-l-2 border-line pl-3 text-slate-400">
                <InlineRuns parts={b.parts} />
              </blockquote>
            )
          case 'ul':
            return (
              <ul key={i} className="list-disc space-y-1 pl-5 marker:text-accent/70">
                {b.items.map((item, j) => (
                  <li key={j}>
                    <InlineRuns parts={item} />
                  </li>
                ))}
              </ul>
            )
          case 'ol':
            return (
              <ol key={i} start={b.start} className="list-decimal space-y-1 pl-5 marker:text-accent/70">
                {b.items.map((item, j) => (
                  <li key={j}>
                    <InlineRuns parts={item} />
                  </li>
                ))}
              </ol>
            )
          case 'time':
            return (
              <div key={i} className="flex gap-2">
                <span className="mt-[3px] h-fit shrink-0 rounded bg-accent/15 px-1.5 py-0.5 font-mono text-[10px] leading-tight text-accent">
                  {b.time}
                </span>
                <p className="min-w-0 flex-1">
                  <InlineRuns parts={b.parts} />
                </p>
              </div>
            )
          case 'table':
            return (
              <div key={i} className="overflow-x-auto">
                <table className="w-full border-collapse text-[11px]">
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
          default:
            return (
              <p key={i} className="whitespace-pre-wrap">
                <InlineRuns parts={b.parts} />
              </p>
            )
        }
      })}
    </div>
  )
}

/**
 * A text field that speaks Markdown: write on the left, read on the right.
 *
 * The preview matters more than the syntax help — the studio is the only place
 * an author can see what a player will actually read, and a role book that
 * renders badly is discovered too late otherwise.
 */
export function MarkdownField({
  label,
  hint,
  value,
  onChange,
  disabled,
  rows = 6,
  placeholder,
  onUpload,
  minHeight,
}: {
  label: string
  hint?: string
  value: string
  onChange: (next: string) => void
  disabled?: boolean
  rows?: number
  placeholder?: string
  /** Uploads one picture and resolves to the URL to embed. */
  onUpload?: (file: File) => Promise<string | null>
  minHeight?: string
}) {
  const [preview, setPreview] = useState(false)
  const [busy, setBusy] = useState(false)
  const area = useRef<HTMLTextAreaElement | null>(null)

  const upload = async (file: File) => {
    if (!onUpload) return
    setBusy(true)
    try {
      const url = await onUpload(file)
      if (!url) return
      const el = area.current
      const at = el && typeof el.selectionStart === 'number' ? el.selectionStart : value.length
      const insertion = `\n![${file.name.replace(/\.[^.]+$/, '')}](${url})\n`
      onChange(value.slice(0, at) + insertion + value.slice(at))
      setPreview(true)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <div className="mb-1 flex items-center justify-between gap-2">
        <span className="label mb-0">{label}</span>
        <div className="flex shrink-0 items-center gap-1">
          {onUpload ? (
            <label
              className={`cursor-pointer rounded-md border border-line px-1.5 py-0.5 text-[11px] text-slate-400 hover:border-accent/60 hover:text-strong ${
                busy || disabled ? 'pointer-events-none opacity-50' : ''
              }`}
              title="上传图片并插入到光标处"
            >
              {busy ? '上传中…' : '插图'}
              <input
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif"
                className="hidden"
                disabled={busy || disabled}
                onChange={(e) => {
                  const file = e.target.files?.[0]
                  e.target.value = ''
                  if (file) void upload(file)
                }}
              />
            </label>
          ) : null}
          <div className="flex rounded-md border border-line p-0.5 text-[11px]">
            {[
              [false, '编辑'],
              [true, '预览'],
            ].map(([isPreview, text]) => (
              <button
                key={String(text)}
                type="button"
                className={`rounded px-1.5 py-0.5 transition ${
                  preview === isPreview ? 'bg-accent/20 text-strong' : 'text-slate-400 hover:text-strong'
                }`}
                onClick={() => setPreview(isPreview as boolean)}
              >
                {text as string}
              </button>
            ))}
          </div>
        </div>
      </div>
      {preview ? (
        <div
          // minHeight is a CSS length for the textarea, so the preview takes it as a
          // style too instead of splicing it into the class list.
          className="scroll-thin max-h-[22rem] min-h-[6rem] overflow-y-auto rounded-lg border border-line bg-ink/30 p-2.5"
          style={minHeight ? { minHeight } : undefined}
        >
          {value.trim() ? (
            <Markdown text={value} />
          ) : (
            <p className="text-xs text-slate-500">还是空的。切回「编辑」写点内容。</p>
          )}
        </div>
      ) : (
        <textarea
          ref={area}
          className="input font-mono text-xs leading-relaxed"
          style={minHeight ? { minHeight } : undefined}
          rows={rows}
          value={value}
          placeholder={placeholder}
          disabled={disabled}
          spellCheck={false}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {hint ? <span className="mt-1 block text-[11px] leading-relaxed text-slate-500">{hint}</span> : null}
    </div>
  )
}
