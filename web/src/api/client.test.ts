import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, configureApi } from './client'

/** The API client silently rotates an expired access token exactly once. */

type Call = { url: string; auth: string | null }

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

let calls: Call[] = []
let token: string | null = 'old'
let unauthorized = 0
let refreshCalls = 0

function stubFetch(handler: (auth: string | null) => Response) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const auth = new Headers(init?.headers).get('Authorization')
      calls.push({ url: String(input), auth })
      return handler(auth)
    }),
  )
}

beforeEach(() => {
  calls = []
  token = 'old'
  unauthorized = 0
  refreshCalls = 0
  configureApi({
    token: () => token,
    unauthorized: () => {
      unauthorized += 1
    },
    refresh: async () => {
      refreshCalls += 1
      return null
    },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('session refresh', () => {
  it('replays the request once with the rotated access token', async () => {
    stubFetch((auth) => (auth === 'Bearer old' ? jsonResponse({ error: 'expired' }, 401) : jsonResponse({ ok: true })))
    configureApi({
      token: () => token,
      unauthorized: () => {
        unauthorized += 1
      },
      refresh: async () => {
        refreshCalls += 1
        token = 'new'
        return 'new'
      },
    })

    await expect(api.health()).resolves.toEqual({ ok: true })
    expect(refreshCalls).toBe(1)
    expect(calls.map((c) => c.auth)).toEqual(['Bearer old', 'Bearer new'])
    expect(unauthorized).toBe(0)
  })

  it('logs out when the refresh token is rejected', async () => {
    stubFetch(() => jsonResponse({ error: 'expired' }, 401))

    await expect(api.health()).rejects.toBeInstanceOf(ApiError)
    expect(refreshCalls).toBe(1)
    expect(calls).toHaveLength(1)
    expect(unauthorized).toBe(1)
  })

  it('never retries the refresh endpoint itself', async () => {
    stubFetch(() => jsonResponse({ error: 'bad refresh' }, 401))
    configureApi({
      token: () => token,
      unauthorized: () => {
        unauthorized += 1
      },
      refresh: async () => {
        refreshCalls += 1
        return 'new'
      },
    })

    await expect(api.refreshSession('broken')).rejects.toMatchObject({ status: 401 })
    expect(calls).toHaveLength(1)
    expect(refreshCalls).toBe(0)
    expect(unauthorized).toBe(0)
  })
})
