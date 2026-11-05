import { useEffect, useState } from 'react'
import { voiceCapabilities, type VoiceCapabilities } from '../voice'

/**
 * Voice capabilities, kept in step with the browser.
 *
 * `speechSynthesis.getVoices()` is empty on the very first call in Chrome and
 * Firefox: the list is filled asynchronously and announced with
 * `voiceschanged`. Reading it once at mount therefore reports "no Mandarin
 * voice" on a machine that reads Chinese perfectly well — which is how the
 * table ended up saying "朗读已停用" while the voice was clearly working.
 *
 * So the value follows the event, with a few delayed reads for browsers that
 * fire it before this listener exists. Everything is torn down on unmount.
 */
export function useVoiceCapabilities(): VoiceCapabilities {
  const [caps, setCaps] = useState<VoiceCapabilities>(voiceCapabilities)

  useEffect(() => {
    const sync = () => setCaps(voiceCapabilities())
    sync()
    const synth = typeof speechSynthesis === 'undefined' ? null : speechSynthesis
    synth?.addEventListener?.('voiceschanged', sync)
    const timers = [250, 1000, 2500].map((ms) => window.setTimeout(sync, ms))
    return () => {
      synth?.removeEventListener?.('voiceschanged', sync)
      timers.forEach((t) => window.clearTimeout(t))
    }
  }, [])

  return caps
}
