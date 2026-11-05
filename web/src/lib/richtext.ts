/**
 * A deliberately small Markdown subset, sized for role books and clue cards.
 *
 * Published scripts are transcribed from printed books, which use a handful of
 * devices: `##`/`###` section headings, `**bold**`, *italic*, `code`, links,
 * lists, quotes, fenced blocks, inline pictures (the ship map) and diary entries
 * that start with a `**HH:MM**` stamp. Parsing them into blocks keeps the reading
 * page free of a Markdown dependency and lets the timeline be laid out as rows
 * instead of prose.
 *
 * Anything this parser does not recognise stays literal text — a half-typed
 * marker must never silently eat the sentence around it.
 */

export type Inline = {
  text: string
  bold: boolean
  italic?: true
  code?: true
  /** Absolute or root-relative URL of a `[label](url)` run. */
  href?: string
}

export type RichBlock =
  | { kind: 'h2'; text: string }
  | { kind: 'h3'; text: string }
  | { kind: 'time'; time: string; parts: Inline[] }
  | { kind: 'image'; src: string; alt: string }
  | { kind: 'rule' }
  | { kind: 'code'; text: string; lang: string }
  | { kind: 'ul'; items: Inline[][] }
  | { kind: 'ol'; items: Inline[][]; start: number }
  | { kind: 'quote'; parts: Inline[] }
  | { kind: 'p'; parts: Inline[] }

const TIME_LINE = /^\*\*((?:\d{1,2}月\d{1,2}日\s*)?\d{1,2}[:：]\d{2})\*\*\s*(.*)$/
const IMAGE_LINE = /^!\[([^\]]*)\]\(\s*([^)\s]+)\s*\)$/
const BULLET_LINE = /^[-*+]\s+(.*)$/
const ORDERED_LINE = /^(\d{1,2})[.)]\s+(.*)$/
const QUOTE_LINE = /^>\s?(.*)$/

