// Start real coordinator/relay child processes and verify control + WSS ownership.
import { spawn, type ChildProcess } from 'node:child_process'
import { readFile, writeFile, open } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import assert from 'node:assert/strict'
import { RelayClient, type Assignment } from '../src/relay/client.ts'

const root = fileURLToPath(new URL('../../', import.meta.url))
const tokenFile = root + '.cache/wss-demo.token'
const token = (await readFile(tokenFile, 'utf8')).trim()
const registry = root + '.cache/cluster-smoke.json'
const clients: RelayClient[] = []
const processes: ChildProcess[] = []
const sessionErrors: string[] = []
const nodeConfig = (id: string, index: number) => ({ id, controlUrl: `http://127.0.0.1:${9612 + index}`, websocketUrl: `wss://localhost:${9641 + index}/v2/relay`, tokenFile })
let configs = [nodeConfig('node-a', 0), nodeConfig('node-b', 1)]
await writeFile(registry, JSON.stringify({ nodes: configs }), { mode: 0o600 })
async function start(name: string, args: string[], url: string): Promise<ChildProcess> {
  const logPath = root + `.cache/${name}.log`
  const log = await open(logPath, 'a', 0o600)
  const startAt = (await log.stat()).size
  const child = spawn(root + 'out/wss-relay', args, { cwd: root, stdio: ['ignore', log.fd, log.fd] })
  await log.close()
  processes.push(child)
  await until(async () => {
    assert.equal(child.exitCode, null, `${name} exited; see .cache/${name}.log`)
    const bootLog = (await readFile(logPath)).subarray(startAt).toString('utf8')
    if (!bootLog.includes('relay.v2 listening at')) return false
    return await fetch(url + '/healthz').then(response => response.ok).catch(() => false)
  }, `${name} startup`)
  return child
}
async function startNode(id: string, index: number): Promise<ChildProcess> {
  return start(id, ['--role', 'relay', '--node-id', id, '--listen', `127.0.0.1:${9641 + index}`, '--management-listen', `127.0.0.1:${9612 + index}`, '--public-url', `wss://localhost:${9641 + index}/v2/relay`, '--operator-token-file', tokenFile, '--tls-cert', '.cache/dev-tls/cert.pem', '--tls-key', '.cache/dev-tls/key.pem'], `https://localhost:${9641 + index}`)
}
async function startCoordinator(): Promise<ChildProcess> {
  return start('coordinator', ['--role', 'coordinator', '--listen', '127.0.0.1:9640', '--management-listen', '127.0.0.1:9611', '--nodes-config', registry, '--operator-token-file', tokenFile, '--tls-cert', '.cache/dev-tls/cert.pem', '--tls-key', '.cache/dev-tls/key.pem'], 'https://localhost:9640')
}
async function stop(child: ChildProcess, signal: NodeJS.Signals = 'SIGTERM'): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return
  const done = new Promise<void>(resolve => child.once('exit', () => resolve()))
  child.kill(signal)
  await done
}
async function until(check: () => boolean | Promise<boolean>, label: string): Promise<void> {
  const deadline = performance.now() + 15000
  while (!await check()) {
    assert.ok(performance.now() < deadline, `${label} timed out; ${sessionErrors.slice(0, 3).join('; ')}`)
    await new Promise(resolve => setTimeout(resolve, 80))
  }
}
async function api<T>(method: string, path: string, bearer = '', body?: unknown): Promise<T> {
  const response = await fetch('https://localhost:9640/v2/api' + path, { method,
    headers: { ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  if (response.status === 429 && path === '/sessions') {
    await new Promise(resolve => setTimeout(resolve, 1100))
    return api(method, path, bearer, body)
  }
  assert.ok(response.ok, `${method} ${path}: ${response.status}`)
  return response.status === 204 ? undefined as T : await response.json() as T
}
async function management<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch('http://127.0.0.1:9611' + path, { method,
    headers: { Authorization: `Bearer ${token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  assert.ok(response.ok, `management ${path}: ${response.status}`)
  return await response.json() as T
}
async function room() {
  const alice = await api<{ token: string; participantId: string }>('POST', '/sessions', '', {})
  const bob = await api<{ token: string; participantId: string }>('POST', '/sessions', '', {})
  const lobby = await api<{ id: string }>('POST', '/lobbies', alice.token, { capacity: 2, applicationProtocol: 'cluster.v1', visibility: 'private' })
  await api('POST', `/lobbies/${lobby.id}/join`, bob.token, { revision: 1 })
  await api('POST', `/lobbies/${lobby.id}/ready`, alice.token, { revision: 2, ready: true })
  await api('POST', `/lobbies/${lobby.id}/ready`, bob.token, { revision: 3, ready: true })
  await api('POST', `/lobbies/${lobby.id}/start`, alice.token, { revision: 4 })
  const a = await api<Assignment>('GET', `/lobbies/${lobby.id}/assignment`, alice.token)
  const b = await api<Assignment>('GET', `/lobbies/${lobby.id}/assignment`, bob.token)
  assert.equal(a.roomInstanceId, b.roomInstanceId)
  return { alice, bob, lobby, a, b }
}
function sdk(assignment: Assignment, onEvent?: () => void): RelayClient {
  const client = new RelayClient({ onError(error) { sessionErrors.push(error.message) }, onEvent })
  clients.push(client);client.connect(assignment);return client
}
const report: Record<string, unknown> = {}
try {
  let nodeA = await startNode('node-a', 0)
  const nodeB = await startNode('node-b', 1)
  let coordinator = await startCoordinator()
  const first = await room()
  const second = await room()
  assert.notEqual(first.a.nodeId, second.a.nodeId, 'rooms did not distribute')
  let events = 0
  const alice = sdk(first.a)
  const bob = sdk(first.b, () => events++)
  const carol = sdk(second.a)
  const dan = sdk(second.b)
  await until(() => [alice, bob, carol, dan].every(client => client.status === 'active'), 'two-node WSS')
  alice.publishState(new TextEncoder().encode('first-room'))
  carol.publishState(new TextEncoder().encode('second-room'))
  await until(() => bob.states.has(first.alice.participantId) && dan.states.has(second.alice.participantId), 'room states')
  assert.ok(!bob.states.has(second.alice.participantId))
  assert.ok(!dan.states.has(first.alice.participantId))
  report.sameRoomSingleOwnerAndIsolation = true

  await management('POST', `/v2/nodes/${first.a.nodeId}/drain`, { draining: true })
  const third = await room()
  assert.equal(third.a.nodeId, second.a.nodeId, 'new room placed on draining node')
  alice.reconnect()
  await until(() => alice.status === 'active' && alice.welcome!.generation === 2, 'resume during drain')
  report.drainPreservesExistingRoomAndResume = true

  await stop(coordinator)
  alice.publishEvent(1, new TextEncoder().encode('coordinator-offline'))
  await until(() => events === 1, 'gameplay while coordinator stopped')
  coordinator = await startCoordinator()
  const recovered = await api<Assignment>('GET', `/lobbies/${first.lobby.id}/assignment`, first.alice.token)
  assert.equal(recovered.roomInstanceId, first.a.roomInstanceId)
  assert.equal(recovered.nodeId, first.a.nodeId)
  assert.ok(recovered.connectTicket)
  report.coordinatorRestartKeepsWssAndRecoversAssignments = true

  // Kill only the first room's node, then restart it at the exact same addresses.
  assert.equal(first.a.nodeId, 'node-a')
  await stop(nodeA, 'SIGKILL')
  await until(async () => (await management<{ nodeId: string; healthy: boolean }[]>('GET', '/v2/nodes')).some(node => node.nodeId === 'node-a' && !node.healthy), 'node loss detection')
  nodeA = await startNode('node-a', 0)
  await until(async () => (await management<{ nodeId: string; healthy: boolean }[]>('GET', '/v2/nodes')).some(node => node.nodeId === 'node-a' && node.healthy), 'node restart detection')
  const rejected = await fetch(`https://localhost:9640/v2/api/lobbies/${first.lobby.id}/assignment`, { method: 'POST', headers: { Authorization: `Bearer ${first.alice.token}`, 'Content-Type': 'application/json' }, body: '{}' })
  assert.equal(rejected.status, 410, 'old node authority resurrected')
  report.nodeCrashAndRestartDoNotResurrectOldRoom = true

  await management('POST', '/v2/nodes/node-a/drain', { draining: true })
  await startNode('node-c', 2)
  configs = [...configs, nodeConfig('node-c', 2)]
  await writeFile(registry, JSON.stringify({ nodes: configs }), { mode: 0o600 })
  await management('POST', '/v2/nodes/reload', {})
  const fourth = await room()
  assert.equal(fourth.a.nodeId, 'node-c')
  report.scaleOutRegistrationAffectsOnlyNewRooms = true

  // B has two active allocations. End them after drain, then verify a clean process exit.
  await management('POST', '/v2/nodes/node-b/drain', { draining: true })
  for (const client of [carol, dan]) client.close()
  for (const allocation of [second.a, third.a]) {
    const response = await fetch(`http://127.0.0.1:9613/v2/rooms/${allocation.roomInstanceId}`, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } })
    assert.equal(response.status, 204)
  }
  await until(() => nodeB.exitCode !== null, 'drained node shutdown')
  assert.equal(nodeB.exitCode, 0)
  report.drainedNodeExitsAfterLastRoom = true
  report.transport = 'actual coordinator and relay child processes; HTTPS + WSS; explicit CA trust'
  await writeFile(root + 'docs/evidence/v2/2026-10-22-cluster-processes.json', JSON.stringify(report, null, 2) + '\n')
  console.log(JSON.stringify(report))
} finally {
  for (const client of clients) client.close()
  for (const child of processes.reverse()) await stop(child)
}
