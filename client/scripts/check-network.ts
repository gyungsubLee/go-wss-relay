// End-to-end TLS passes through an opaque, delayed TCP proxy. No TLS verification bypass.
import assert from 'node:assert/strict'
import { createServer, connect, type Socket } from 'node:net'
import { Transform } from 'node:stream'
import { readFile, writeFile } from 'node:fs/promises'
import { RelayClient, type Assignment } from '../src/relay/client.ts'
const delayMs = 100
const sockets = new Set<Socket>()
const errors: string[] = []
function delayed(): Transform {
  return new Transform({ transform(chunk, _encoding, callback) {
    setTimeout(() => callback(null, chunk), delayMs)
  } })
}
const proxy = createServer(socket => {
  const upstream = connect({ host: '127.0.0.1', port: 9443 })
  sockets.add(socket); sockets.add(upstream)
  const outbound = delayed(), inbound = delayed()
  const close = () => {
    socket.destroy(); upstream.destroy(); outbound.destroy(); inbound.destroy()
    sockets.delete(socket); sockets.delete(upstream)
  }
  socket.on('error', close); upstream.on('error', close)
  outbound.on('error', close); inbound.on('error', close)
  socket.on('close', close); upstream.on('close', close)
  socket.pipe(outbound).pipe(upstream)
  upstream.pipe(inbound).pipe(socket)
})
await new Promise<void>(resolve => proxy.listen(0, '127.0.0.1', resolve))
const address = proxy.address()
assert.ok(address && typeof address !== 'string')
const token = (await readFile(new URL('../../.cache/wss-demo.token', import.meta.url), 'utf8')).trim()
async function management<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch('http://127.0.0.1:8092' + path, { method,
    headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}) })
  assert.ok(response.ok, `management: ${response.status}`)
  return response.status === 204 ? undefined as T : await response.json() as T
}
async function until(check: () => boolean, label: string): Promise<void> {
  const deadline = performance.now() + 20000
  while (!check()) {
    assert.ok(performance.now() < deadline, `${label}: ${errors.join('; ')}`)
    await new Promise(resolve => setTimeout(resolve, 25))
  }
}
let roomInstance = ''
let sentAt = 0
const eventLatency: number[] = [], receiptLatency: number[] = []
const alice = new RelayClient({ onError(error) { errors.push(error.message) }, onMessage(message) {
  if (message.body.case === 'eventAccepted') receiptLatency.push(performance.now() - sentAt)
} })
const bob = new RelayClient({ onError(error) { errors.push(error.message) }, onEvent() { eventLatency.push(performance.now() - sentAt) } })
try {
  const allocation = await management<{ roomId: string; roomInstanceId: string; nodeId: string; expiresAt: string; ticketExpiresAt: string; tickets: Record<string, string> }>('POST', '/v2/rooms', {
    roomId: `delay-${crypto.randomUUID().slice(0, 8)}`, applicationProtocol: 'network.v1', participants: ['alice', 'bob'], ttlSeconds: 1800 })
  roomInstance = allocation.roomInstanceId
  const assignment = (participantId: string): Assignment => ({ ...allocation, participantId, matchId: allocation.roomId,
    applicationProtocol: 'network.v1', subprotocol: 'relay.v2', websocketUrl: `wss://localhost:${address.port}/v2/relay`, connectTicket: allocation.tickets[participantId]! })
  alice.connect(assignment('alice')); bob.connect(assignment('bob'))
  await until(() => alice.status === 'active' && bob.status === 'active', 'delayed TLS connection')
  for (let index = 0; index < 5; index++) {
    sentAt = performance.now()
    alice.publishEvent(1, new Uint8Array([index]))
    await until(() => eventLatency.length === index + 1 && receiptLatency.length === index + 1, 'delayed event and receipt')
  }
  assert.ok(eventLatency.every(value => value >= delayMs * 2 - 20), 'proxy delay was not exercised')
  const interruptedAt = performance.now()
  for (const socket of sockets) socket.destroy()
  await until(() => alice.status === 'active' && bob.status === 'active' && alice.welcome?.generation === 2 && bob.welcome?.generation === 2, 'TCP interruption recovery')
  const recoveryMs = performance.now() - interruptedAt
  assert.equal(errors.length, 0)
  const report = { transport: 'end-to-end verified WSS through opaque loopback TCP proxy',
    delayPerChunkEachDirectionMs: delayMs, eventSamples: eventLatency.length,
    eventLatencyMs: eventLatency, receiptLatencyMs: receiptLatency, interruptionRecoveryMs: recoveryMs,
    resumedGeneration: 2, errors,
    limitation: 'Controlled TCP stream delay and abrupt connection loss; not radio/IP packet-loss percentage or Android hardware evidence' }
  await writeFile(new URL('../../docs/evidence/v2/2026-10-22-network-faults.json', import.meta.url), JSON.stringify(report, null, 2) + '\n')
  console.log(JSON.stringify(report))
} finally {
  alice.close(); bob.close()
  for (const socket of sockets) socket.destroy()
  await new Promise<void>(resolve => proxy.close(() => resolve()))
  if (roomInstance) await management('DELETE', `/v2/rooms/${roomInstance}`).catch(() => {})
}
