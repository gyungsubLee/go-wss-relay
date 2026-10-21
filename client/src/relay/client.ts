import { create, fromBinary, toBinary, type MessageInitShape } from '@bufbuild/protobuf'
import {
  ClientEnvelopeSchema, ServerEnvelopeSchema,
  type Peer, type PublishedState, type RelayedEvent,
  type RoomSnapshot, type ServerEnvelope, type Welcome,
} from './gen/relay/v2/relay_pb.ts'

export type ConnectionState = 'idle' | 'connecting' | 'active' | 'reconnecting' | 'suspended' | 'closed'
export interface Assignment {
  matchId: string
  subprotocol: string
  ticketExpiresAt: string
  roomId: string
  roomInstanceId: string
  participantId: string
  nodeId: string
  websocketUrl: string
  applicationProtocol: string
  connectTicket: string
}
export interface RelayOptions {
  applicationProtocol?: string
  onStatus?: (status: ConnectionState) => void
  onMessage?: (message: ServerEnvelope) => void
  onState?: (state: PublishedState) => void
  onEvent?: (event: RelayedEvent) => void
  onError?: (error: Error) => void
  /** Re-authorizes the same participant through the authenticated control API. */
  refreshAssignment?: () => Promise<Assignment>
}

/** One instance owns one room session; game motion and payloads remain with the caller. */
export class RelayClient {
  readonly peers = new Map<string, Peer>()
  readonly states = new Map<string, PublishedState>()
  status: ConnectionState = 'idle'
  welcome?: Welcome
  rttMs?: number
  private assignment?: Assignment
  private socket?: WebSocket
  private sequence = 0
  private serverSequence = 0
  private resumeToken = ''
  private pendingState?: Uint8Array
  private latestState?: Uint8Array
  private flushTimer?: ReturnType<typeof setInterval>
  private retryTimer?: ReturnType<typeof setTimeout>
  private heartbeatTimer?: ReturnType<typeof setInterval>
  private authTimer?: ReturnType<typeof setTimeout>
  private disconnectedAt = 0
  private lastMessageAt = 0
  private retry = 0
  private stopped = true
  private suspended = false
  private refreshing = false
  private refreshNeeded = false
  private peerRevisions = new Map<string, bigint>()
  private snapshotRevision = 0n
  private ping?: { nonce: number; sentAt: number }
  private nonce = 0
  private eventTimes: number[] = []

  constructor(private readonly options: RelayOptions = {}) {}

  connect(assignment: Assignment): void {
    validateAssignment(assignment)
    if (this.options.applicationProtocol && assignment.applicationProtocol !== this.options.applicationProtocol) throw new Error('Unsupported game protocol')
    this.close()
    this.assignment = { ...assignment }
    this.stopped = false
    this.suspended = false
    this.resumeToken = ''
    this.welcome = undefined
    this.latestState = undefined
    this.pendingState = undefined
    this.peers.clear()
    this.states.clear()
    this.peerRevisions.clear()
    this.snapshotRevision = 0n
    this.disconnectedAt = 0
    this.retry = 0
    this.open()
  }

  /** Latest full state replaces the unsent state; never enqueue a chain of deltas. */
  publishState(payload: Uint8Array): void {
    const limit = this.welcome?.limits?.maxStatePayloadBytes ?? 4096
    if (payload.length === 0 || payload.length > limit) throw new Error('Invalid state payload size')
    this.latestState = payload.slice()
    if (this.status === 'active') this.pendingState = this.latestState
  }

  /** Events are not buffered offline and are never replayed after reconnect. */
  publishEvent(kind: number, payload: Uint8Array): number {
    if (this.status !== 'active' || this.socket?.readyState !== WebSocket.OPEN) throw new Error('Relay is not active')
    if (!Number.isInteger(kind) || kind < 1 || kind > 65535 || payload.length > (this.welcome?.limits?.maxEventPayloadBytes ?? 8192)) {
      throw new Error('Invalid event')
    }
    const now = performance.now()
    this.eventTimes = this.eventTimes.filter(time => now - time < 1000)
    if (this.eventTimes.length >= (this.welcome?.limits?.maxEventsPerSecond ?? 5)) throw new Error('Event rate exceeded')
    if (!this.send({ case: 'event', value: { kind, payload } })) throw new Error('Relay send buffer is full')
    this.eventTimes.push(now)
    return this.sequence
  }

