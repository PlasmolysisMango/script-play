import type { GameEvent, SeatState } from '../types/schema'

/** One rendered line in the transcript panel. */
export type Line = {
  key: string
  seq?: number
  kind: string
  speaker?: string
  seatId?: string
  roleId?: string
  text: string
  at?: string
  /** Display name of the seat this line answers, when it is a reply. */
  replyTo?: string
  /** true while an AI seat is still streaming its answer */
  streaming?: boolean
}

let counter = 0

function nextKey(prefix: string): string {
  counter += 1
  return `${prefix}-${counter}`
}

/** Reset the synthetic key sequence (tests only). */
export function resetLineKeys() {
  counter = 0
}

export function lineFromEvent(ev: GameEvent): Line {
  return {
    key: ev.seq ? `ev-${ev.seq}` : nextKey('ev'),
    seq: ev.seq,
    kind: ev.type,
    speaker: ev.speaker ?? '',
    seatId: ev.seatId ?? '',
    roleId: ev.roleId ?? '',
    text: ev.text ?? '',
    at: ev.at ?? '',
    replyTo: ev.meta?.replyToName ?? '',
  }
}

/**
 * Folds one websocket event into the transcript.
 *
 * Rules:
 *  - `agent.delta` extends the open streaming line of that seat;
 *  - `speech` finalises it (the streaming line is dropped, the final kept);
 *  - every other event appends once, keyed by sequence number, so a reconnect
 *    replay cannot duplicate history.
 */
export function applyEvent(lines: Line[], ev: GameEvent): Line[] {
  // Thinking markers are a live indicator, not history: they drive the
  // "somebody is composing" badge and must never be replayed after a reload.
  if (ev.type === 'agent.thinking') return lines

  if (ev.type === 'agent.delta') {
    const idx = lastStreamingIndex(lines, ev.seatId ?? '')
    if (idx >= 0) {
      const next = lines.slice()
      next[idx] = { ...next[idx], text: next[idx].text + (ev.text ?? '') }
      return next
    }
    return [
      ...lines,
      {
        key: nextKey('delta'),
        kind: 'agent.delta',
        speaker: ev.speaker ?? '',
        seatId: ev.seatId ?? '',
        roleId: ev.roleId ?? '',
        text: ev.text ?? '',
        streaming: true,
      },
    ]
  }

  if (ev.type === 'speech') {
    const final = lineFromEvent(ev)
    const idx = lastStreamingIndex(lines, ev.seatId ?? '')
    if (idx >= 0) {
      const next = lines.slice()
      next[idx] = { ...final, key: next[idx].key }
      return next
    }
    return appendOnce(lines, final)
  }

  return appendOnce(lines, lineFromEvent(ev))
}

function lastStreamingIndex(lines: Line[], seatId: string): number {
  for (let i = lines.length - 1; i >= 0; i -= 1) {
    if (lines[i].streaming && (!seatId || lines[i].seatId === seatId)) return i
  }
  return -1
}

function appendOnce(lines: Line[], line: Line): Line[] {
  if (line.seq && lines.some((l) => l.seq === line.seq && !l.streaming)) return lines
  return [...lines, line]
}

/**
 * Merges the authoritative transcript of a state snapshot with the live lines
 * already on screen. Authoritative sequences win; open streams are preserved.
 */
export function mergeWithState(lines: Line[], state: SeatState): Line[] {
  const fromState: Line[] = (state.visible.transcript ?? []).map((l) => ({
    key: `ev-${l.seq}`,
    seq: l.seq,
    kind: l.kind,
    speaker: l.speaker,
    seatId: l.seatId,
    roleId: l.roleId,
    text: l.text,
    at: l.at,
  }))
  const streaming = lines.filter((l) => l.streaming)
  const bySeq = new Map<number, Line>()
  for (const line of fromState) bySeq.set(line.seq ?? 0, line)
  for (const line of lines) {
    if (line.streaming || !line.seq) continue
    if (!bySeq.has(line.seq)) bySeq.set(line.seq, line)
  }
  const merged = [...bySeq.values()].sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0))
  return [...merged, ...streaming]
}

export type EventStyle = {
  label: string
  tone: 'phase' | 'dm' | 'speech' | 'system' | 'clue' | 'vote' | 'reveal' | 'stream' | 'leak'
}

export function styleOf(kind: string): EventStyle {
  switch (kind) {
    case 'phase':
      return { label: '阶段', tone: 'phase' }
    case 'dm':
      return { label: '主持人', tone: 'dm' }
    case 'speech':
      return { label: '发言', tone: 'speech' }
    case 'agent.delta':
      return { label: '正在说', tone: 'stream' }
    case 'clue.found':
      return { label: '线索', tone: 'clue' }
    case 'clue.revealed':
      return { label: '公开线索', tone: 'clue' }
    case 'vote':
      return { label: '投票', tone: 'vote' }
    case 'vote.result':
      return { label: '计票', tone: 'vote' }
    case 'reveal':
      return { label: '真相', tone: 'reveal' }
    case 'leak_incident':
      return { label: '泄密检查', tone: 'leak' }
    default:
      return { label: '系统', tone: 'system' }
  }
}
