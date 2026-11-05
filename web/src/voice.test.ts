import { describe, expect, it } from 'vitest'
import { isMandarinLang, narrationFor, planFor } from './voice'
import type { GameEvent } from './types/schema'

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

describe('per-role voice planning', () => {
  it('keeps a speaker on the same voice across reloads', () => {
    const a = planFor('role_guard', 6)
    const b = planFor('role_guard', 6)
    expect(a).toEqual(b)
  })

  it('keeps every plan inside the tasteful prosody range', () => {
    for (const seed of ['r1', 'r2', 'r3', 'narrator', 'seat_abc', '座位']) {
      const plan = planFor(seed, 8)
      expect(plan.voiceIndex).toBeGreaterThanOrEqual(0)
      expect(plan.voiceIndex).toBeLessThan(8)
      expect(plan.rate).toBeGreaterThanOrEqual(0.94)
      expect(plan.rate).toBeLessThanOrEqual(1.12)
      expect(plan.pitch).toBeGreaterThanOrEqual(0.82)
      expect(plan.pitch).toBeLessThanOrEqual(1.14)
    }
  })

  it('reports no voice slot when the system has no Chinese voice', () => {
    expect(planFor('role_guard', 0).voiceIndex).toBe(-1)
  })

  it('spreads different roles over different slots', () => {
    const slots = new Set(['r1', 'r2', 'r3', 'r4'].map((seed) => planFor(seed, 6).voiceIndex))
    expect(slots.size).toBeGreaterThan(1)
  })
})

describe('mandarin only voices', () => {
  it('accepts plain mandarin tags', () => {
    for (const lang of ['zh-CN', 'zh_CN', 'ZH-cn', 'zh', 'zh-Hans', 'zh-Hans-CN', 'zh-SG']) {
      expect(isMandarinLang(lang)).toBe(true)
    }
  })

  it('rejects cantonese and other chinese variants', () => {
    for (const lang of ['zh-HK', 'zh_TW', 'zh-MO', 'zh-Hant', 'zh-Hant-TW', 'yue', 'en-US', '']) {
      expect(isMandarinLang(lang)).toBe(false)
    }
  })
})

describe('what gets read aloud', () => {
  it('reads another seat but never echoes my own line', () => {
    const mine = narrationFor(ev({ type: 'speech', seatId: 'seat1', text: '我先说' }), 'seat1')
    expect(mine).toBeNull()
    const other = narrationFor(
      ev({ type: 'speech', seatId: 'seat2', roleId: 'r2', speaker: '苏禾', text: '我在气象站' }),
      'seat1',
    )
    expect(other?.text).toBe('苏禾说：我在气象站')
    expect(other?.seed).toBe('r2')
    expect(other?.kind).toBe('speech')
  })

  it('falls back to the seat when a speech has no role yet', () => {
    const line = narrationFor(ev({ type: 'speech', seatId: 'seat9', text: '谁' }), 'seat1')
    expect(line?.seed).toBe('seat9')
    expect(line?.text).toBe('有人说：谁')
  })

  it('narrates the host, clues, the vote result and the reveal', () => {
    const narrator = (partial: Partial<GameEvent>) => narrationFor(ev(partial), 'seat1')
    expect(narrator({ type: 'dm', text: '欢迎来到灯塔' })).toEqual({
      text: '主持人：欢迎来到灯塔',
      seed: 'narrator',
      kind: 'dm',
    })
    expect(narrator({ type: 'clue.revealed', text: '苏禾 公开了线索值班室的钥匙板：有擦痕' })).toMatchObject({
      kind: 'clue',
      seed: 'narrator',
    })
    expect(narrator({ type: 'vote.result', text: '投票结束：苏禾 3 票' })?.kind).toBe('vote')
    expect(narrator({ type: 'reveal', text: '真相揭晓：凶手是 苏禾' })?.kind).toBe('reveal')
  })

  it('stays silent on noise like phase changes and empty text', () => {
    expect(narrationFor(ev({ type: 'phase', text: '进入阶段：第一轮讨论' }), 'seat1')).toBeNull()
    expect(narrationFor(ev({ type: 'seat.updated', text: '座位变化' }), 'seat1')).toBeNull()
    expect(narrationFor(ev({ type: 'clue.revealed', text: '   ' }), 'seat1')).toBeNull()
  })
})