  resync(): boolean {
    return this.status === 'active' && this.send({ case: 'resync', value: { lastRoomRevision: this.snapshotRevision } })
  }

  suspend(): void {
    if (this.stopped || this.suspended) return
    this.suspended = true
    this.stopTimers()
    // Keep resume authority in memory; background sockets and timers are not trusted.
    this.socket?.close(1000, 'app suspended')
    this.setStatus('suspended')
  }

  resume(): void {
    if (this.stopped || !this.suspended) return
    this.suspended = false
    this.open()
  }

  /** Explicit reconnect is also useful after a browser network-change notification. */
  reconnect(): void {
    if (this.stopped || this.suspended) return
    this.socket?.close(1000, 'reconnect')
    this.open()
  }

  leave(): void {
    if (this.status === 'active') this.send({ case: 'leave', value: {} })
    this.stopped = true
    this.stopTimers()
    const socket = this.socket
    // Give the server a short bounded opportunity to acknowledge the leave.
    setTimeout(() => socket?.close(1000, 'left'), 250)
    this.resumeToken = ''
    this.setStatus('closed')
  }

  close(): void {
    this.stopped = true
    this.stopTimers()
    const socket = this.socket
    this.socket = undefined
    socket?.close(1000, 'closed')
    this.resumeToken = ''
    this.setStatus('closed')
  }

  private open(): void {
    const assignment = this.assignment
    if (!assignment || this.stopped || this.suspended) return
    this.stopTimers()
    const previous = this.socket
    this.socket = undefined
    previous?.close(1000, 'replaced')
    const socket = new WebSocket(assignment.websocketUrl, 'relay.v2')
    this.socket = socket
    socket.binaryType = 'arraybuffer'
    this.sequence = 0
    this.serverSequence = 0
    this.setStatus(this.welcome ? 'reconnecting' : 'connecting')
    this.authTimer = setTimeout(() => socket.close(1000, 'authentication timeout'), 5000)
    socket.onopen = () => {
      if (this.socket !== socket) return
      if (socket.protocol !== 'relay.v2') { this.fail(new Error('Wrong WebSocket subprotocol')); return }
      const credential = this.resumeToken
        ? { case: 'resumeToken' as const, value: this.resumeToken }
        : { case: 'connectTicket' as const, value: assignment.connectTicket }
      const connect = create(ClientEnvelopeSchema, {
        protocolRevision: 2, sequence: 0,
        body: { case: 'connect', value: { applicationProtocol: assignment.applicationProtocol, credential } },
      })
      socket.send(toBinary(ClientEnvelopeSchema, connect))
    }
    socket.onmessage = event => {
      if (this.socket !== socket || this.stopped) return
      try {
        if (!(event.data instanceof ArrayBuffer) || event.data.byteLength > 131072) throw new Error('Invalid server message')
        const envelope = fromBinary(ServerEnvelopeSchema, new Uint8Array(event.data))
        if (envelope.protocolRevision !== 2 || envelope.sequence <= this.serverSequence || !envelope.body.case) {
          throw new Error('Invalid server version or sequence')
        }
        this.serverSequence = envelope.sequence
        this.lastMessageAt = performance.now()
        this.receive(envelope)
      } catch (error) {
        this.fail(error instanceof Error ? error : new Error('Relay decoding failed'))
      }
    }
    socket.onclose = () => {
      if (this.socket !== socket || this.stopped) return
      this.stopTimers()
      this.pendingState = undefined
      this.eventTimes = []
      if (!this.disconnectedAt) this.disconnectedAt = performance.now()
      if (this.suspended) return
      this.setStatus('reconnecting')
      this.scheduleRetry()
    }
    // Browsers intentionally hide handshake errors; onclose owns retry scheduling.
    socket.onerror = () => {}
  }

