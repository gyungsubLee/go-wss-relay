// Verified local CA is required. Usage: sdk-load.ts [base] [participants] [roomSize] [payload] [hz] [seconds] [out]
// This check fails if public allocation, SDK state/event delivery, heartbeat, or generation recovery breaks.
import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { RelayClient } from '../../../../client/src/relay/client.ts'
import { LobbyClient, ControlError, type MatchTicket } from '../../../../client/src/relay/api.ts'

const [base = 'https://localhost:19443', countArg = '200', roomArg = '16', payloadArg = '679', hzArg = '20', secondsArg = '30',
  output = new URL('./sdk-load.json', import.meta.url).pathname] = process.argv.slice(2)
const count = Number(countArg), roomSize = Number(roomArg), payloadBytes = Number(payloadArg)
const hz = Number(hzArg), seconds = Number(secondsArg)
assert.ok([count, roomSize, payloadBytes, hz, seconds].every(Number.isInteger), 'profile values must be integers')
assert.ok(count >= 2 && count <= 1000 && roomSize >= 2 && roomSize <= 16 && count % roomSize !== 1, 'invalid room profile')
assert.ok(payloadBytes >= 8 && payloadBytes <= 4096 && hz >= 1 && hz <= 20 && seconds >= 20 && seconds <= 600, 'invalid traffic profile')
const baseURL = new URL(base)
assert.ok(baseURL.protocol === 'https:' && ['localhost', '127.0.0.1', '[::1]'].includes(baseURL.hostname), 'use a local HTTPS node')
Object.defineProperty(globalThis, 'location', { configurable: true, value: { href: baseURL.href } })

type Phase = 'setup' | 'warmup' | 'steady' | 'capacity' | 'preReconnectDrain' | 'reconnect' | 'afterReconnect' | 'finalDrain' | 'cleanup'
type Samples = { seen: number; maxMs: number; values: number[] }
type Participant = {
  api: LobbyClient
  sdk: RelayClient
  id: string
  received: number
  pongs: number
  receivedByPeer: Map<string, number>
  recoveredMs?: number
}
type Room = { members: Participant[]; ids: Set<string>; setupMs: number }
type PendingEvent = { room: Room; sender: Participant; received: Set<string>; accepted: boolean; sentAt: number }

const clients: Participant[] = [], apis: LobbyClient[] = [], rooms: Room[] = []
const extras: { api: LobbyClient; ticket?: MatchTicket }[] = []
const errors: string[] = [], statusTransitions: Record<string, number> = {}, httpStatuses: Record<string, number> = {}
const errorDetails: { phase: Phase; elapsedMs: number; message: string }[] = []
const metricSnapshots: { label: string; phase: Phase; elapsedMs: number; stats: Record<string, number> }[] = []
const stateCounts: Partial<Record<Phase, number>> = {}, activeMinimum: Partial<Record<Phase, number>> = {}
const latency = { steady: { seen: 0, maxMs: 0, values: [] } as Samples, afterReconnect: { seen: 0, maxMs: 0, values: [] } as Samples }
const eventLatency: Samples = { seen: 0, maxMs: 0, values: [] }
const pendingEvents = new Map<number, PendingEvent>(), pendingReceipts = new Map<string, number>()
const capacityResponses: { method: string; path: string; httpStatus: number; matchStatus?: string; hasAssignment?: boolean }[] = []
const extraCleanup: { status: string; httpStatus?: number }[] = []
let phase: Phase = 'setup', errorCount = 0, stateSeen = 0, cutAt = 0, captureCapacity = false
let eventsSent = 0, eventDeliveries = 0, expectedEventDeliveries = 0, eventAccepted = 0, nextEvent = 0
let publishTimer: ReturnType<typeof setInterval> | undefined, eventTimer: ReturnType<typeof setInterval> | undefined
let steadyMs = 0, setupMs = 0, reconnectMs = 0, finalActive = 0, passed = false
const startedAt = performance.now(), applicationProtocol = `sdk-load-${crypto.randomUUID().slice(0, 8)}.v1`
const originalFetch = globalThis.fetch
const operatorToken = (await readFile(new URL('../../../../.cache/live-load-20261023/operator.token', import.meta.url), 'utf8')).trim()

