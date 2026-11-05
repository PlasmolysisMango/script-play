import {
  adminRoomSchema,
  adminUserSchema,
  rawMaterialSchema,
  replaySchema,
  roomSchema,
  scriptSummarySchema,
  seatStateSchema,
  userSchema,
  validationErrorSchema,
  type AdminRoom,
  type AdminUser,
  type Persona,
  type RawMaterial,
  type Replay,
  type Room,
  type ScriptSummary,
  type SeatState,
  type User,
  type ValidationError,
} from '../types/schema'

const BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? ''

export class ApiError extends Error {
  status: number
  payload: unknown
  constructor(message: string, status: number, payload?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.payload = payload
  }
}

let tokenProvider: () => string | null = () => null
let onUnauthorized: () => void = () => {}
let onRefresh: () => Promise<string | null> = async () => null

/**
 * The session store registers itself here so every request is authorised.
 * `refresh` trades the stored refresh token for a new access token; returning
 * null means the session is gone for good.
 */
export function configureApi(opts: {
  token: () => string | null
  unauthorized: () => void
  refresh?: () => Promise<string | null>
}) {
  tokenProvider = opts.token
  onUnauthorized = opts.unauthorized
  if (opts.refresh) onRefresh = opts.refresh
}

const REFRESH_PATH = '/api/auth/refresh'

async function request<T>(path: string, init: RequestInit = {}, canRetry = true): Promise<T> {
  const headers = new Headers(init.headers)
  const token = tokenProvider()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (init.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const res = await fetch(`${BASE}${path}`, { ...init, headers })
  if (res.status === 401 && token && path !== REFRESH_PATH) {
    // Access tokens are short-lived, so a 401 usually just means it expired.
    // Refresh silently once and replay the call; only give up after that.
    const fresh = canRetry ? await onRefresh() : null
    if (fresh) return request<T>(path, init, false)
    onUnauthorized()
  }
  const text = await res.text()
  let payload: unknown = undefined
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = text
    }
  }
  if (!res.ok) {
    const message =
      payload && typeof payload === 'object' && 'error' in payload
        ? String((payload as { error: unknown }).error)
        : `请求失败（${res.status}）`
    throw new ApiError(message, res.status, payload)
  }
  return payload as T
}

export type AuthSession = { token: string; refreshToken: string; expiresAt?: string; user: User }

type AuthResponse = { token: string; refreshToken: string; expiresAt?: string; user: unknown }

function withUser(res: AuthResponse): AuthSession {
  return {
    token: res.token,
    refreshToken: res.refreshToken,
    expiresAt: res.expiresAt,
    user: userSchema.parse(res.user),
  }
}

export type RoomStateResponse = { state: SeatState; room: Room }

export type ScriptResponse = { script: Record<string, unknown>; errors?: ValidationError[]; owner?: boolean }

function withRoom(payload: unknown): Room {
  const obj = payload as { room: unknown }
  return roomSchema.parse(obj.room)
}

