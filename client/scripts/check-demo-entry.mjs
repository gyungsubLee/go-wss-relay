// Run: node --disable-warning=ExperimentalWarning --experimental-transform-types scripts/check-demo-entry.mjs
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ControlError } from '../src/relay/api.ts'

const source = readFileSync(new URL('../src/demo/main.ts', import.meta.url), 'utf8')
function block(start, end) {
  const from = source.indexOf(start)
  const to = source.indexOf(end, from)
  assert.ok(from >= 0 && to > from, `production block: ${start}`)
  return source.slice(from, to)
}
// Execute the real state, entry/exit handlers, polling and disposal. Only DOM,
// timers and external API/relay boundaries are substituted; no handler is copied.
const production = ts.transpileModule([
  block('let lobbyId =', 'let pulse ='),
  block('async function action(', "canvas.addEventListener('pointerdown'"),
  block("window.addEventListener('pagehide'", 'void action(async () => {\n  await api.guest()'),
].join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const entries = ['create', 'join', 'match']
const flush = () => new Promise(resolve => setImmediate(resolve))
const lobby = {
  id: 'l-first', ownerId: 'peer', participants: ['peer'], capacity: 2,
  ready: { peer: false }, status: 'open', allocating: false,
  allocationPending: false, revision: 1, visibility: 'private',
}
const ticket = { id: 'q-first', status: 'queued' }
const assignment = { roomId: 'l-first' }
const result = entry => entry === 'match' ? ticket : lobby

function fixture() {
  const buttons = new Map()
  const elements = new Map()
  const requests = []
  const timers = new Map()
  const listeners = new Map()
  const logs = []
  const connections = []
  let timerId = 0
  const button = id => {
    if (!buttons.has(id)) buttons.set(id, { disabled: !entries.includes(id), hidden: id === 'cancel' })
    return buttons.get(id)
  }
  const element = id => {
    if (!elements.has(id)) elements.set(id, { textContent: '' })
    return elements.get(id)
  }
  const api = { participantId: 'peer', dispose() {} }
  for (const method of ['create', 'join', 'queue', 'get', 'ticket', 'assignment', 'leaveLobby', 'cancel', 'ready', 'start']) {
    api[method] = (...args) => new Promise((resolve, reject) => requests.push({ method, args, resolve, reject }))
  }
  const context = vm.createContext({
    Error, ControlError, api, button, element, roomInput: { value: 'l-existing' },
    snakeProtocol: 'snake-demo.v1', addLog: message => logs.push(message),
    setTimeout(callback) { timers.set(++timerId, callback); return timerId },
    clearTimeout(id) { timers.delete(id) },
    window: { addEventListener: (event, callback) => listeners.set(event, callback) },
    relay: { status: 'idle', connect: value => connections.push(value), close() {} },
    performance: { now: () => 0 }, authorityTimer: undefined, frame: 0,
    cancelAnimationFrame() {}, viewport: { dispose() {} }, finishGame() {},
  })
  vm.runInContext(production, context)
  return {
    requests, timers, logs, connections, context, button, element,
    click: id => button(id).onclick(),
    dispose: () => listeners.get('pagehide')(),
    state: () => vm.runInContext('({lobbyId, ticketId, participationEpoch, joined})', context),
    runPoll() {
      assert.equal(timers.size, 1, 'one participation poll')
      const [id, callback] = timers.entries().next().value
      timers.delete(id)
      callback()
    },
    ui: () => JSON.stringify({ buttons: [...buttons], elements: [...elements] }),
  }
}

function entryDisabled(f, disabled) {
  for (const id of entries) assert.equal(f.button(id).disabled, disabled, `${id} disabled=${disabled}`)
}
function retained(f, entry) {
  assert.equal(f.state().lobbyId, entry === 'match' ? '' : 'l-first')
  assert.equal(f.state().ticketId, entry === 'match' ? 'q-first' : '')
  entryDisabled(f, true)
  if (entry === 'match') assert.equal(f.button('cancel').hidden, false)
  else assert.equal(f.button('lobby-leave').disabled, false)
  assert.equal(f.timers.size, 1, 'successful entry keeps polling')
}
async function enter(f, entry) {
  if (entry === 'join') f.context.roomInput.value = 'l-existing'
  f.click(entry)
  f.requests.at(-1).resolve(result(entry))
  await flush()
  retained(f, entry)
}

// Removing the shared entry guard must fail here before a second server mutation
// can invalidate the successful owner's epoch, even if onclick is called directly.
for (const first of entries) {
  for (const second of entries) {
    const f = fixture()
    f.click(first)
    const epoch = f.state().participationEpoch
    entryDisabled(f, true)
    f.click(second)
    assert.equal(f.requests.length, 1, `${first} then ${second}: one mutation`)
    assert.equal(f.state().participationEpoch, epoch, 'pending owner keeps its epoch')
    f.requests[0].resolve(result(first))
    await flush()
    retained(f, first)
    f.click(second)
    assert.equal(f.requests.length, 1, 'existing membership rejects another entry')
    f.runPoll()
    assert.equal(f.requests[1].method, first === 'match' ? 'ticket' : 'get')
    f.requests[1].resolve(result(first))
    await flush()
    retained(f, first)
    console.log(`PASS ${first} then ${second}: one mutation, membership, controls and polling retained`)
  }
}

for (const entry of entries) {
  const f = fixture()
  f.click(entry)
  f.requests[0].reject(new ControlError(409, 'entry refused'))
  await flush()
  entryDisabled(f, false)
  assert.deepEqual(f.logs, ['entry refused'])
  await enter(f, entry)
  assert.equal(f.requests.length, 2)
  console.log(`PASS ${entry}: failure restores entries and retry succeeds`)
}
{
  const f = fixture()
  f.context.roomInput.value = '   '
  f.click('join')
  await flush()
  assert.equal(f.requests.length, 0)
  entryDisabled(f, false)
  assert.deepEqual(f.logs, ['방 코드를 입력하세요.'])
  await enter(f, 'match')
  console.log('PASS invalid join: no mutation, entries restored and next entry succeeds')
}

for (const entry of entries) {
  for (const outcome of ['success', 'failure']) {
    const f = fixture()
    f.click(entry)
    f.dispose()
    const ui = f.ui()
    if (outcome === 'success') f.requests[0].resolve(result(entry))
    else f.requests[0].reject(new Error('late failure'))
    await flush()
    assert.equal(f.ui(), ui, 'disposed entry must not revive UI')
    assert.equal(f.state().lobbyId, '')
    assert.equal(f.state().ticketId, '')
    assert.equal(f.timers.size, 0)
    assert.equal(f.logs.length, 0)
    f.click('create')
    assert.equal(f.requests.length, 1, 'disposed page rejects entries')
    console.log(`PASS ${entry}: disposal before ${outcome} ignores completion`)
  }
}

for (const outcome of ['success', 'failure']) {
  const f = fixture()
  f.click('create')
  vm.runInContext('invalidateWaiting()', f.context)
  const ui = f.ui()
  if (outcome === 'success') f.requests[0].resolve(lobby)
  else f.requests[0].reject(new Error('stale entry'))
  await flush()
  assert.equal(f.ui(), ui, 'stale entry cleanup must not revive UI')
  assert.equal(f.state().lobbyId, '')
  assert.equal(f.logs.length, 0)
  assert.equal(f.timers.size, 0)
  console.log(`PASS stale entry ${outcome}: cleanup does not revive UI`)
}

for (const entry of ['create', 'match']) {
  const exit = entry === 'match' ? 'cancel' : 'lobby-leave'
  for (const outcome of ['success', 'failure']) {
    const f = fixture()
    await enter(f, entry)
    f.runPoll()
    const query = f.requests.at(-1)
    f.click(exit)
    f.requests.at(-1).resolve()
    await flush()
    entryDisabled(f, false)
    const ui = f.ui()
    if (outcome === 'success') {
      query.resolve(entry === 'match' ? { ...ticket, status: 'matched', assignment } : { ...lobby, status: 'matched' })
    } else query.reject(new Error('stale query'))
    await flush()
    assert.equal(f.ui(), ui, 'exit invalidates late query UI')
    assert.equal(f.connections.length, 0)
    assert.equal(f.timers.size, 0)
    assert.equal(f.requests.length, 3, 'stale query does not fetch assignment or lobby')
    await enter(f, 'join')
    console.log(`PASS ${exit}: late query ${outcome} stays stale; next entry succeeds`)
  }
  {
    const f = fixture()
    await enter(f, entry)
    f.click(exit)
    f.requests.at(-1).reject(new ControlError(503, 'retry exit'))
    await flush()
    retained(f, entry)
    assert.deepEqual(f.logs, ['retry exit'])
    console.log(`PASS ${exit}: retryable failure retains membership and polling`)
  }
  for (const outcome of ['success', 'failure']) {
    const f = fixture()
    await enter(f, entry)
    f.runPoll()
    f.dispose()
    const ui = f.ui()
    if (outcome === 'success') f.requests.at(-1).resolve(result(entry))
    else f.requests.at(-1).reject(new Error('disposed query'))
    await flush()
    assert.equal(f.ui(), ui)
    assert.equal(f.timers.size, 0)
    assert.equal(f.connections.length, 0)
    console.log(`PASS ${entry}: pagehide invalidates query ${outcome}`)
  }
}
console.log('PASS demo entry and affected lifecycle regression checks')