// Observe the real HTTP boundary. Evidence deliberately contains no tokens or assignments.
globalThis.fetch = async (input, init) => {
  const response = await originalFetch(input, init)
  httpStatuses[String(response.status)] = (httpStatuses[String(response.status)] ?? 0) + 1
  if (captureCapacity) {
    const url = new URL(typeof input === 'string' || input instanceof URL ? input : input.url)
    const body = await response.clone().json().catch(() => ({})) as { status?: string; assignment?: unknown }
    capacityResponses.push({
      method: init?.method ?? 'GET', path: url.pathname.replace(/\/matchmaking\/[^/]+$/, '/matchmaking/:ticket'),
      httpStatus: response.status, ...(body.status ? { matchStatus: body.status, hasAssignment: Boolean(body.assignment) } : {}),
    })
  }
  return response
}

function recordError(error: unknown): void {
  errorCount++
  const message = error instanceof Error ? error.message : String(error)
  if (errors.length < 100) {
    errors.push(`[${phase}] ${message}`)
    errorDetails.push({ phase, elapsedMs: performance.now() - startedAt, message })
  }
}
async function captureMetrics(label: string): Promise<void> {
  try {
    const response = await originalFetch('http://127.0.0.1:19092/v2/status', {
      headers: { Authorization: `Bearer ${operatorToken}` }, signal: AbortSignal.timeout(5000),
    })
    assert.ok(response.ok, `metric snapshot HTTP ${response.status}`)
    const stats = await response.json() as Record<string, number>
    metricSnapshots.push({ label, phase, elapsedMs: performance.now() - startedAt, stats })
  } catch (error) {
    recordError(error)
  }
}
async function enterPhase(next: Phase): Promise<void> {
  phase = next
  await captureMetrics(`enter-${next}`)
}
const sleep = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms))
async function until(check: () => boolean | Promise<boolean>, label: string, timeoutMs = 25000): Promise<void> {
  const deadline = performance.now() + timeoutMs
  while (!await check()) {
    assert.ok(performance.now() < deadline, `timed out: ${label}; ${errors.slice(0, 3).join('; ')}`)
    await sleep(25)
  }
}
function observe(samples: Samples, delayMs: number): void {
  // ponytail: Keep the latest 65536 samples; use interval histograms for longer distributed runs.
  samples.maxMs = Math.max(samples.maxMs, delayMs)
  samples.values[samples.seen++ % 65536] = delayMs
}
function summarize(samples: Samples) {
  const sorted = samples.values.toSorted((a, b) => a - b)
  const percentile = (q: number) => sorted.length ? sorted[Math.ceil(sorted.length * q) - 1] : null
  return { observations: samples.seen, sampleCount: sorted.length, p95Ms: percentile(.95), p99Ms: percentile(.99), maxMs: samples.maxMs }
}
function collectEvent(id: number): void {
  const event = pendingEvents.get(id)!
  if (event.accepted && event.received.size === event.room.members.length - 1) pendingEvents.delete(id)
}
function assertFresh(generation: number, newerThan = 0): void {
  const now = performance.now()
  for (const room of rooms) {
    for (const member of room.members) {
      assert.equal(member.sdk.status, 'active', 'participant is not active')
      assert.equal(member.sdk.welcome?.generation, generation, 'unexpected generation')
      assert.equal(member.sdk.peers.size, room.members.length, 'peer roster is incomplete')
      for (const peer of room.members) {
        assert.equal(member.sdk.peers.get(peer.id)?.generation, generation, 'stale peer generation')
        if (peer === member) continue
        const state = member.sdk.states.get(peer.id)
        assert.ok(state && state.generation === generation, 'missing or stale peer state')
        const sentAt = new DataView(state.payload.buffer, state.payload.byteOffset, state.payload.byteLength).getFloat64(0, true)
        assert.ok(sentAt >= newerThan && now - sentAt < 2000, 'peer state is not recent')
        assert.ok((member.receivedByPeer.get(peer.id) ?? 0) > 0, 'peer sent no traffic')
      }
      assert.ok([...member.sdk.states.values()].every(state => state.generation === generation), 'stale state survived reconnect')
    }
  }
}