export const api = {
  async register(body: { username: string; password: string; displayName?: string }): Promise<AuthSession> {
    return withUser(await request<AuthResponse>('/api/auth/register', { method: 'POST', body: JSON.stringify(body) }))
  },
  async login(body: { username: string; password: string }): Promise<AuthSession> {
    return withUser(await request<AuthResponse>('/api/auth/login', { method: 'POST', body: JSON.stringify(body) }))
  },
  /** Exchanges a refresh token for a fresh pair; never uses the retry path. */
  async refreshSession(refreshToken: string): Promise<{ token: string; refreshToken: string; expiresAt?: string }> {
    const res = await request<AuthResponse>(REFRESH_PATH, {
      method: 'POST',
      body: JSON.stringify({ refreshToken }),
    })
    return { token: res.token, refreshToken: res.refreshToken, expiresAt: res.expiresAt }
  },
  async me(): Promise<User> {
    return userSchema.parse(await request<unknown>('/api/me'))
  },
  async personas(): Promise<Persona[]> {
    const res = await request<{ personas: Persona[] }>('/api/personas')
    return res.personas
  },
  async providers(): Promise<{ providers: string[]; default: string; qoder?: Record<string, string>; openai?: Record<string, string> }> {
    return request('/api/providers')
  },
  async health(): Promise<{ ok: boolean; providers: string[]; default: string; floorTimeoutSec?: number; agentTimeoutSec?: number }> {
    return request('/api/health')
  },
  async listScripts(): Promise<ScriptSummary[]> {
    const res = await request<{ scripts: unknown[] }>('/api/scripts')
    return (res.scripts ?? []).map((s) => scriptSummarySchema.parse(s))
  },
  async getScript(id: string): Promise<ScriptResponse> {
    return request<ScriptResponse>(`/api/scripts/${id}`)
  },
  async createScript(body: unknown): Promise<ScriptResponse> {
    const res = await request<{ script: Record<string, unknown>; errors?: unknown[] }>('/api/scripts', {
      method: 'POST',
      body: JSON.stringify(body),
    })
    return { ...res, errors: parseErrors(res.errors) }
  },
  async updateScript(id: string, body: unknown): Promise<ScriptResponse> {
    const res = await request<{ script: Record<string, unknown>; errors?: unknown[] }>(`/api/scripts/${id}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    })
    return { ...res, errors: parseErrors(res.errors) }
  },
  async publishScript(id: string): Promise<ScriptResponse> {
    const res = await request<{ script: Record<string, unknown>; errors?: unknown[] }>(`/api/scripts/${id}/publish`, {
      method: 'POST',
    })
    return { ...res, errors: parseErrors(res.errors) }
  },
  /** Published versions are frozen; this copies one back into an editable draft. */
  async forkScript(id: string): Promise<ScriptResponse> {
    const res = await request<{ script: Record<string, unknown>; errors?: unknown[] }>(`/api/scripts/${id}/draft`, {
      method: 'POST',
    })
    return { ...res, errors: parseErrors(res.errors) }
  },
  async deleteScript(id: string): Promise<void> {
    await request(`/api/scripts/${id}`, { method: 'DELETE' })
  },
  /**
   * Stores one picture beside the script and returns the relative URL to embed.
   * The URL is what clue.imageUrl (or a markdown image link) holds, so the same
   * body works locally, behind a proxy and in an exported copy.
   */
  async uploadScriptImage(id: string, file: File): Promise<{ url: string; name: string; size: number; mime: string }> {
    const form = new FormData()
    form.append('file', file)
    return request(`/api/scripts/${id}/media`, { method: 'POST', body: form })
  },
  async seedSample(): Promise<ScriptResponse> {
    return request<ScriptResponse>('/api/scripts/sample', { method: 'POST' })
  },
  /** Uploaded originals: stored verbatim, never parsed. */
  async materials(): Promise<{ materials: RawMaterial[]; dir: string }> {
    const res = await request<{ materials: unknown[]; dir?: string }>('/api/admin/materials')
    return {
      materials: (res.materials ?? []).map((m) => rawMaterialSchema.parse(m)),
      dir: res.dir ?? '',
    }
  },
  async uploadMaterials(files: File[]): Promise<RawMaterial[]> {
    const form = new FormData()
    for (const f of files) form.append('files', f)
    const res = await request<{ materials: unknown[] }>('/api/admin/materials', { method: 'POST', body: form })
    return (res.materials ?? []).map((m) => rawMaterialSchema.parse(m))
  },
  async deleteMaterial(id: string): Promise<void> {
    await request<unknown>(`/api/admin/materials/${id}`, { method: 'DELETE' })
  },
  async adminUsers(): Promise<AdminUser[]> {
    const res = await request<{ users: unknown[] }>('/api/admin/users')
    return (res.users ?? []).map((u) => adminUserSchema.parse(u))
  },
  /** The server returns the new password once; it only keeps a hash. */
  async adminResetPassword(id: string, password?: string): Promise<{ password: string; generated: boolean }> {
    return request<{ password: string; generated: boolean }>(`/api/admin/users/${id}/password`, {
      method: 'POST',
      body: JSON.stringify(password ? { password } : {}),
    })
  },
  async adminRooms(): Promise<AdminRoom[]> {
    const res = await request<{ rooms: unknown[] }>('/api/admin/rooms')
    return (res.rooms ?? []).map((r) => adminRoomSchema.parse(r))
  },
  async adminCloseRoom(code: string): Promise<void> {
    await request<unknown>(`/api/admin/rooms/${code}/close`, { method: 'POST' })
  },
  async adminDeleteRoom(code: string): Promise<void> {
    await request<unknown>(`/api/admin/rooms/${code}`, { method: 'DELETE' })
  },
  async listRooms(): Promise<Room[]> {
    const res = await request<{ rooms: unknown[] }>('/api/rooms')
    return (res.rooms ?? []).map((r) => roomSchema.parse(r))
  },
  async createRoom(body: {
    name?: string
    scriptId: string
    maxSeats?: number
    allowSpectator?: boolean
    speechSeconds?: number
    autoSkip?: boolean
  }): Promise<Room> {
    return withRoom(await request('/api/rooms', { method: 'POST', body: JSON.stringify(body) }))
  },
  async getRoom(code: string): Promise<Room> {
    return withRoom(await request(`/api/rooms/${code}`))
  },
  async joinRoom(
    code: string,
    body: { displayName?: string; roleId?: string; personaId?: string; spectator?: boolean },
  ): Promise<Room> {
    return withRoom(await request(`/api/rooms/${code}/join`, { method: 'POST', body: JSON.stringify(body) }))
  },
  async addSeat(
    code: string,
    body: { kind?: string; displayName?: string; roleId?: string; personaId?: string },
  ): Promise<Room> {
    return withRoom(await request(`/api/rooms/${code}/seats`, { method: 'POST', body: JSON.stringify(body) }))
  },
  async patchSeat(
    code: string,
    seatId: string,
    body: { displayName?: string; roleId?: string; personaId?: string; ready?: boolean },
  ): Promise<Room> {
    return withRoom(await request(`/api/rooms/${code}/seats/${seatId}`, { method: 'PATCH', body: JSON.stringify(body) }))
  },
  async deleteSeat(code: string, seatId: string): Promise<Room> {
    return withRoom(await request(`/api/rooms/${code}/seats/${seatId}`, { method: 'DELETE' }))
  },
  async startRoom(code: string): Promise<Room> {
    return withRoom(await request(`/api/rooms/${code}/start`, { method: 'POST' }))
  },
  /** Short-lived ticket that binds one websocket to this account's seat. */
  async seatTicket(code: string): Promise<{ ticket: string; seatId: string; expiresAt: string }> {
    return request(`/api/rooms/${code}/seat-ticket`)
  },
  async roomState(code: string, seatId?: string): Promise<RoomStateResponse> {
    const query = seatId ? `?seatId=${encodeURIComponent(seatId)}` : ''
    const res = await request<{ state: unknown; room: unknown }>(`/api/rooms/${code}/state${query}`)
    return { state: seatStateSchema.parse(res.state), room: roomSchema.parse(res.room) }
  },
  async replay(code: string): Promise<Replay> {
    return replaySchema.parse(await request<unknown>(`/api/rooms/${code}/replay`))
  },
}

function parseErrors(raw: unknown[] | undefined): ValidationError[] | undefined {
  if (!raw) return undefined
  return raw.map((e) => validationErrorSchema.parse(e))
}
