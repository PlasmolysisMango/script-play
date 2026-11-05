import { create } from 'zustand'
import type { GameEvent, Room, SeatState } from '../types/schema'
import { applyEvent, mergeWithState, type Line } from '../lib/transcript'

type ConnState = 'idle' | 'connecting' | 'open' | 'closed'

type GameStore = {
  room: Room | null
  state: SeatState | null
  lines: Line[]
  /**
   * Seat id → timestamp of its "thinking" marker. Kept apart from `lines`
   * because it is a live indicator: it disappears when the model answers and
   * must never be replayed from history.
   */
  thinking: Record<string, number>
  conn: ConnState
  tts: boolean
  setRoom: (room: Room) => void
  setState: (state: SeatState) => void
  pushEvent: (ev: GameEvent) => void
  setConn: (conn: ConnState) => void
  setTts: (on: boolean) => void
  reset: () => void
}

export const useGame = create<GameStore>((set) => ({
  room: null,
  state: null,
  lines: [],
  thinking: {},
  conn: 'idle',
  tts: typeof localStorage !== 'undefined' && localStorage.getItem('script-play.tts') === '1',
  setRoom: (room) => set({ room }),
  setState: (state) =>
    set((prev) => ({
      state,
      // The snapshot is authoritative: merge it instead of replacing so the
      // lines received while the websocket was down are not lost twice.
      lines: mergeWithState(prev.lines, state),
    })),
  pushEvent: (ev) => {
    if (ev.type === 'agent.thinking') {
      const seatId = ev.seatId ?? ''
      if (!seatId) return
      set((prev) => {
        const next = { ...prev.thinking }
        if (ev.meta?.state === 'stop') delete next[seatId]
        else next[seatId] = Date.now()
        return { thinking: next }
      })
      return
    }
    set((prev) => ({ lines: applyEvent(prev.lines, ev) }))
  },
  setConn: (conn) => set({ conn }),
  setTts: (on) => {
    if (typeof localStorage !== 'undefined') localStorage.setItem('script-play.tts', on ? '1' : '0')
    set({ tts: on })
  },
  reset: () => set({ room: null, state: null, lines: [], thinking: {}, conn: 'idle' }),
}))