try {
  await captureMetrics('before-setup')
  // Connect each matched group promptly so its 30-second initial ticket does not expire during setup.
  for (let offset = 0; offset < count; offset += roomSize) {
    const roomStarted = performance.now(), size = Math.min(roomSize, count - offset)
    const room: Room = { members: [], ids: new Set(), setupMs: 0 }
    const queued: MatchTicket[] = []
    rooms.push(room)
    for (let index = 0; index < size; index++) {
      const api = new LobbyClient(baseURL.origin + '/v2/api', recordError)
      apis.push(api)
      await api.guest()
      queued.push(await api.queue({ capacity: size, applicationProtocol }))
      const member: Participant = { api, sdk: undefined as unknown as RelayClient, id: api.participantId, received: 0, pongs: 0, receivedByPeer: new Map() }
      room.members.push(member)
      room.ids.add(member.id)
      clients.push(member)
      await sleep(60)
    }
    for (let index = 0; index < size; index++) {
      const member = room.members[index]
      let ticket = queued[index]
      await until(async () => {
        ticket = await member.api.ticket(ticket.id)
        return ticket.status === 'matched' && Boolean(ticket.assignment)
      }, 'public matchmaking assignment')
      const sdk = new RelayClient({
        applicationProtocol, refreshAssignment: () => member.api.refresh(ticket.assignment!.roomId), onError: recordError,
        onStatus(status) {
          statusTransitions[status] = (statusTransitions[status] ?? 0) + 1
          if (status === 'active' && cutAt && sdk.welcome?.generation === 2 && member.recoveredMs === undefined) member.recoveredMs = performance.now() - cutAt
          if (['steady', 'capacity', 'preReconnectDrain', 'afterReconnect', 'finalDrain'].includes(phase) && ['closed', 'suspended', 'reconnecting'].includes(status)) recordError(`unexpected ${status} during ${phase}`)
        },
        onState(state) {
          if (state.participantId === member.id) return
          if (!room.ids.has(state.participantId) || state.payload.byteLength !== payloadBytes) { recordError('invalid peer state identity or payload'); return }
          const sentAt = new DataView(state.payload.buffer, state.payload.byteOffset, state.payload.byteLength).getFloat64(0, true)
          const delayMs = performance.now() - sentAt
          if (!Number.isFinite(delayMs) || delayMs < 0) { recordError('invalid state timestamp'); return }
          member.received++
          member.receivedByPeer.set(state.participantId, (member.receivedByPeer.get(state.participantId) ?? 0) + 1)
          stateCounts[phase] = (stateCounts[phase] ?? 0) + 1
          if ((phase === 'steady' || phase === 'afterReconnect') && ++stateSeen % 16 === 0) observe(latency[phase], delayMs)
        },
        onEvent(event) {
          if (event.payload.byteLength !== 4) { recordError('invalid event payload'); return }
          const id = new DataView(event.payload.buffer, event.payload.byteOffset, 4).getUint32(0, true)
          const pending = pendingEvents.get(id)
          if (!pending || pending.room !== room || pending.sender.id !== event.participantId || pending.sender === member || pending.received.has(member.id)) { recordError('missing, duplicate, or cross-room event'); return }
          pending.received.add(member.id)
          eventDeliveries++
          observe(eventLatency, performance.now() - pending.sentAt)
          collectEvent(id)
        },
        onMessage(message) {
          if (message.body.case === 'pong') member.pongs++
          if (message.body.case !== 'eventAccepted') return
          const key = `${member.id}:${message.body.value.clientSequence}`, id = pendingReceipts.get(key)
          if (id === undefined) { recordError('unexpected or duplicate event receipt'); return }
          pendingEvents.get(id)!.accepted = true
          pendingReceipts.delete(key)
          eventAccepted++
          collectEvent(id)
        },
      })
      member.sdk = sdk
      sdk.connect(ticket.assignment!)
    }
    await until(() => room.members.every(member => member.sdk.status === 'active'), 'room SDK connections')
    room.setupMs = performance.now() - roomStarted
  }
  setupMs = performance.now() - startedAt
  assert.equal(clients.length, count)
  publishTimer = setInterval(() => {
    const active = clients.filter(member => member.sdk.status === 'active')
    activeMinimum[phase] = Math.min(activeMinimum[phase] ?? count, active.length)
    for (const member of active) {
      const payload = new Uint8Array(payloadBytes)
      new DataView(payload.buffer).setFloat64(0, performance.now(), true)
      try { member.sdk.publishState(payload) } catch (error) { recordError(error) }
    }
  }, 1000 / hz)
  eventTimer = setInterval(() => {
    if (!['warmup', 'steady', 'capacity', 'afterReconnect'].includes(phase)) return
    for (const room of rooms) {
      if (!room.members.every(member => member.sdk.status === 'active')) continue
      const sender = room.members[0], id = ++nextEvent, payload = new Uint8Array(4)
      new DataView(payload.buffer).setUint32(0, id, true)
      try {
        const sequence = sender.sdk.publishEvent(1, payload)
        pendingEvents.set(id, { room, sender, received: new Set(), accepted: false, sentAt: performance.now() })
        pendingReceipts.set(`${sender.id}:${sequence}`, id)
        eventsSent++
        expectedEventDeliveries += room.members.length - 1
      } catch (error) { recordError(error) }
    }
  }, 1000)
  await enterPhase('warmup')
  await sleep(3000)
  assertFresh(1)
  await enterPhase('steady')
  const steadyStarted = performance.now()
  await sleep(seconds * 1000)
  steadyMs = performance.now() - steadyStarted
  assertFresh(1)
  assert.ok(clients.every(member => member.pongs > 0), 'heartbeat pong missing')
  assert.ok(latency.steady.values.length > 0 && (stateCounts.steady ?? 0) > 0, 'no measured traffic')
  assert.ok(summarize(latency.steady).p95Ms! <= 100 && summarize(latency.steady).p99Ms! <= 200, 'steady latency gate failed')

  await enterPhase('capacity')
  captureCapacity = true
  for (let index = 0; index < 2; index++) {
    const api = new LobbyClient(baseURL.origin + '/v2/api', recordError)
    apis.push(api)
    const extra = { api, ticket: undefined as MatchTicket | undefined }
    extras.push(extra)
    await api.guest()
    extra.ticket = await api.queue({ capacity: 2, applicationProtocol })
    assert.ok(!extra.ticket.assignment, 'extra player received an assignment at participant capacity')
    await sleep(60)
  }
  for (let attempt = 0; attempt < 5; attempt++) {
    for (const extra of extras) {
      extra.ticket = await extra.api.ticket(extra.ticket!.id)
      assert.equal(extra.ticket.status, 'queued', 'extra group unexpectedly matched at capacity')
      assert.ok(!extra.ticket.assignment, 'capacity probe received an assignment')
    }
    await sleep(200)
  }
  captureCapacity = false
  assertFresh(1)
  // Drain reliable events before the deliberate socket interruption; SDK events are not replayed.
  await enterPhase('preReconnectDrain')
  await until(() => pendingEvents.size === 0 && pendingReceipts.size === 0, 'pre-reconnect event delivery', 5000)
  await enterPhase('reconnect')
  cutAt = performance.now()
  for (const member of clients) member.sdk.reconnect()
  await until(() => clients.every(member => member.sdk.status === 'active' && member.sdk.welcome?.generation === 2), 'concentrated SDK reconnect')
  await until(() => {
    for (const room of rooms) {
      for (const member of room.members) {
        for (const peer of room.members) {
          if (member.sdk.peers.get(peer.id)?.generation !== 2) return false
          if (peer === member) continue
          const state = member.sdk.states.get(peer.id)
          if (!state || state.generation !== 2) return false
          const sentAt = new DataView(state.payload.buffer, state.payload.byteOffset, state.payload.byteLength).getFloat64(0, true)
          if (sentAt < cutAt) return false
        }
      }
    }
    return true
  }, 'generation-two peer states')
  reconnectMs = performance.now() - cutAt
  assertFresh(2, cutAt)
  assert.equal(clients.filter(member => member.recoveredMs !== undefined).length, count, 'missing recovery sample')
  assert.ok(reconnectMs < 25000, 'reconnect exceeded the SDK resume grace')
  await enterPhase('afterReconnect')
  await sleep(5000)
  assertFresh(2, cutAt)
  assert.ok(latency.afterReconnect.values.length > 0, 'no post-reconnect traffic')
  assert.ok(summarize(latency.afterReconnect).p99Ms! <= 200, 'post-reconnect latency gate failed')
  await enterPhase('finalDrain')
  await until(() => pendingEvents.size === 0 && pendingReceipts.size === 0, 'final event delivery', 5000)
  assert.ok(eventsSent > 0 && eventDeliveries > 0, 'no event traffic')
  assert.equal(eventDeliveries, expectedEventDeliveries, 'missing event delivery')
  assert.equal(eventAccepted, eventsSent, 'missing event receipt')
  finalActive = clients.filter(member => member.sdk.status === 'active').length
  assert.equal(finalActive, count)
  assert.equal(errorCount, 0, errors.slice(0, 5).join('; '))
  passed = true
} catch (error) {
  recordError(error)
} finally {
  finalActive = clients.filter(member => member.sdk?.status === 'active').length
  await enterPhase('cleanup')
  captureCapacity = false
  clearInterval(publishTimer)
  clearInterval(eventTimer)
  for (const member of clients) {
    try { member.sdk?.leave() } catch (error) { recordError(error) }
  }
  for (const extra of extras) {
    if (!extra.ticket) continue
    try {
      await extra.api.cancel(extra.ticket.id)
      extraCleanup.push({ status: 'cancelled' })
    } catch (error) {
      if (error instanceof ControlError && error.status === 409) extraCleanup.push({ status: 'formed group expires after its existing two-minute TTL', httpStatus: 409 })
      else recordError(error)
    }
  }
  for (const api of apis) api.dispose()
  await sleep(1500)
  for (const member of clients) member.sdk?.close()
  await captureMetrics('after-cleanup')
  globalThis.fetch = originalFetch
}

