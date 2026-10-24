// Node fixtures exercise public SDK methods without exposing test controls in production.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { create, toBinary } from '@bufbuild/protobuf'
import { RelayClient, type Assignment } from '../src/relay/client.ts'
import { LobbyClient, ControlError } from '../src/relay/api.ts'
import { ServerEnvelopeSchema } from '../src/relay/gen/relay/v2/relay_pb.ts'

const assignment: Assignment = {
  matchId: 'match', subprotocol: 'relay.v2', ticketExpiresAt: '2099-01-01T00:00:00Z',
  roomId: 'room', roomInstanceId: 'instance', participantId: 'peer', nodeId: 'node',
  websocketUrl: 'ws://127.0.0.1:8090/v2/relay', applicationProtocol: 'check.v1', connectTicket: 'ticket',
}

// Only the socket boundary is controlled; retry scheduling and refresh run in the real client.
class SocketFixture {
  static OPEN = 1
  static instances: SocketFixture[] = []
  readyState = 1
  bufferedAmount = 0
  protocol = 'relay.v2'
  binaryType = ''
  onopen?: () => void
  onmessage?: (event: { data: ArrayBuffer }) => void
  onclose?: () => void
  onerror?: () => void
  constructor(_url: string, _protocol: string) { SocketFixture.instances.push(this) }
  send(_data: Uint8Array): void {}
  close(): void {
    this.readyState = 3
    queueMicrotask(() => this.onclose?.())
  }
  expireTicket(): void {
    const data = toBinary(ServerEnvelopeSchema, create(ServerEnvelopeSchema, {
      protocolRevision: 2, sequence: 1,
      body: { case: 'error', value: { code: 4, fatal: true, message: 'expired ticket' } },
    }))
    this.onmessage?.({ data: data.slice().buffer as ArrayBuffer })
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
async function waitFor(check: () => boolean, label: string): Promise<void> {
  const deadline = performance.now() + 1500
  while (!check()) {
    assert.ok(performance.now() < deadline, `timed out: ${label}`)
    await new Promise(resolve => setTimeout(resolve, 10))
  }
}
const settle = () => new Promise(resolve => setImmediate(resolve))
const latestSocket = () => SocketFixture.instances.at(-1)!

async function checkRefresh(): Promise<void> {
  const originalSocket = globalThis.WebSocket
  globalThis.WebSocket = SocketFixture as unknown as typeof WebSocket
  try {
    for (const action of ['connect', 'close', 'leave', 'suspend'] as const) {
      for (const outcome of ['success', 'failure', 'terminal', 'invalid'] as const) {
        const pending = deferred<Assignment>()
        const errors: Error[] = []
        let calls = 0
        const client = new RelayClient({
          applicationProtocol: 'check.v1', onError: error => errors.push(error),
          refreshAssignment: () => { ++calls; return pending.promise },
        })
        try {
          client.connect(assignment)
          latestSocket().expireTicket()
          await waitFor(() => calls === 1, 'first refresh')
          errors.length = 0
          if (action === 'connect') client.connect({ ...assignment, connectTicket: 'new-ticket' })
          else client[action]()
          const socket = latestSocket()
          const socketCount = SocketFixture.instances.length
          const status = client.status
          if (outcome === 'success') pending.resolve({ ...assignment, connectTicket: 'old-refresh' })
          else if (outcome === 'invalid') pending.resolve({ ...assignment, websocketUrl: 'not a URL' })
          else pending.reject(new ControlError(outcome === 'terminal' ? 403 : 503, 'old failure'))
          await settle()
          // A stale rejection must not report an error or schedule another retry.
          await new Promise(resolve => setTimeout(resolve, 650))
          assert.equal(client.status, status, `${action}/${outcome}: stale refresh changed status`)
          assert.equal(SocketFixture.instances.length, socketCount, `${action}/${outcome}: stale refresh opened a socket`)
          assert.equal(errors.length, 0, `${action}/${outcome}: stale refresh reported an error`)
          if (action === 'connect') assert.equal(socket.readyState, 1, 'stale refresh closed new socket')
          assert.equal(calls, 1, `${action}/${outcome}: stale refresh scheduled retry`)
        } finally { client.close() }
      }
    }

    // Old finally must not release the guard protecting a newer pending refresh.
    for (const [transition, outcome] of [
      ['connect', 'success'], ['connect', 'failure'],
      ['suspend/resume', 'success'], ['suspend/resume', 'failure'],
    ] as const) {
      const first = deferred<Assignment>()
      const second = deferred<Assignment>()
      let calls = 0
      const errors: Error[] = []
      const client = new RelayClient({
        onError: error => errors.push(error),
        refreshAssignment: () => ++calls === 1 ? first.promise : second.promise,
      })
      try {
        client.connect(assignment)
        latestSocket().expireTicket()
        await waitFor(() => calls === 1, 'old refresh')
        assert.throws(() => client.connect({ ...assignment, websocketUrl: 'bad' }))
        assert.throws(() => client.connect({ ...assignment, applicationProtocol: '' }))
        if (transition === 'connect') client.connect(assignment)
        else {
          client.suspend()
          client.resume()
        }
        latestSocket().expireTicket()
        await waitFor(() => calls === 2, `${transition}: new refresh must start`)
        if (outcome === 'success') first.resolve(assignment)
        else first.reject(new ControlError(403, 'stale terminal failure'))
        await settle()
        latestSocket().onclose?.()
        await new Promise(resolve => setTimeout(resolve, 1400))
        assert.equal(calls, 2, `${transition}: stale finally allowed a duplicate refresh`)
        const before = SocketFixture.instances.length
        second.resolve(assignment)
        await waitFor(() => SocketFixture.instances.length > before, 'new refresh opened socket')
        assert.equal(errors.filter(error => !error.message.includes('expired ticket')).length, 0)
      } finally {
        first.resolve(assignment)
        second.resolve(assignment)
        client.close()
      }
    }
    // Rejected connect input must preserve a refresh which still owns the live session.
    const pending = deferred<Assignment>()
    let refreshing = false
    const client = new RelayClient({ refreshAssignment: () => { refreshing = true; return pending.promise } })
    try {
      client.connect(assignment)
      latestSocket().expireTicket()
      await waitFor(() => refreshing, 'refresh before invalid connect')
      assert.throws(() => client.connect({ ...assignment, websocketUrl: 'invalid' }))
      const before = SocketFixture.instances.length
      pending.resolve(assignment)
      await waitFor(() => SocketFixture.instances.length > before, 'valid refresh survives invalid connect')
    } finally { pending.resolve(assignment); client.close() }
    console.log('PASS: stale refresh success/failure/invalid/finally, connect/close/leave/suspend/resume, input validation')
  } finally { globalThis.WebSocket = originalSocket }
}

async function checkBodyTimeout(status: number): Promise<void> {
  const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { href: 'http://127.0.0.1/' } })
  const server = createServer((_request, response) => {
    response.writeHead(status, { 'Content-Type': 'application/json' })
    response.write('{"unfinished":')
  })
  let failsafe: ReturnType<typeof setTimeout> | undefined
  try {
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject)
      server.listen(0, '127.0.0.1', resolve)
    })
    const address = server.address()
    assert.ok(address && typeof address !== 'string')
    const api = new LobbyClient(`http://127.0.0.1:${address.port}`)
    const start = performance.now()
    const result = await Promise.race([
      api.get('stalled').then(() => 'resolved', error => error),
      new Promise<string>(resolve => { failsafe = setTimeout(() => resolve('failsafe'), 6500) }),
    ])
    assert.notEqual(result, 'failsafe', `${status}: HTTP timeout stopped after headers; stalled body exceeded 5 seconds`)
    assert.ok(result instanceof Error, `${status}: stalled body must reject`)
    assert.ok(performance.now() - start >= 4500, `${status}: deadline fired too early`)
    api.dispose()
    console.log(`PASS: HTTP ${status} stalled body aborted within the existing 5-second deadline`)
  } finally {
    clearTimeout(failsafe)
    server.closeAllConnections()
    await new Promise<void>(resolve => server.close(() => resolve()))
    if (originalLocation) Object.defineProperty(globalThis, 'location', originalLocation)
    else Reflect.deleteProperty(globalThis, 'location')
  }
}

const failures: unknown[] = []
for (const check of [checkRefresh, () => checkBodyTimeout(200), () => checkBodyTimeout(503)]) {
  try { await check() } catch (error) { failures.push(error); console.error(error) }
}
assert.equal(failures.length, 0, `${failures.length} lifecycle checks failed`)
