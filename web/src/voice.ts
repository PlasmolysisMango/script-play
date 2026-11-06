/**
 * Browser-side voice output. Everything here runs in the browser: no audio ever
 * reaches our server, matching docs/protocol.md.
 *
 * TTS: speechSynthesis with a zh-CN voice. Each role gets a stable slice of the
 *      available voices plus its own rate/pitch, so a table of agents does not
 *      sound like one person reading everyone's lines.
 *
 * Speech *input* is deliberately absent. getUserMedia and the Web Speech
 * recogniser both require a secure context, so on the plain-http deployment the
 * microphone is not merely denied — it is missing, and a button that looks
 * pressable would lie about what it can do. The table points players at their
 * own keyboard's dictation instead, which works wherever the browser does.
 */

import type { GameEvent } from './types/schema'

// ---- text to speech ----

/** A deterministic voice choice for one speaker. */
export type VoicePlan = {
  /** Index into the ordered Chinese voice list, or -1 when no voice exists. */
  voiceIndex: number
  rate: number
  pitch: number
}

const planCache = new Map<string, VoicePlan>()

function hashSeed(seed: string): number {
  // FNV-1a: stable across reloads, so a role keeps its voice for the whole game.
  let h = 0x811c9dc5
  for (let i = 0; i < seed.length; i += 1) {
    h ^= seed.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

function round2(n: number): number {
  return Math.round(n * 100) / 100
}

/**
 * Pure mapping from a speaker seed to voice slot + prosody. Kept free of the
 * speechSynthesis API so it can be unit tested in Node.
 */
export function planFor(seed: string, voiceCount: number): VoicePlan {
  const key = `${seed}:${voiceCount}`
  const cached = planCache.get(key)
  if (cached) return cached
  const h = hashSeed(seed || 'anonymous')
  const plan: VoicePlan = {
    voiceIndex: voiceCount > 0 ? h % voiceCount : -1,
    rate: round2(0.94 + ((h >>> 8) % 19) / 100), // 0.94 – 1.12
    pitch: round2(0.82 + ((h >>> 16) % 33) / 100), // 0.82 – 1.14
  }
  planCache.set(key, plan)
  return plan
}

let voicesWarmed = false

/** Chrome populates the voice list asynchronously; ask early and on change. */
export function warmVoices() {
  if (typeof speechSynthesis === 'undefined') return
  speechSynthesis.getVoices()
  if (voicesWarmed) return
  voicesWarmed = true
  speechSynthesis.addEventListener?.('voiceschanged', () => planCache.clear())
}

/** Chinese voices, Mandarin only, local ones first, then by name so slot i is
 * stable. Cantonese (zh-HK/yue) and zh-TW are excluded on purpose: a table of
 * Mandarin speakers must never suddenly answer in Cantonese. `cmn-*` is the
 * same language under its ISO 639-3 code, which some Android engines report. */
export function isMandarinLang(lang: string): boolean {
  const l = (lang || '').replace(/_/g, '-').toLowerCase()
  if (!l) return false
  if (l === 'cmn' || l.startsWith('cmn-')) return true
  if (!(l === 'zh' || l.startsWith('zh-'))) return false
  return !/(^|-)hk($|-)|(^|-)tw($|-)|(^|-)mo($|-)|hant|yue/.test(l)
}

function allVoices(): SpeechSynthesisVoice[] {
  if (typeof speechSynthesis === 'undefined') return []
  return speechSynthesis.getVoices()
}

export function mandarinVoices(): SpeechSynthesisVoice[] {
  const voices = allVoices().filter((v) => isMandarinLang(v.lang))
  const rank = (v: SpeechSynthesisVoice) => {
    const cn = /^zh[-_]cn$/i.test(v.lang) ? 0 : 2
    return cn + (v.localService ? 0 : 1)
  }
  return voices.slice().sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
}

/**
 * Pokes the engine so Android Chromium enumerates its voices.
 *
 * Phones hand the Web Speech API to the system TTS engine, and on Chromium the
 * list often stays empty until an utterance has been spoken once — so a page
 * that waits for `voiceschanged` before speaking never gets a voice at all.
 * A silent utterance breaks that deadlock; call it from a user gesture.
 */
export function probeVoices() {
  if (typeof speechSynthesis === 'undefined') return
  speechSynthesis.getVoices()
  if (speechSynthesis.getVoices().length > 0) return
  const wake = new SpeechSynthesisUtterance(' ')
  wake.volume = 0
  wake.lang = 'zh-CN'
  speechSynthesis.speak(wake)
}

function pickMandarinVoice(): SpeechSynthesisVoice | null {
  return mandarinVoices()[0] ?? null
}

export type SpeakOptions = {
  rate?: number
  pitch?: number
  /** Stable speaker key (role id, or 'narrator' for the host) — sets the voice. */
  seed?: string
  onEnd?: () => void
}

/** Speaks one line, using the speaker's own voice when a seed is given. */
export function speak(text: string, options: SpeakOptions = {}): boolean {
  if (typeof speechSynthesis === 'undefined') return false
  const clean = text
    .replace(/[【】]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
  if (!clean) return false
  const utterance = new SpeechSynthesisUtterance(clean)
  const voices = mandarinVoices()
  const plan = options.seed ? planFor(options.seed, voices.length) : null
  const voice = plan && plan.voiceIndex >= 0 ? (voices[plan.voiceIndex] ?? pickMandarinVoice()) : pickMandarinVoice()
  if (voice) {
    utterance.voice = voice
    utterance.lang = voice.lang
  } else if (allVoices().length === 0) {
    // Nothing enumerated at all (the usual state on a phone): hand the line to
    // the system engine with a Mandarin language tag. This is the fallback that
    // keeps a phone readable, and it is deliberately narrower than "any voice":
    // when the browser *does* list voices and none is Mandarin, we stay silent
    // rather than read Chinese with a Cantonese or English synthesiser.
    utterance.lang = 'zh-CN'
  } else {
    return false
  }
  utterance.rate = options.rate ?? plan?.rate ?? 1.02
  utterance.pitch = options.pitch ?? plan?.pitch ?? 1
  if (options.onEnd) utterance.onend = () => options.onEnd?.()
  speechSynthesis.speak(utterance)
  return true
}

export function stopSpeaking() {
  if (typeof speechSynthesis !== 'undefined') speechSynthesis.cancel()
}

export type VoiceCapabilities = {
  /** A speech synthesiser exists at all. */
  tts: boolean
  /** How many Mandarin voices it offers; 0 means no named voice to assign. */
  mandarinVoices: number
  /**
   * True when the browser enumerates no voices at all, so lines are spoken by
   * the system engine through a `zh-CN` language tag instead of a named voice.
   * Phones (Chromium on Android) are in this state until an utterance wakes the
   * engine, so it is a normal state, not a broken one.
   */
  fallback: boolean
}

export function voiceCapabilities(): VoiceCapabilities {
  const tts = typeof speechSynthesis !== 'undefined'
  const named = tts ? mandarinVoices().length : 0
  const known = tts ? allVoices().length : 0
  return { tts, mandarinVoices: named, fallback: tts && known === 0 }
}

// ---- what gets read aloud ----

export type NarrationKind = 'speech' | 'dm' | 'clue' | 'vote' | 'reveal'

export type Narration = {
  text: string
  /** Voice seed: the speaker's role id for speech, 'narrator' for the host. */
  seed: string
  kind: NarrationKind
}

/**
 * Turns one game event into a line worth reading. Everything else (phase
 * changes, seat updates, leak incidents, streaming deltas) stays silent so the
 * audio does not turn into noise.
 */
export function narrationFor(ev: GameEvent, selfSeatId: string): Narration | null {
  const text = (ev.text ?? '').trim()
  if (!text) return null
  switch (ev.type) {
    case 'speech':
      // Never read the player's own line back to them.
      if (!ev.seatId || ev.seatId === selfSeatId) return null
      return { text: `${ev.speaker || '有人'}说：${text}`, seed: ev.roleId || ev.seatId, kind: 'speech' }
    case 'whisper':
      // Private to this seat, so it is read the same way a real whisper would be
      // heard — quietly, and by one person.
      if (!ev.seatId || ev.seatId === selfSeatId) return null
      return { text: `${ev.speaker || '有人'}低声对你说：${text}`, seed: ev.roleId || ev.seatId, kind: 'speech' }
    case 'dm':
      return { text: `主持人：${text}`, seed: 'narrator', kind: 'dm' }
    case 'clue.revealed':
      // The event text already reads "苏禾 公开了线索【…】：…"; a prefix would repeat it.
      return { text, seed: 'narrator', kind: 'clue' }
    case 'vote.result':
      return { text, seed: 'narrator', kind: 'vote' }
    case 'reveal':
      return { text, seed: 'narrator', kind: 'reveal' }
    default:
      return null
  }
}
