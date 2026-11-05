import { describe, expect, it } from 'vitest'
import { THEMES, applyTheme, isDarkTheme, isThemeId, readTheme, resolveTheme } from './theme'

describe('theme registry', () => {
  it('knows the four shipped themes and nothing else', () => {
    expect(THEMES.map((t) => t.id)).toEqual(['night', 'light', 'paper', 'forest'])
    for (const t of THEMES) expect(isThemeId(t.id)).toBe(true)
    expect(isThemeId('dark')).toBe(false)
    expect(isThemeId('')).toBe(false)
    expect(isThemeId(null)).toBe(false)
  })

  it('marks exactly the two dark themes as dark', () => {
    expect(isDarkTheme('night')).toBe(true)
    expect(isDarkTheme('forest')).toBe(true)
    expect(isDarkTheme('light')).toBe(false)
    expect(isDarkTheme('paper')).toBe(false)
  })
})

describe('resolveTheme', () => {
  it('lets a stored choice win over the OS preference', () => {
    expect(resolveTheme('paper', false)).toBe('paper')
    expect(resolveTheme('night', true)).toBe('night')
  })

  it('falls back to the OS when nothing valid is stored', () => {
    expect(resolveTheme(null, true)).toBe('light')
    expect(resolveTheme('bogus', true)).toBe('light')
    expect(resolveTheme(undefined, false)).toBe('night')
  })
})

describe('applyTheme', () => {
  it('writes the attribute and the color-scheme used by native controls', () => {
    const light = { dataset: {} as Record<string, string | undefined>, style: {} as Record<string, unknown> }
    applyTheme('light', light)
    expect(light.dataset.theme).toBe('light')
    expect(light.style.colorScheme).toBe('light')

    const dark = { dataset: {} as Record<string, string | undefined>, style: {} as Record<string, unknown> }
    applyTheme('forest', dark)
    expect(dark.dataset.theme).toBe('forest')
    expect(dark.style.colorScheme).toBe('dark')
  })
})

describe('readTheme without a browser', () => {
  it('returns the default theme instead of throwing', () => {
    expect(readTheme()).toBe('night')
  })
})