  private receive(envelope: ServerEnvelope): void {
    const body = envelope.body
    if (body.case !== 'welcome' && body.case !== 'error' && this.status !== 'active') throw new Error('Welcome required')
    switch (body.case) {
      case 'welcome': {
        const welcome = body.value
        const assignment = this.assignment!
        if (this.status === 'active' || welcome.roomInstanceId !== assignment.roomInstanceId || welcome.roomId !== assignment.roomId ||
          welcome.participantId !== assignment.participantId || welcome.nodeId !== assignment.nodeId ||
          welcome.applicationProtocol !== assignment.applicationProtocol || !welcome.generation || !welcome.limits || !welcome.snapshot ||
          !welcome.resumeToken || this.welcome && welcome.generation <= this.welcome.generation) throw new Error('Invalid Welcome identity')
        this.welcome = welcome
        this.resumeToken = welcome.resumeToken
        this.refreshNeeded = false
        this.disconnectedAt = 0
        this.retry = 0
        clearTimeout(this.authTimer)
        this.applySnapshot(welcome.snapshot)
        this.setStatus('active')
        this.pendingState = this.latestState
        this.flushTimer = setInterval(() => this.flushState(), Math.max(50, 1000 / Math.max(1, welcome.limits.maxStateUpdatesPerSecond)))
        this.heartbeatTimer = setInterval(() => this.heartbeat(), welcome.limits.heartbeatIntervalMs || 15000)
        break
      }
      case 'stateBatch':
        for (const state of body.value.states) this.applyState(state)
        break
      case 'syncSnapshot':
        if (!body.value.snapshot) throw new Error('Snapshot missing')
        this.applySnapshot(body.value.snapshot)
        break
      case 'peerJoined':
      case 'peerStatusChanged': {
        const { peer, roomRevision } = body.value
        if (!peer || roomRevision <= (this.peerRevisions.get(peer.participantId) ?? this.snapshotRevision)) break
        const old = this.peers.get(peer.participantId)
        if (old && old.generation > peer.generation) break
        if (!old || old.generation !== peer.generation) this.states.delete(peer.participantId)
        this.peers.set(peer.participantId, peer)
        this.peerRevisions.set(peer.participantId, roomRevision)
        break
      }
      case 'peerLeft': {
        const { participantId, generation, roomRevision } = body.value
        const peer = this.peers.get(participantId)
        if (roomRevision <= (this.peerRevisions.get(participantId) ?? this.snapshotRevision) || peer && peer.generation > generation) break
        this.peers.delete(participantId)
        this.states.delete(participantId)
        this.peerRevisions.set(participantId, roomRevision)
        break
      }
      case 'event': {
        const event = body.value
        const peer = this.peers.get(event.participantId)
        if (peer?.generation === event.generation && event.roomRevision > this.snapshotRevision) this.options.onEvent?.(event)
        break
      }
      case 'pong':
        if (this.ping?.nonce === body.value.nonce) this.rttMs = performance.now() - this.ping.sentAt
        break
      case 'error':
        this.options.onError?.(new Error(`Relay ${body.value.code}: ${body.value.message}`))
        if (body.value.fatal) {
          if (body.value.code === 4 && this.options.refreshAssignment) {
            this.resumeToken = ''
            this.refreshNeeded = true
            this.socket?.close(1000, 'reauthorize')
          } else if (body.value.code === 10 || body.value.code === 12) {
            this.socket?.close(1000, 'retryable relay error')
          } else this.close()
        }
        break
      case 'left':
        this.close()
        break
    }
    this.options.onMessage?.(envelope)
  }

  private applySnapshot(snapshot: RoomSnapshot): void {
    if (snapshot.roomRevision < this.snapshotRevision) return
    this.peers.clear()
    this.states.clear()
    this.peerRevisions.clear()
    this.snapshotRevision = snapshot.roomRevision
    for (const peer of snapshot.members) {
      this.peers.set(peer.participantId, peer)
      this.peerRevisions.set(peer.participantId, snapshot.roomRevision)
    }
    for (const state of snapshot.states) this.applyState(state)
  }

