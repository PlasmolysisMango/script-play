/**
 * A deliberately tiny subset of Markdown, sized for role books.
 *
 * The published scripts are transcribed from the printed books, which use just
 * four devices: `##`/`###` section headings, `**bold**` emphasis, inline
 * pictures (the ship map) and diary entries that start with a `**HH:MM**`
 * stamp. The first entry of a diary also carries its date, so a stamp is
 * `**9月8日18:15**`. Parsing them into blocks keeps the reading page free of a
 * Markdown dependency and lets the timeline be laid out as rows instead of prose.
 */

export type Inline = { text: string; bold: boolean }

export type RichBlock =
  | { kind: 'h2'; text: string }
  | { kind: 'h3'; text: string }
  | { kind: 'time'; time: string; parts: Inline[] }
  | { kind: 'image'; src: string; alt: string }
  | { kind: 'rule' }
  | { kind: 'p'; parts: Inline[] }

const TIME_LINE = /^\*\*((?:\d{1,2}月\d{1,2}日\s*)?\d{1,2}[:：]\d{2})\*\*\s*(.*)$/
const IMAGE_LINE = /^!\[([^\]]*)\]\(\s*([^)\s]+)\s*\)$/

/** Split one line into bold and plain runs. Unpaired `**` stay literal. */
export function parseInline(text: string): Inline[] {
  if (!text) return []
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

export function parseRichText(source: string): RichBlock[] {
  const blocks: RichBlock[] = []
  let buffer: string[] = []

  const flush = () => {
    if (buffer.length === 0) return
    blocks.push({ kind: 'p', parts: parseInline(buffer.join('\n')) })
    buffer = []
  }

  for (const raw of source.replace(/\r\n?/g, '\n').split('\n')) {
    const line = raw.trim()
    if (!line) {
      flush()
      continue
    }
    if (line === '---' || line === '***') {
      flush()
      blocks.push({ kind: 'rule' })
      continue
    }
    const image = IMAGE_LINE.exec(line)
    if (image) {
      flush()
      blocks.push({ kind: 'image', alt: image[1], src: image[2] })
      continue
    }
    if (line.startsWith('### ')) {
      flush()
      blocks.push({ kind: 'h3', text: line.slice(4).trim() })
      continue
    }
    if (line.startsWith('## ') || line.startsWith('# ')) {
      flush()
      blocks.push({ kind: 'h2', text: line.replace(/^#+\s*/, '') })
      continue
    }
    const time = TIME_LINE.exec(line)
    if (time) {
      flush()
      blocks.push({ kind: 'time', time: stamp(time[1]), parts: parseInline(time[2]) })
      continue
    }
    buffer.push(line)
  }
  flush()
  return blocks
}

/** `9月8日18:15` reads better with a gap between the date and the clock. */
function stamp(raw: string): string {
  return raw.replace('：', ':').replace(/^(\d{1,2}月\d{1,2}日)/, '$1 ')
}
