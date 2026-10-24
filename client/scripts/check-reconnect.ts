// Concentrated reconnect uses real TS SDK clients and a trusted management-only provisioner.
import { readFile, writeFile } from 'node:fs/promises'
import assert from 'node:assert/strict'
import { RelayClient, type Assignment } from '../src/relay/client.ts'

const count = Number(process.argv[2] ?? 100)
assert.ok(Number.isInteger(count) && count >= 2 && count <= 1000)
const token = (await readFile(new URL('../../.cache/wss-demo.token', import.meta.url), 'utf8')).trim()
const clients: RelayClient[] = []
const rooms: string[] = []
const recovered: number[] = []
const errors: string[] = []
let cutAt = 0
async function control<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch('http://127.0.0.1:8092' + path, { method,
    headers: { Authorization: `Bearer ${token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}),
  })
  assert.ok(response.ok, `management HTTP ${response.status}`)
  return response.status === 204 ? undefined as T : await response.json() as T
}
async function waitFor(condition: () => boolean, label: string): Promise<void> {
  const deadline = performance.now() + 25000
  while (!condition()) {
    assert.ok(performance.now() < deadline, `${label}: ${errors.slice(0, 5).join('; ')}`)
    await new Promise(resolve => setTimeout(resolve, 25))
  }
}
try {
  const runId = crypto.randomUUID().slice(0, 8)
  for (let offset = 0; offset < count; offset += 8) {
    const participants = Array.from({ length: Math.min(8, count - offset) }, (_, index) => `p-${offset + index}`)
    const allocation = await control<{ roomId: string; roomInstanceId: string; nodeId: string; websocketUrl: string; expiresAt: string; tickets: Record<string, string> }>('POST', '/v2/rooms', {
      roomId: `chaos-${runId}-${offset}`, applicationProtocol: 'chaos.v1', participants, ttlSeconds: 1800,
    })
    rooms.push(allocation.roomInstanceId)
    for (const participantId of participants) {
      const assignment: Assignment = { ...allocation, participantId, applicationProtocol: 'chaos.v1', matchId: allocation.roomId,
        subprotocol: 'relay.v2', ticketExpiresAt: new Date(Date.now() + 30000).toISOString(), connectTicket: allocation.tickets[participantId] }
      const sdk = new RelayClient({
        onStatus(status) {
          if (status === 'active') {
            sdk.publishState(new TextEncoder().encode(`full-state:${participantId}:${sdk.welcome!.generation}`))
            if (cutAt) recovered.push(performance.now() - cutAt)
          }
        },
        onError(error) { errors.push(error.message) },
      })
      clients.push(sdk)
      sdk.connect(assignment)
    }
  }
  await waitFor(() => clients.every(client => client.status === 'active'), 'initial connection')
  cutAt = performance.now()
  for (const client of clients) client.reconnect()
  await waitFor(() => clients.every(client => client.status === 'active' && client.welcome!.generation === 2), 'concentrated reconnect')
  await waitFor(() => clients.every(client => [...client.peers.values()].every(peer => peer.generation === 2) && [...client.states.values()].every(state => state.generation === 2)), 'stale generation removed')
  assert.equal(errors.length, 0, errors.slice(0, 5).join('; '))
  recovered.sort((a, b) => a - b)
  const report = { participants: count, recovered: recovered.length,
    recoveryP95Ms: recovered[Math.ceil(recovered.length * .95) - 1], recoveryP99Ms: recovered[Math.ceil(recovered.length * .99) - 1],
    maxRecoveryMs: recovered.at(-1), errors, transport: 'same-host WSS; Node extra CA trust; actual TypeScript SDK' }
  await writeFile(new URL(`../../docs/evidence/v2/load/reconnect-${count}.json`, import.meta.url), JSON.stringify(report, null, 2) + '\n')
  console.log(JSON.stringify(report))
} finally {
  for (const client of clients) client.close()
  for (const instance of rooms) await control('DELETE', `/v2/rooms/${instance}`).catch(() => {})
}