  private applyState(state: PublishedState): void {
    const peer = this.peers.get(state.participantId)
    const old = this.states.get(state.participantId)
    if (!peer || peer.generation !== state.generation || old && old.clientSequence >= state.clientSequence) return
    // The generation check prevents stale batches from recreating a departed object.
    this.states.set(state.participantId, state)
    this.options.onState?.(state)
  }

  private flushState(): void {
    if (this.pendingState && this.send({ case: 'stateUpdate', value: { payload: this.pendingState } })) this.pendingState = undefined
  }

  private heartbeat(): void {
    if (performance.now() - this.lastMessageAt > (this.welcome?.limits?.heartbeatTimeoutMs ?? 45000)) {
      this.socket?.close(1000, 'heartbeat timeout')
      return
    }
    if (this.send({ case: 'ping', value: { nonce: ++this.nonce, clientTimeUnixMs: BigInt(Date.now()) } })) {
      this.ping = { nonce: this.nonce, sentAt: performance.now() }
    }
    // Full republish lets a newly joined peer receive unchanged state too.
    this.pendingState = this.latestState
  }

  private send(body: MessageInitShape<typeof ClientEnvelopeSchema>['body']): boolean {
    const socket = this.socket
    if (this.status !== 'active' || !socket || socket.readyState !== WebSocket.OPEN || socket.bufferedAmount > 65536) return false
    if (this.sequence === 0xffffffff) { this.fail(new Error('Sequence exhausted')); return false }
    const message = create(ClientEnvelopeSchema, { protocolRevision: 2, sequence: ++this.sequence, body })
    socket.send(toBinary(ClientEnvelopeSchema, message))
    return true
  }

  private scheduleRetry(): void {
    const grace = this.welcome?.limits?.resumeGraceMs ?? 30000
    if (this.retry >= 8 || this.disconnectedAt && performance.now() - this.disconnectedAt >= grace) {
      this.fail(new Error('Reconnect grace expired; rejoin the match'))
      return
    }
    const delay = Math.min(5000, 250 * 2 ** this.retry++) * (0.75 + Math.random() * 0.5)
    this.retryTimer = setTimeout(() => { void this.retryConnection() }, delay)
  }

  private async retryConnection(): Promise<void> {
    if (this.stopped || this.suspended || this.refreshing) return
    if (this.refreshNeeded && this.options.refreshAssignment) {
      this.refreshing = true
      try {
        const assignment = await this.options.refreshAssignment()
        validateAssignment(assignment)
        if (this.stopped || this.suspended) return
        if (assignment.roomInstanceId !== this.assignment?.roomInstanceId || assignment.participantId !== this.assignment.participantId ||
          assignment.websocketUrl !== this.assignment.websocketUrl || assignment.nodeId !== this.assignment.nodeId ||
          assignment.roomId !== this.assignment.roomId || assignment.applicationProtocol !== this.assignment.applicationProtocol) throw new Error('Room authority changed')
        this.assignment = assignment
        this.refreshNeeded = false
      } catch (error) {
        this.options.onError?.(error instanceof Error ? error : new Error('Reauthorization failed'))
        if (error instanceof Error && 'retryable' in error && error.retryable === false) { this.close(); return }
        this.scheduleRetry()
        return
      } finally {
        this.refreshing = false
      }
    }
    this.open()
  }

  private fail(error: Error): void { this.options.onError?.(error); this.close() }

  private stopTimers(): void {
    clearInterval(this.flushTimer)
    clearInterval(this.heartbeatTimer)
    clearTimeout(this.retryTimer)
    clearTimeout(this.authTimer)
  }

  private setStatus(status: ConnectionState): void {
    if (this.status === status) return
    this.status = status
    this.options.onStatus?.(status)
  }
}

function validateAssignment(assignment: Assignment): void {
  const url = new URL(assignment.websocketUrl)
  const loopback = ['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname)
  if ((url.protocol !== 'wss:' && !(loopback && url.protocol === 'ws:')) || url.pathname !== '/v2/relay' ||
    url.username || url.password || url.search || url.hash || !assignment.roomInstanceId || !assignment.roomId ||
    !assignment.participantId || !assignment.nodeId || !assignment.applicationProtocol || !assignment.connectTicket) {
    throw new Error('Invalid relay assignment')
  }
}
