// Run against a live standalone server: npm run check -- http://127.0.0.1:8090
import assert from 'node:assert/strict'
import { RelayClient, type Assignment } from '../src/relay/client.ts'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { ServerEnvelopeSchema, ClientEnvelopeSchema } from '../src/relay/gen/relay/v2/relay_pb.ts'

const base = process.argv[2] ?? 'http://127.0.0.1:8090'
const encode = new TextEncoder()
const decode = new TextDecoder()
const errors: string[] = []
const clients: RelayClient[] = []
async function request<T>(method: string, path: string, token = '', data?: unknown): Promise<T> {
  const result = await fetch(base + '/v2/api' + path, {
    method, headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(data === undefined ? {} : { 'Content-Type': 'application/json' }) },
    ...(data === undefined ? {} : { body: JSON.stringify(data) }),
  })
  assert.ok(result.ok, `${method} ${path}: ${result.status}`)
  return result.status === 204 ? undefined as T : await result.json() as T
}
async function waitFor(check: () => boolean, label: string): Promise<void> {
  const deadline = performance.now() + 4000
  while (!check()) {
    assert.ok(performance.now() < deadline, `timed out: ${label}; ${errors.join('; ')}`)
    await new Promise(resolve => setTimeout(resolve, 20))
  }
}
try {
  // JS numbers cannot exactly represent this revision; the generated contract must retain bigint.
  const max = 18446744073709551615n
  const fixture = create(ServerEnvelopeSchema, { protocolRevision: 2, sequence: 1, body: { case: 'stateBatch', value: { roomRevision: max, states: [{ participantId: 'peer', generation: 1, clientSequence: 9, roomRevision: max, receivedUnixMs: 9007199254740993n, payload: encode.encode('opaque') }] } } })
  const roundTrip = fromBinary(ServerEnvelopeSchema, toBinary(ServerEnvelopeSchema, fixture))
  assert.equal(roundTrip.body.case === 'stateBatch' && roundTrip.body.value.roomRevision, max)

  const a = await request<{ token: string; participantId: string }>('POST', '/sessions', '', {})
  const b = await request<{ token: string; participantId: string }>('POST', '/sessions', '', {})
  const room = await request<{ id: string }>('POST', '/lobbies', a.token, { capacity: 2, applicationProtocol: 'check.v1', visibility: 'private' })
  const path = `/lobbies/${room.id}`
  await request('POST', path + '/join', b.token, { revision: 1 })
  await request('POST', path + '/ready', a.token, { revision: 2, ready: true })
  await request('POST', path + '/ready', b.token, { revision: 3, ready: true })
  await request('POST', path + '/start', a.token, { revision: 4 })
  const assignmentA = await request<Assignment>('GET', path + '/assignment', a.token)
  const assignmentB = await request<Assignment>('GET', path + '/assignment', b.token)
  assert.equal(assignmentA.roomInstanceId, assignmentB.roomInstanceId)
  assert.notEqual(assignmentA.connectTicket, assignmentB.connectTicket)
  const received: string[] = []
  let accepted = false
  const alice = new RelayClient({ applicationProtocol: 'check.v1', onError: error => errors.push(error.message), onMessage: message => { if (message.body.case === 'eventAccepted') accepted = true }, refreshAssignment: () => request('POST', path + '/assignment', a.token, {}) })
  const bob = new RelayClient({ onError: error => errors.push(error.message), onEvent: event => received.push(decode.decode(event.payload)) })
  clients.push(alice, bob)
  alice.connect(assignmentA)
  bob.connect(assignmentB)
  await waitFor(() => alice.status === 'active' && bob.status === 'active', 'two SDK connections')
  assert.throws(() => alice.connect({ ...assignmentA, applicationProtocol: 'wrong.v1' }), /Unsupported game/)
  assert.equal(alice.status, 'active', 'invalid assignment closed the live session')
  for (let i = 0; i < 100; i++) alice.publishState(encode.encode(`full-state-${i}`))
  await waitFor(() => decode.decode(bob.states.get(a.participantId)?.payload) === 'full-state-99', 'latest state delivered')
  assert.equal(bob.states.get(a.participantId)?.clientSequence, 1, 'superseded states leaked to wire')
  alice.publishEvent(1, encode.encode('event-once'))
  await waitFor(() => accepted && received.length === 1, 'event and server acceptance')
  const generation = alice.welcome!.generation
  alice.reconnect()
  await waitFor(() => alice.status === 'active' && alice.welcome!.generation > generation, 'generation rotated on reconnect')
  await waitFor(() => bob.states.get(a.participantId)?.generation === alice.welcome!.generation, 'old generation cache replaced')
  assert.equal(received.length, 1, 'event replayed after reconnect')

  // Consume a ticket and lose Welcome before constructing the SDK; HTTP reissue must recover it.
  const refreshed = await request<Assignment>('POST', path + '/assignment', a.token, {})
  alice.close()
  const raw = new WebSocket(refreshed.websocketUrl, 'relay.v2')
  raw.binaryType = 'arraybuffer'
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('raw auth timeout')), 2000)
    raw.onopen = () => raw.send(toBinary(ClientEnvelopeSchema, create(ClientEnvelopeSchema, { protocolRevision: 2, body: { case: 'connect', value: { applicationProtocol: refreshed.applicationProtocol, credential: { case: 'connectTicket', value: refreshed.connectTicket } } } })))
    raw.onmessage = () => { clearTimeout(timeout); raw.close(); resolve() }
    raw.onerror = () => { clearTimeout(timeout); reject(new Error('raw socket failed')) }
  })
  const replacement = new RelayClient({ onError: error => errors.push(error.message), refreshAssignment: () => request('POST', path + '/assignment', a.token, {}) })
  clients.push(replacement)
  replacement.connect(refreshed)
  await waitFor(() => replacement.status === 'active', 'consumed ticket reauthorized through HTTP')
  assert.ok(replacement.welcome!.generation >= generation + 3)
  assert.ok(replacement.welcome!.resumed)
  bob.suspend()
  assert.throws(() => bob.publishEvent(1, encode.encode('offline')), /not active/)
  bob.resume()
  await waitFor(() => bob.status === 'active' && bob.welcome!.generation === 2, 'suspend/resume')
  assert.equal(received.length, 1, 'offline event replayed')
  console.log('PASS: bigint, HTTP lobby authorization, two real SDK sockets, coalescing, event receipt, reconnect, ticket reissue, suspend/resume')
} finally {
  for (const client of clients) client.close()
}
