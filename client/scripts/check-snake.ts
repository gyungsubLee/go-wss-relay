// Validate the actual demo payload, snapshot sizes, and authority policy on real sockets.
import assert from 'node:assert/strict'
import { writeFile } from 'node:fs/promises'
import { create, toBinary } from '@bufbuild/protobuf'
import { ServerEnvelopeSchema } from '../src/relay/gen/relay/v2/relay_pb.ts'
import { RelayClient, type Assignment } from '../src/relay/client.ts'
import { snakeProtocol, encodeSnakeState, decodeSnakeState, acceptScene, type SnakeState, type SharedScene } from '../src/demo/snake-state.ts'
const base = process.argv[2] ?? 'http://127.0.0.1:8090'
const state: SnakeState = { x: .35, y: .5, lifeId: crypto.randomUUID(), heading: .21, speed: .4,
  length: 32, mass: 32, score: 12, isAlive: true,
  bodyPath: Array.from({ length: 32 }, (_, i) => [.35 - i * .004, .5]),
  shared: { revision: 1, seed: 42, ended: false } }
const payload = encodeSnakeState(state)
const decoded = decodeSnakeState(payload)!
assert.equal(decoded.bodyPath.length, 32)
assert.ok(Math.abs(decoded.bodyPath[0]![0] - state.bodyPath[0]![0]) <= 1 / 65535)
assert.equal(decoded.lifeId, state.lifeId)
assert.equal(decodeSnakeState(new Uint8Array([255])), undefined)
for (const invalid of [{ ...state, x: NaN }, { ...state, length: 1 }, { ...state, bodyPath: Array(65).fill([0, 0]), length: 65 }, { ...state, bodyPath: [[-1, 0]], length: 1 }]) {
  assert.throws(() => encodeSnakeState(invalid as SnakeState), /Invalid/)
}
assert.equal(acceptScene('owner', 'other', state.shared), undefined)
assert.equal(acceptScene('owner', 'owner', { revision: 1, seed: 99, ended: true }, state.shared)?.seed, 42)
const maximum = encodeSnakeState({ ...state, length: 64, bodyPath: Array.from({ length: 64 }, (_, i) => [.9 - i * .004, .75]) })
function snapshotBytes(body: Uint8Array): number {
  return toBinary(ServerEnvelopeSchema, create(ServerEnvelopeSchema, { protocolRevision: 2, sequence: 1,
    body: { case: 'syncSnapshot', value: { snapshot: { roomRevision: 16n,
      members: Array.from({ length: 16 }, (_, i) => ({ participantId: `p-${i}`, generation: 1, status: 1 })),
      states: Array.from({ length: 16 }, (_, i) => ({ participantId: `p-${i}`, generation: 1, clientSequence: 1, roomRevision: BigInt(i + 1), payload: body })),
    } } } })).length
}
async function api<T>(method: string, path: string, bearer = '', body?: unknown): Promise<T> {
  const response = await fetch(base + '/v2/api' + path, { method,
    headers: { ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}), ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}) })
  assert.ok(response.ok, `${path}: ${response.status}`)
  return response.status === 204 ? undefined as T : await response.json() as T
}
async function until(check: () => boolean, label: string): Promise<void> {
  const deadline = performance.now() + 5000
  while (!check()) {
    assert.ok(performance.now() < deadline, label)
    await new Promise(resolve => setTimeout(resolve, 20))
  }
}
const alice = await api<{ token: string; participantId: string }>('POST', '/sessions', '', {})
const bob = await api<{ token: string; participantId: string }>('POST', '/sessions', '', {})
const room = await api<{ id: string }>('POST', '/lobbies', alice.token, { capacity: 2, applicationProtocol: snakeProtocol, visibility: 'private' })
const path = `/lobbies/${room.id}`
await api('POST', path + '/join', bob.token, { revision: 1 })
await api('POST', path + '/ready', alice.token, { revision: 2, ready: true })
await api('POST', path + '/ready', bob.token, { revision: 3, ready: true })
await api('POST', path + '/start', alice.token, { revision: 4 })
const a = await api<Assignment>('GET', path + '/assignment', alice.token)
const b = await api<Assignment>('GET', path + '/assignment', bob.token)
let scene: SharedScene | undefined
let authorityLeft = false
let bodyReceived = false
let unauthorizedSeen = false
let ownerScene = state.shared
const owner = new RelayClient({ applicationProtocol: snakeProtocol, onState(update) {
  const candidate = decodeSnakeState(update.payload)?.shared
  if (update.participantId === bob.participantId && candidate?.seed === 666) unauthorizedSeen = true
  ownerScene = acceptScene(alice.participantId, update.participantId, candidate, ownerScene)
} })
const peer = new RelayClient({ applicationProtocol: snakeProtocol,
  onState(update) {
    const snake = decodeSnakeState(update.payload)
    scene = acceptScene(alice.participantId, update.participantId, snake?.shared, scene)
    if (update.participantId === alice.participantId && snake?.bodyPath.length === 32) bodyReceived = true
  },
  onMessage(message) { if (message.body.case === 'peerLeft' && message.body.value.participantId === alice.participantId) authorityLeft = true },
})
try {
  owner.connect(a); peer.connect(b)
  await until(() => owner.status === 'active' && peer.status === 'active', 'snake sockets')
  owner.publishState(payload)
  await until(() => bodyReceived && scene?.seed === 42, 'full body and shared scene')
  peer.publishState(encodeSnakeState({ ...state, shared: { revision: 99, seed: 666, ended: true } }))
  await until(() => unauthorizedSeen, 'unauthorized candidate reached receiver')
  assert.equal(ownerScene?.seed, 42, 'non-authority scene was accepted')
  assert.equal(ownerScene?.ended, false)
  owner.reconnect()
  await until(() => owner.status === 'active' && owner.welcome?.generation === 2, 'authority resume')
  assert.equal(authorityLeft, false, 'temporary disconnect ended the game')
  owner.publishState(encodeSnakeState({ ...state, shared: { revision: 2, seed: 42, ended: true } }))
  await until(() => scene?.ended === true, 'authority end state')
  owner.leave()
  await until(() => authorityLeft, 'permanent authority departure')
  const report = { protocol: snakeProtocol, bodyPoints: 32, fullStateBytes: payload.length,
    maxBodyPoints: 64, maxFullStateBytes: maximum.length, snapshot16Bytes: snapshotBytes(payload),
    maxSnapshot16Bytes: snapshotBytes(maximum), fullBodyReceived: bodyReceived,
    unauthorizedSceneIgnored: true, temporaryDisconnectKeepsAuthority: true,
    authorityEndStateReceived: true, permanentAuthorityDepartureObserved: authorityLeft }
  assert.ok(report.maxSnapshot16Bytes < 128 * 1024)
  await writeFile(new URL('../../docs/evidence/v2/2026-10-22-snake-state.json', import.meta.url), JSON.stringify(report, null, 2) + '\n')
  console.log(JSON.stringify(report))
} finally { owner.close(); peer.close() }
