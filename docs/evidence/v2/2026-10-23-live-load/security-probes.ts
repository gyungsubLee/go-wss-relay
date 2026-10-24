// Local-only bounded probes. NODE_EXTRA_CA_CERTS must trust the test CA.
import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import https from 'node:https'
import { randomBytes } from 'node:crypto'
import { create, toBinary, fromBinary } from '../../../../client/node_modules/@bufbuild/protobuf/dist/esm/index.js'
import { ClientEnvelopeSchema, ServerEnvelopeSchema, ErrorCode } from '../../../../client/src/relay/gen/relay/v2/relay_pb.ts'

const base = new URL(process.argv[2] ?? 'https://localhost:19443')
assert.ok(['localhost', '127.0.0.1'].includes(base.hostname), 'local probes only')
const out = process.argv[3] ?? new URL('./security.json', import.meta.url)
const token = (await readFile(new URL('../../../../.cache/live-load-20261023/operator.token', import.meta.url), 'utf8')).trim()
const results: Record<string, unknown> = {}
const rooms: string[] = []
const sockets: WebSocket[] = []
const rawSockets: import('node:net').Socket[] = []
const delay = (ms: number) => new Promise(resolve => setTimeout(resolve, ms))
async function control(method: string, path: string, body?: unknown) {
  const response = await fetch('http://127.0.0.1:19092' + path, {
    method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(5000),
  })
  assert.ok(response.ok, `management ${response.status}`)
  return response.status === 204 ? undefined : response.json()
}
async function allocate(name: string, participants: string[]) {
  const room = await control('POST', '/v2/rooms', { roomId: `security-${name}-${Date.now()}`, participants, applicationProtocol: 'security.v1', ttlSeconds: 120 })
  rooms.push(room.roomInstanceId)
  return room
}
function upgrade(origin?: string): Promise<{ status: number; socket?: import('node:net').Socket }> {
  return new Promise((resolve, reject) => {
    const request = https.request(new URL('/v2/relay', base), { headers: {
      Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13',
      'Sec-WebSocket-Key': randomBytes(16).toString('base64'), 'Sec-WebSocket-Protocol': 'relay.v2',
      ...(origin ? { Origin: origin } : {}),
    }, timeout: 2500 })
    request.on('upgrade', (response, socket) => { rawSockets.push(socket); socket.on('error', () => {}); resolve({ status: response.statusCode!, socket }) })
    request.on('response', response => { response.resume(); resolve({ status: response.statusCode! }) })
    request.on('timeout', () => request.destroy(new Error('upgrade timeout')))
    request.on('error', reject)
    request.end()
  })
}
async function connection(ticket: string, applicationProtocol = 'security.v1') {
  const ws = new WebSocket(new URL('/v2/relay', base).href.replace('https:', 'wss:'), 'relay.v2')
  sockets.push(ws)
  ws.binaryType = 'arraybuffer'
  const message = await new Promise<any>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('connect timeout')), 3000)
    ws.onopen = () => ws.send(toBinary(ClientEnvelopeSchema, create(ClientEnvelopeSchema, {
      protocolRevision: 2, body: { case: 'connect', value: { applicationProtocol, credential: { case: 'connectTicket', value: ticket } } },
    })))
    ws.onmessage = event => { clearTimeout(timeout); resolve(fromBinary(ServerEnvelopeSchema, new Uint8Array(event.data as ArrayBuffer))) }
    ws.onerror = () => { clearTimeout(timeout); reject(new Error('websocket handshake failed')) }
  })
  return { ws, message }
}
async function closeCode(ws: WebSocket, send: () => void) {
  return new Promise<number>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('close timeout')), 3000)
    ws.onclose = event => { clearTimeout(timeout); resolve(event.code) }
    send()
  })
}
async function httpProbe(name: string, path: string, init: RequestInit, expected: number[]) {
  const response = await fetch(new URL(path, base), { ...init, signal: AbortSignal.timeout(3000) })
  await response.arrayBuffer()
  results[name] = { status: response.status, passed: expected.includes(response.status) }
  assert.ok(expected.includes(response.status), `${name}: ${response.status}`)
}
try {
  await httpProbe('missingBearer', '/v2/api/lobbies', {}, [401])
  await httpProbe('forgedBearer', '/v2/api/lobbies', { headers: { Authorization: 'Bearer fake' } }, [401])
  await httpProbe('badOriginHTTP', '/v2/api/sessions', { method: 'POST', headers: { Origin: 'https://untrusted.test', 'Content-Type': 'application/json' }, body: '{}' }, [403])
  await httpProbe('credentialQuery', '/v2/api/sessions?token=redacted', { method: 'POST', body: '{}' }, [400])
  await httpProbe('duplicateJSON', '/v2/api/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"x":1,"x":2}' }, [400])
  await httpProbe('oversizedJSON', '/v2/api/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"x":"' + 'a'.repeat(65536) + '"}' }, [413])
  await httpProbe('managementOnPublic', '/v2/status', {}, [404])
  const managementUnauthorized = await fetch('http://127.0.0.1:19092/v2/status', { signal: AbortSignal.timeout(3000) })
  results.managementUnauthorized = { status: managementUnauthorized.status, passed: managementUnauthorized.status === 401 }
  await managementUnauthorized.arrayBuffer()
  assert.equal(managementUnauthorized.status, 401)
  const badOrigin = await upgrade('https://untrusted.test')
  results.badOriginWSS = { status: badOrigin.status, passed: badOrigin.status === 403 }
  assert.equal(badOrigin.status, 403)

  const room = await allocate('credentials', ['alice', 'bob', 'text', 'oversize', 'flood'])
  const alice = await connection(room.tickets.alice)
  assert.equal(alice.message.body.case, 'welcome')
  const replay = await connection(room.tickets.alice)
  assert.equal(replay.message.body.case, 'error')
  assert.equal(replay.message.body.value.code, ErrorCode.UNAUTHENTICATED)
  results.singleUseTicket = { errorCode: replay.message.body.value.code, passed: true }
  const mismatch = await connection(room.tickets.bob, 'wrong.v1')
  assert.equal(mismatch.message.body.case, 'error')
  assert.equal(mismatch.message.body.value.code, ErrorCode.APPLICATION_PROTOCOL_MISMATCH)
  const bob = await connection(room.tickets.bob)
  assert.equal(bob.message.body.case, 'welcome')
  results.applicationIsolation = { errorCode: mismatch.message.body.value.code, correctProtocolStillWorks: true }
  const text = await connection(room.tickets.text)
  const textCode = await closeCode(text.ws, () => text.ws.send('text'))
  results.textFrame = { closeCode: textCode, passed: textCode === 1003 }
  assert.equal(textCode, 1003)
  const oversized = await connection(room.tickets.oversize)
  const oversizeCode = await closeCode(oversized.ws, () => oversized.ws.send(new Uint8Array(16385)))
  results.oversizedFrame = { closeCode: oversizeCode, passed: oversizeCode === 1009 }
  assert.equal(oversizeCode, 1009)

  const flood = await connection(room.tickets.flood)
  let rejected = 0
  let sent = 0
  let seq = 0
  flood.ws.onmessage = event => {
    const message = fromBinary(ServerEnvelopeSchema, new Uint8Array(event.data as ArrayBuffer))
    if (message.body.case === 'error' && message.body.value.code === ErrorCode.RATE_LIMITED) ++rejected
  }
  const before = await control('GET', '/v2/status')
  const started = performance.now()
  const timer = setInterval(() => {
    if (flood.ws.readyState !== WebSocket.OPEN) return
    flood.ws.send(toBinary(ClientEnvelopeSchema, create(ClientEnvelopeSchema, {
      protocolRevision: 2, sequence: ++seq, body: { case: 'stateUpdate', value: { payload: new Uint8Array(1024) } },
    })))
    ++sent
  }, 10)
  await delay(1000)
  clearInterval(timer)
  await delay(100)
  const after = await control('GET', '/v2/status')
  results.authenticatedOverRate = { durationMs: performance.now() - started, sent, errorsReceived: rejected, stillOpen: flood.ws.readyState === WebSocket.OPEN,
    rateRejectedDelta: after.rateRejected - before.rateRejected, ingressBytesDelta: after.ingressBytes - before.ingressBytes }
  assert.ok(rejected > 0)
  for (const ws of sockets) ws.close()
  await delay(300)

  const startedHold = performance.now()
  for (let offset = 0; offset < 128; offset += 16) {
    const admissions = await Promise.all(Array.from({ length: 16 }, () => upgrade()))
    assert.ok(admissions.every(item => item.status === 101), 'silent admissions unexpectedly rejected')
  }
  assert.ok(performance.now() - startedHold < 2000, 'silent socket setup too slow to isolate admission')
  const blocked = await upgrade()
  results.silentAuthStarvation = { silentConnections: 128, holdMs: performance.now() - startedHold, legitimateUpgradeStatus: blocked.status,
    reproduced: blocked.status === 429 }
  assert.equal(blocked.status, 429)
  for (const socket of rawSockets) socket.destroy()
  await delay(250)
  const recovery = await upgrade()
  recovery.socket?.destroy()
  results.silentAuthRecovery = { status: recovery.status, passed: recovery.status === 101 }
  assert.equal(recovery.status, 101)

  const statuses: number[] = []
  for (let offset = 0; offset < 64; offset += 16) {
    const responses = await Promise.all(Array.from({ length: 16 }, (_, index) => fetch(new URL('/v2/api/sessions', base), {
      method: 'POST', signal: AbortSignal.timeout(3000), headers: { 'Content-Type': 'application/json', 'X-Forwarded-For': `192.0.2.${offset + index + 1}` }, body: '{}',
    })))
    for (const response of responses) { statuses.push(response.status); await response.arrayBuffer() }
  }
  results.guestRateAndUntrustedXFF = { attempted: statuses.length, accepted: statuses.filter(status => status === 201).length,
    rejected429: statuses.filter(status => status === 429).length, passed: statuses.includes(429) }
  assert.ok(statuses.includes(429))
} finally {
  for (const ws of sockets) ws.close()
  for (const socket of rawSockets) socket.destroy()
  for (const room of rooms) await control('DELETE', `/v2/rooms/${room}`).catch(() => {})
  await delay(500)
  const cleanup = await control('GET', '/v2/status')
  assert.equal(cleanup.rooms, 0)
  assert.equal(cleanup.connections, 0)
  assert.equal(cleanup.reservedParticipants, 0)
  results.cleanup = { rooms: cleanup.rooms, connections: cleanup.connections, reservedParticipants: cleanup.reservedParticipants }
  await writeFile(out, JSON.stringify(results, null, 2) + '\n')
}
console.log(JSON.stringify(results))