/** Bold, then inline code / links / italic inside each run. */
const EMPHASIS = /`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)|\*([^*\n]+)\*|_([^_\n]+)_/g

/**
 * Split one line into bold and plain runs. Unpaired `**` stay literal.
 *
 * Runs are bold-first on purpose: `**…**` is the only device the printed books
 * use for emphasis, and losing half a pair at a page break must not merge two
 * sentences.
 */
export function parseInline(text: string): Inline[] {
  if (!text) return []
  const out: Inline[] = []
  for (const run of splitBold(text)) out.push(...emphasise(run))
  return out
}

function splitBold(text: string): Inline[] {
  const parts = text.split('**')
  if (parts.length === 1) return [{ text, bold: false }]
  // An even number of pieces means the last marker lost its partner: the scan
  // occasionally drops half of a pair at a page break, and hiding it would
  // silently merge two sentences.
  const closed = parts.length % 2 === 1
  const out: Inline[] = []
  parts.forEach((piece, i) => {
    if (i === 0) {
      if (piece) out.push({ text: piece, bold: false })
      return
    }
    if (i % 2 === 0) {
      // closing marker: the run ended, what follows is plain again
      if (piece) out.push({ text: piece, bold: false })
      return
    }
    if (!closed && i === parts.length - 1) {
      out.push({ text: '**', bold: false })
      if (piece) out.push({ text: piece, bold: false })
      return
    }
    if (piece) out.push({ text: piece, bold: true })
  })
  return out
}

/** Code, links and italic inside one bold-or-plain run. */
function emphasise(run: Inline): Inline[] {
  const { text, bold } = run
  if (!text) return []
  const out: Inline[] = []
  let last = 0
  EMPHASIS.lastIndex = 0
  for (let m = EMPHASIS.exec(text); m; m = EMPHASIS.exec(text)) {
    if (m.index > last) out.push({ text: text.slice(last, m.index), bold })
    const [whole, code, label, href, star, underscore] = m
    if (code !== undefined) out.push({ text: code, bold, code: true })
    else if (label !== undefined) out.push({ text: label, bold, href })
    else out.push({ text: star ?? underscore ?? '', bold, italic: true })
    last = m.index + whole.length
  }
  if (last < text.length) out.push({ text: text.slice(last), bold })
  return out.length ? out : [{ text, bold }]
}

export function parseRichText(source: string): RichBlock[] {
  const blocks: RichBlock[] = []
  const lines = source.replace(/\r\n?/g, '\n').split('\n')
  let para: string[] = []
  let quote: string[] = []
  let list: { ordered: boolean; start: number; items: string[] } | null = null
  let fence: { lang: string; lines: string[] } | null = null

  const flushPara = () => {
    if (para.length === 0) return
    blocks.push({ kind: 'p', parts: parseInline(para.join('\n')) })
    para = []
  }
  const flushQuote = () => {
    if (quote.length === 0) return
    blocks.push({ kind: 'quote', parts: parseInline(quote.join('\n')) })
    quote = []
  }
  const flushList = () => {
    if (!list) return
    const items = list.items.map((item) => parseInline(item))
    blocks.push(list.ordered ? { kind: 'ol', start: list.start, items } : { kind: 'ul', items })
    list = null
  }
  // A heading, rule or picture ends whatever container is open.
  const cut = () => {
    flushPara()
    flushList()
    flushQuote()
  }

  for (const raw of lines) {
    if (fence) {
      // Inside a fence every byte matters: no trimming, no inline parsing.
      if (raw.trim().startsWith('```')) {
        blocks.push({ kind: 'code', lang: fence.lang, text: fence.lines.join('\n') })
        fence = null
      } else {
        fence.lines.push(raw)
      }
      continue
    }
    const line = raw.trim()
    if (!line) {
      flushPara()
      flushList()
      flushQuote()
      continue
    }
    if (line.startsWith('```')) {
      cut()
      fence = { lang: line.slice(3).trim(), lines: [] }
      continue
    }
    if (line === '---' || line === '***' || line === '___') {
      cut()
      blocks.push({ kind: 'rule' })
      continue
    }
    const image = IMAGE_LINE.exec(line)
    if (image) {
      cut()
      blocks.push({ kind: 'image', alt: image[1], src: image[2] })
      continue
    }
    if (line.startsWith('###')) {
      cut()
      blocks.push({ kind: 'h3', text: line.replace(/^#+\s*/, '') })
      continue
    }
    if (line.startsWith('## ') || line.startsWith('# ')) {
      cut()
      blocks.push({ kind: 'h2', text: line.replace(/^#+\s*/, '') })
      continue
    }
    const time = TIME_LINE.exec(line)
    if (time) {
      cut()
      blocks.push({ kind: 'time', time: stamp(time[1]), parts: parseInline(time[2]) })
      continue
    }
    const quoted = QUOTE_LINE.exec(line)
    if (quoted) {
      flushPara()
      flushList()
      quote.push(quoted[1])
      continue
    }
    const bullet = BULLET_LINE.exec(line)
    if (bullet) {
      flushPara()
      flushQuote()
      if (!list || list.ordered) {
        flushList()
        list = { ordered: false, start: 1, items: [] }
      }
      list.items.push(bullet[1])
      continue
    }
    const ordered = ORDERED_LINE.exec(line)
    if (ordered) {
      flushPara()
      flushQuote()
      if (!list || !list.ordered) {
        flushList()
        list = { ordered: true, start: Number(ordered[1]), items: [] }
      }
      list.items.push(ordered[2])
      continue
    }
    flushList()
    flushQuote()
    para.push(line)
  }
  if (fence) blocks.push({ kind: 'code', lang: fence.lang, text: fence.lines.join('\n') })
  cut()
  return blocks
}

/** `9月8日18:15` reads better with a gap between the date and the clock. */
function stamp(raw: string): string {
  return raw.replace('：', ':').replace(/^(\d{1,2}月\d{1,2}日)/, '$1 ')
}
