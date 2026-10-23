import type { Assignment } from './client.ts'

export interface Lobby {
  allocating: boolean
  allocationPending: boolean
  revision: number
  ready: Record<string, boolean>
  visibility: 'public' | 'private'
  id: string
  ownerId: string
  participants: string[]
  capacity: number
  status: 'open' | 'matched' | 'ended'
}
export interface MatchTicket { id: string; status: 'queued' | 'matched' | 'ended'; assignment?: Assignment }

/** The operator credential is never sent to this public client. */
export class ControlError extends Error {
  readonly retryable: boolean
  constructor(readonly status: number, message: string) {
    super(message)
    this.retryable = status === 429 || status >= 500
  }
}

export class LobbyClient {
  private token = ''
  private renewal?: ReturnType<typeof setTimeout>
  private sessionEpoch = 0
  private sessionDeadline = 0
  participantId = ''

  constructor(private readonly baseUrl = '/v2/api', private readonly onSessionError?: (error: Error) => void) {
    const url = new URL(baseUrl, location.href)
    if (url.protocol !== 'https:' && !(['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) && url.protocol === 'http:')) {
      throw new Error('Control API requires HTTPS')
    }
  }

  async guest(): Promise<void> {
    const epoch = ++this.sessionEpoch
    const session = await this.request<{ participantId: string; token: string; expiresInSeconds: number }>('/sessions', 'POST', {})
    if (epoch !== this.sessionEpoch) return
    this.token = session.token
    this.participantId = session.participantId
    this.scheduleRenewal(session.expiresInSeconds)
  }

  async renew(): Promise<void> {
    if (!this.token) return
    const epoch = this.sessionEpoch
    const session = await this.request<{ participantId: string; token: string; expiresInSeconds: number }>('/sessions/refresh', 'POST', {})
    if (epoch !== this.sessionEpoch) return
    if (session.participantId !== this.participantId) throw new Error('Renewal changed identity')
    this.token = session.token
    this.scheduleRenewal(session.expiresInSeconds)
  }

  dispose(): void { ++this.sessionEpoch; clearTimeout(this.renewal); this.token = '' }

  private scheduleRenewal(seconds: number): void {
    clearTimeout(this.renewal)
    this.sessionDeadline = performance.now() + seconds * 1000
    const epoch = this.sessionEpoch
    const attempt = () => { void this.renew().catch(error => {
      this.onSessionError?.(error)
      if (epoch === this.sessionEpoch && this.token && performance.now() < this.sessionDeadline - 1000 && !(error instanceof ControlError && !error.retryable)) {
        this.renewal = setTimeout(attempt, 1000)
      }
    }) }
    this.renewal = setTimeout(attempt, Math.max(1000, seconds * 750))
  }

  create(options: { capacity?: number; applicationProtocol: string; visibility?: 'public' | 'private' }): Promise<Lobby> {
    return this.request('/lobbies', 'POST', { capacity: options.capacity ?? 2, applicationProtocol: options.applicationProtocol, visibility: options.visibility ?? 'private' })
  }
  list(applicationProtocol: string, cursor = '', limit = 50): Promise<{ lobbies: Pick<Lobby, 'id' | 'revision' | 'ownerId' | 'capacity'>[]; nextCursor: string }> {
    return this.request(`/lobbies?${new URLSearchParams({ applicationProtocol, cursor, limit: String(limit) })}`)
  }
  get(id: string): Promise<Lobby> { return this.request(`/lobbies/${encodeURIComponent(id)}`) }
  async join(id: string, expectedProtocol?: string): Promise<Lobby> {
    const preview = await this.request<{ revision: number; applicationProtocol: string }>(`/lobbies/${encodeURIComponent(id)}/preview`)
    if (expectedProtocol && preview.applicationProtocol !== expectedProtocol) throw new Error('Unsupported lobby game protocol')
    return this.request(`/lobbies/${encodeURIComponent(id)}/join`, 'POST', { revision: preview.revision })
  }
  async start(id: string): Promise<void> {
    const lobby = await this.get(id)
    return this.request(`/lobbies/${encodeURIComponent(id)}/start`, 'POST', { revision: lobby.revision })
  }
  async ready(id: string, ready: boolean): Promise<Lobby> {
    const lobby = await this.get(id)
    return this.request(`/lobbies/${encodeURIComponent(id)}/ready`, 'POST', { revision: lobby.revision, ready })
  }
  async leaveLobby(id: string): Promise<void> {
    const lobby = await this.get(id)
    await this.request(`/lobbies/${encodeURIComponent(id)}/leave`, 'POST', { revision: lobby.revision })
  }
  assignment(id: string): Promise<Assignment> { return this.request(`/lobbies/${encodeURIComponent(id)}/assignment`) }
  refresh(id: string): Promise<Assignment> { return this.request(`/lobbies/${encodeURIComponent(id)}/assignment`, 'POST', {}) }
  queue(options: { capacity?: number; applicationProtocol: string }): Promise<MatchTicket> { return this.request('/matchmaking', 'POST', { capacity: options.capacity ?? 2, applicationProtocol: options.applicationProtocol }) }
  ticket(id: string): Promise<MatchTicket> { return this.request(`/matchmaking/${encodeURIComponent(id)}`) }
  cancel(id: string): Promise<void> { return this.request(`/matchmaking/${encodeURIComponent(id)}`, 'DELETE') }

  private async request<T>(path: string, method = 'GET', data?: unknown): Promise<T> {
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 5000)
    try {
      const response = await fetch(this.baseUrl + path, {
        method, cache: 'no-store', credentials: 'omit', signal: controller.signal,
        headers: { ...(data === undefined ? {} : { 'Content-Type': 'application/json' }), ...(this.token ? { Authorization: `Bearer ${this.token}` } : {}) },
        ...(data === undefined ? {} : { body: JSON.stringify(data) }),
      })
      if (!response.ok) {
        const problem = await response.json().catch(() => ({ error: 'request failed' }))
        throw new ControlError(response.status, `${response.status}: ${JSON.stringify(problem)}`)
      }
      if (response.status === 204) return undefined as T
      return await response.json() as T
    } finally { clearTimeout(timeout) }
  }
}