const recovery: Samples = { seen: 0, maxMs: 0, values: [] }
for (const member of clients) if (member.recoveredMs !== undefined) observe(recovery, member.recoveredMs)
const report = {
  recordedAt: new Date().toISOString(), passed: passed && errorCount === 0,
  transport: 'public HTTPS matchmaking + real TypeScript RelayClient WSS; explicit Node CA trust; local host',
  profile: { base: baseURL.origin, participants: count, roomSize, payloadBytes, publishHz: hz, measurementSeconds: seconds },
  setupMs, steadyMs, totalMs: performance.now() - startedAt, rooms: rooms.map(room => ({ participants: room.members.length, setupMs: room.setupMs })),
  activeTotal: finalActive, activeMinimum, statusTransitions, httpStatuses,
  receivedStates: clients.reduce((total, member) => total + member.received, 0), stateCounts,
  stateLatency: { sampling: 'one in sixteen states; latest 65536 bounded samples per window', steady: summarize(latency.steady), afterReconnect: summarize(latency.afterReconnect) },
  participantTraffic: clients.map((member, index) => ({ index, receivedStates: member.received, peerSenders: member.receivedByPeer.size, pongs: member.pongs })),
  events: { sent: eventsSent, accepted: eventAccepted, received: eventDeliveries, expectedDeliveries: expectedEventDeliveries, pending: pendingEvents.size, latency: summarize(eventLatency) },
  reconnect: { windowMs: reconnectMs, recovered: recovery.seen, ...summarize(recovery) },
  capacityProbe: { additionalPlayers: extras.length, responses: capacityResponses, cleanup: extraCleanup },
  errorCount, errors, errorDetails, metricSnapshots,
}
await writeFile(output, JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
console.log(JSON.stringify({ passed: report.passed, activeTotal: finalActive, stateLatency: report.stateLatency, reconnect: report.reconnect, errors, output }))
if (!report.passed) process.exitCode = 1
