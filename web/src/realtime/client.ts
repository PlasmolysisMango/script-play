import { gameEventSchema, type GameEvent } from '../types/schema'

export type SocketStatus = 'connecting' | 'open' | 'closed'

type Options = {
  code: string
  token: string
  seatId?: string
  /** Mints a short-lived seat ticket; falls back to the session token. */
  fetchTicket?: () => Promise<{ ticket: string } | null>
  onEvent: (ev: GameEvent) => void
  onStatus: (status: SocketStatus) => void
  onError: (message: string) => void
  onResync: () => void
}

/**
 * One websocket per seat. Speaker identity lives on the socket (server side),
 * so the client never has to guess who said what.
 *
 * Reconnects with capped exponential backoff and asks the caller to resync the
 * state snapshot, because events produced while offline are not queued.
 */
export class RoomSocket {
  private ws: WebSocket | null = null
  private attempt = 0
  private stopped = false
  private timer: number | null = null

  constructor(private readonly opts: Options) {}

  connect() {
    this.stopped = false
    void this.open()
  }

  private url(ticket?: string): string {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const params = new URLSearchParams()
    if (ticket) params.set('ticket', ticket)
    else params.set('token', this.opts.token)
    if (this.opts.seatId) params.set('seat', this.opts.seatId)
    return `${proto}//${location.host}/ws/rooms/${this.opts.code}?${params.toString()}`
  }

  private async open() {
    if (this.stopped) return
    this.opts.onStatus('connecting')
    // Prefer a seat ticket: it is scoped to this seat and expires quickly.
    let ticket: string | undefined
    if (this.opts.fetchTicket) {
      try {
        ticket = (await this.opts.fetchTicket())?.ticket
      } catch {
        ticket = undefined
      }
    }
    if (this.stopped) return
    let ws: WebSocket
    try {
      ws = new WebSocket(this.url(ticket))
    } catch {
      this.scheduleReconnect()
      return
    }
    this.ws = ws
    ws.onopen = () => {
      this.attempt = 0
      this.opts.onStatus('open')
      // The server sends no backlog: the caller reloads the snapshot on every
      // (re)connect, which also covers events produced while offline.
      this.opts.onResync()
    }
    ws.onmessage = (msg) => {
      let payload: unknown
      try {
        payload = JSON.parse(String(msg.data))
      } catch {
        return
      }
      if (payload && typeof payload === 'object' && 'type' in payload) {
        const record = payload as Record<string, unknown>
        if (record.type === 'error') {
          this.opts.onError(String(record.message ?? '服务器返回错误'))
          return
        }
        const parsed = gameEventSchema.safeParse(payload)
        if (parsed.success) this.opts.onEvent(parsed.data)
      }
    }
    ws.onclose = () => {
      this.opts.onStatus('closed')
      this.scheduleReconnect()
    }
    ws.onerror = () => {
      ws.close()
    }
  }

  private scheduleReconnect() {
    if (this.stopped) return
    this.attempt += 1
    const delay = Math.min(8000, 500 * 2 ** Math.min(this.attempt, 4))
    if (this.timer !== null) window.clearTimeout(this.timer)
    this.timer = window.setTimeout(() => {
      this.timer = null
      void this.open()
    }, delay)
  }

  send(cmd: { type: string; text?: string; clueId?: string; roleId?: string }) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return false
    this.ws.send(JSON.stringify(cmd))
    return true
  }

  close() {
    this.stopped = true
    if (this.timer !== null) {
      window.clearTimeout(this.timer)
      this.timer = null
    }
    this.ws?.close()
    this.ws = null
  }
}
