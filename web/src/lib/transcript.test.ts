import { beforeEach, describe, expect, it } from 'vitest'
import { applyEvent, mergeWithState, resetLineKeys, styleOf, type Line } from './transcript'
import type { GameEvent, SeatState } from '../types/schema'

function ev(partial: Partial<GameEvent>): GameEvent {
  return {
    seq: 0,
    gameId: 'g1',
    at: '',
    type: 'speech',
    phaseId: '',
    seatId: '',
    roleId: '',
    speaker: '',
    visibility: 'public',
    text: '',
    clueId: '',
    ...partial,
  }
}

function stateWith(transcript: { seq: number; kind: string; text: string }[]): SeatState {
  return {
    status: 'running',
    visible: {
      gameId: 'g1',
      roomId: 'room1',
      phase: { id: 'p3', type: 'discussion', name: '讨论', round: 1, order: 'round_robin', searchMode: '', revealOnFind: false, perPlayerPick: 0, speechPerPlayer: 1, draws: [] },
      phaseIndex: 2,
      round: 1,
      self: { id: 'seat1', roleId: 'r1', roleName: '沈曼丽', displayName: '我', kind: 'human', isHost: true, connected: true, ready: true },
      publicRoles: [],
      ownClues: [],
      publicClues: [],
      ownFacts: [],
      items: [],
      transcript: transcript.map((l) => ({ seq: l.seq, kind: l.kind, text: l.text, at: '', seatId: '', roleId: '', speaker: '' })),
      cardRoles: [],
      seats: [],
    },
    phases: [],
    voteCount: 0,
    search: [],
    order: [],
    draw: 0,
    talkOpen: false,
    queue: [],
    hand: [],
    scoreDone: false,
    points: 0,
    pointsLabel: '',
    tablePoints: {},
  } as SeatState
}

describe('transcript folding', () => {
  beforeEach(() => resetLineKeys())

  it('extends the streaming line of one seat with deltas', () => {
    let lines: Line[] = []
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat2', text: '我' }))
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat2', text: '觉得' }))
    expect(lines).toHaveLength(1)
    expect(lines[0].text).toBe('我觉得')
    expect(lines[0].streaming).toBe(true)
  })

  it('keeps two agents streaming independently', () => {
    let lines: Line[] = []
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat2', text: 'A' }))
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat3', text: 'B' }))
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat2', text: '1' }))
    expect(lines.map((l) => l.text)).toEqual(['A1', 'B'])
  })

  it('finalises the streaming line when the speech arrives', () => {
    let lines: Line[] = []
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat2', speaker: '陆沉', text: '先说一半' }))
    lines = applyEvent(lines, ev({ type: 'speech', seq: 7, seatId: 'seat2', speaker: '陆沉', text: '完整的发言' }))
    expect(lines).toHaveLength(1)
    expect(lines[0].text).toBe('完整的发言')
    expect(lines[0].streaming).toBeUndefined()
    expect(lines[0].seq).toBe(7)
  })

  it('does not duplicate replayed events by sequence number', () => {
    let lines: Line[] = []
    const line = ev({ type: 'dm', seq: 3, speaker: '主持人', text: '欢迎' })
    lines = applyEvent(lines, line)
    lines = applyEvent(lines, line)
    expect(lines).toHaveLength(1)
  })

  it('keeps the authoritative order and open streams when a snapshot lands', () => {
    let lines: Line[] = []
    lines = applyEvent(lines, ev({ type: 'dm', seq: 1, text: '开场' }))
    lines = applyEvent(lines, ev({ type: 'agent.delta', seatId: 'seat2', text: '正在说' }))
    const merged = mergeWithState(lines, stateWith([{ seq: 1, kind: 'dm', text: '开场' }, { seq: 2, kind: 'speech', text: '补回来的历史' }]))
    expect(merged.filter((l) => !l.streaming).map((l) => l.seq)).toEqual([1, 2])
    expect(merged[merged.length - 1].streaming).toBe(true)
  })

  it('labels every known event kind', () => {
    expect(styleOf('phase').label).toBe('阶段')
    expect(styleOf('clue.found').label).toBe('线索')
    expect(styleOf('reveal').tone).toBe('reveal')
    expect(styleOf('unknown-kind').tone).toBe('system')
  })
})
