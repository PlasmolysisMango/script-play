// Room speech settings mirror the server's encoding of speechSeconds:
// 0 keeps the server default, -1 disables the per-turn clock, anything else is
// the number of seconds one seat gets on its turn.
export const SPEECH_UNLIMITED = -1

export const SPEECH_PRESETS = [30, 45, 60, 120, 180]

export function isUnlimitedSpeech(seconds: number): boolean {
  return seconds < 0
}

// speechLimitLabel renders the stored value for room cards and headers.
export function speechLimitLabel(seconds: number, defaultSeconds?: number): string {
  if (isUnlimitedSpeech(seconds)) return '不限时'
  if (!seconds) return defaultSeconds ? `默认 ${defaultSeconds} 秒` : '服务端默认'
  return `${seconds} 秒`
}
