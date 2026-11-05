import { describe, expect, it } from 'vitest'
import { blankClue, blankRole, blankScript, moveAt, nextId, removeAt, replaceAt, secretIndex, splitIds } from './draft'

describe('nextId', () => {
  it('follows the id style already in the draft', () => {
    // Phases are written `p1` in every published script; roles carry an underscore.
    expect(nextId('p', ['p1', 'p2'])).toBe('p3')
    expect(nextId('role', ['role_1', 'role_4'])).toBe('role_5')
  })

  it('starts at 1 and never reuses a number', () => {
    expect(nextId('clue', [])).toBe('clue_1')
    expect(nextId('clue', ['clue_1', 'clue_2', undefined])).toBe('clue_3')
  })
})

describe('blankScript', () => {
  it('opens with reading and ends with reveal, which the validator demands', () => {
    const script = blankScript()
    expect(script.phases?.[0].type).toBe('reading')
    expect(script.phases?.some((p) => p.type === 'reveal')).toBe(true)
    // Ids must be unique or the editor would collapse two rows into one key.
    const ids = (script.phases ?? []).map((p) => p.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('creates roles and clues with a filled id and an empty book', () => {
    const role = blankRole([{ id: 'role_1' }])
    expect(role.id).toBe('role_2')
    expect(role.private?.script).toBe('')
    const clue = blankClue([{ id: 'clue_1' }])
    expect(clue.id).toBe('clue_2')
    expect(clue.links?.roles).toEqual([])
  })
})

describe('list helpers', () => {
  it('moves, replaces and removes without mutating the source', () => {
    const source = ['a', 'b', 'c']
    expect(moveAt(source, 0, 1)).toEqual(['b', 'a', 'c'])
    expect(moveAt(source, 0, -1)).toBe(source)
    expect(replaceAt(source, 1, 'x')).toEqual(['a', 'x', 'c'])
    expect(removeAt(source, 0)).toEqual(['b', 'c'])
    expect(source).toEqual(['a', 'b', 'c'])
  })

  it('splits ids typed with any comma or whitespace', () => {
    expect(splitIds('tag1、tag2, tag3   tag4')).toEqual(['tag1', 'tag2', 'tag3', 'tag4'])
  })
})

describe('secretIndex', () => {
  it('collects every secret with the role that owns it', () => {
    const index = secretIndex([
      { id: 'role_1', name: '船长', private: { secrets: [{ id: 's1', text: '改过日志', revealCost: 0.8 }] } },
      { id: 'role_2', name: '医生', private: { secrets: [{ id: 's2', text: '见过尸体', revealCost: 0.3 }] } },
    ])
    expect(index.map((s) => s.id)).toEqual(['s1', 's2'])
    expect(index[0].roleName).toBe('船长')
  })
})
