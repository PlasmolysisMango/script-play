import { describe, expect, it } from 'vitest'
import { SPEECH_UNLIMITED, isUnlimitedSpeech, speechLimitLabel } from './speech'

describe('speechLimitLabel', () => {
  it('renders the server default when the room stored 0', () => {
    expect(speechLimitLabel(0)).toBe('服务端默认')
    expect(speechLimitLabel(0, 90)).toBe('默认 90 秒')
  })

  it('renders unlimited for the -1 sentinel', () => {
    expect(speechLimitLabel(SPEECH_UNLIMITED)).toBe('不限时')
    expect(isUnlimitedSpeech(SPEECH_UNLIMITED)).toBe(true)
  })

  it('renders a per-turn limit in seconds', () => {
    expect(speechLimitLabel(45)).toBe('45 秒')
    expect(isUnlimitedSpeech(45)).toBe(false)
  })
})
