import { mountGameViewport } from '@minbgames/web-kit/viewport'
import { Capacitor } from '@capacitor/core'
import { App } from '@capacitor/app'
import { RelayClient, type Assignment } from '../relay/client.ts'
import { LobbyClient, type Lobby, type MatchTicket } from '../relay/api.ts'
import './style.css'

const viewport = mountGameViewport({ referenceWidth: 1280, referenceHeight: 800, maxAspectRatio: 1.8 })
const element = <T extends HTMLElement>(id: string) => document.getElementById(id)! as T
const button = (id: string) => element<HTMLButtonElement>(id)
const roomInput = element<HTMLInputElement>('room-id')
const canvas = element<HTMLCanvasElement>('arena')
const context = canvas.getContext('2d')!
const apiBase = import.meta.env.VITE_RELAY_API_URL || '/v2/api'
const api = new LobbyClient(apiBase,error => addLog(error.message))
const encoder = new TextEncoder()
const decoder = new TextDecoder()
const log: string[] = []
let lobbyId = ''
let ticketId = ''
let poll: ReturnType<typeof setTimeout> | undefined
let ownReady = false
let joined = false
let disposed = false
let pulse = 0
const lifeId = crypto.randomUUID()
let own = { x: .35, y: .5, lifeId }
let target = { x: own.x, y: own.y }
const drawings = new Map<string, { x: number; y: number }>()

const relay = new RelayClient({
  applicationProtocol: 'relay-demo.v1',
  onStatus(status) {
    const labels = { idle: '대기', connecting: '연결 중', active: '연결됨', reconnecting: '재접속 중', suspended: '앱 일시 중단', closed: '연결 종료' }
    element('connection').textContent = labels[status]
    for (const id of ['pulse', 'reconnect', 'leave']) button(id).disabled = status !== 'active'
    document.querySelector('.stage-heading h2')!.textContent = status === 'active' ? '같은 공간에 연결됐어요' : labels[status]
    if (status === 'active') {
      relay.publishState(encoder.encode(JSON.stringify(own)))
      addLog(`연결 완료 · 세대 ${relay.welcome?.generation}`)
    }
  },
  onEvent(event) {
    try {
      const payload = JSON.parse(decoder.decode(event.payload))
      if (event.kind !== 1 || payload.action !== 'pulse' || typeof payload.id !== 'string') return
      pulse = performance.now()
      addLog(`✦ ${event.participantId.slice(0, 12)}의 이벤트 수신`)
    } catch { addLog('잘못된 게임 이벤트를 거부했습니다.') }
  },
  onMessage(message) {
    if (message.body.case === 'eventAccepted') addLog(`이벤트 ${message.body.value.clientSequence} 서버 접수`)
    renderMembers()
  },
  onError(error) { addLog(error.message) },
  refreshAssignment: () => api.refresh(lobbyId),
})

function addLog(message: string): void {
  log.unshift(`${new Date().toLocaleTimeString('ko-KR', { hour12: false })}  ${message}`)
  log.length = Math.min(log.length, 8)
  element('events').replaceChildren(...log.map(text => { const li = document.createElement('li'); li.textContent = text; return li }))
}

function renderMembers(): void {
  const rows = [...relay.peers.values()].map(peer => {
    const state = peer.participantId === api.participantId ? own : decodePosition(relay.states.get(peer.participantId)?.payload)
    const li = document.createElement('li')
    li.textContent = `${peer.participantId.slice(0, 14)} · g${peer.generation} · ${state ? `${state.x.toFixed(2)}, ${state.y.toFixed(2)}` : '상태 대기'}${peer.status === 2 ? ' · 단절' : ''}`
    return li
  })
  element('peers').replaceChildren(...rows)
}

function decodePosition(payload?: Uint8Array): { x: number; y: number; lifeId: string } | undefined {
  if (!payload) return
  try {
    const value = JSON.parse(decoder.decode(payload))
    if (typeof value.x !== 'number' || typeof value.y !== 'number' || !Number.isFinite(value.x) || !Number.isFinite(value.y) ||
      value.x < 0 || value.x > 1 || value.y < 0 || value.y > 1 || typeof value.lifeId !== 'string') return
    return value
  } catch { return }
}

async function action(operation: () => Promise<void>): Promise<void> {
  try { await operation() } catch (error) { addLog(error instanceof Error ? error.message : '요청 실패') }
}

button('create').onclick = () => { void action(async () => {
  const lobby = await api.create()
  enterLobby(lobby)
}) }
button('join').onclick = () => { void action(async () => {
  const id = roomInput.value.trim()
  if (!id) throw new Error('방 코드를 입력하세요.')
  enterLobby(await api.join(id, 'relay-demo.v1'))
}) }
button('ready').onclick = () => { void action(async () => { scheduleLobby(await api.ready(lobbyId, !ownReady)) }) }
button('lobby-leave').onclick = () => { void action(async () => {
  await api.leaveLobby(lobbyId)
  clearTimeout(poll)
  lobbyId = ''
  roomInput.value = ''
  for (const id of ['create', 'join', 'match']) button(id).disabled = false
  for (const id of ['ready', 'start', 'lobby-leave']) button(id).disabled = true
  element('room-status').textContent = '대기방에서 나왔습니다.'
}) }
button('start').onclick = () => { void action(async () => {
  await api.start(lobbyId)
  button('start').disabled = true
}) }
button('match').onclick = () => { void action(async () => {
  button('match').disabled = true
  const ticket = await api.queue()
  ticketId = ticket.id
  button('cancel').hidden = false
  scheduleTicket(ticket)
}) }
button('cancel').onclick = () => { void action(async () => {
  await api.cancel(ticketId)
  clearTimeout(poll)
  ticketId = ''
  button('cancel').hidden = true
  button('match').disabled = false
  element('room-status').textContent = '매칭을 취소했습니다.'
}) }
button('pulse').onclick = () => {
  try {
    relay.publishEvent(1, encoder.encode(JSON.stringify({ action: 'pulse', id: crypto.randomUUID() })))
    pulse = performance.now()
  } catch (error) { addLog((error as Error).message) }
}
button('reconnect').onclick = () => relay.reconnect()
button('leave').onclick = () => { relay.leave(); addLog('방에서 퇴장했습니다.') }

function enterLobby(lobby: Lobby): void {
  lobbyId = lobby.id
  roomInput.value = lobby.id
  button('create').disabled = true
  button('join').disabled = true
  button('match').disabled = true
  clearTimeout(poll)
  scheduleLobby(lobby)
}
function scheduleLobby(lobby: Lobby): void {
  ownReady = lobby.ready[api.participantId] ?? false
  button('ready').disabled = lobby.status !== 'open' || lobby.allocationPending || lobby.allocating
  button('ready').textContent = ownReady ? '준비 취소' : '준비하기'
  button('lobby-leave').disabled = lobby.status !== 'open' || lobby.allocationPending || lobby.allocating
  const allReady = lobby.participants.every(id => lobby.ready[id])
  element('room-status').textContent = `방 ${lobby.id} · ${lobby.participants.length}/${lobby.capacity}명 · 준비 ${Object.values(lobby.ready).filter(Boolean).length}명`
  button('start').disabled = lobby.allocating || lobby.ownerId !== api.participantId || lobby.participants.length !== lobby.capacity || !allReady || lobby.status === 'matched'
  button('start').textContent = lobby.allocationPending ? '배정 다시 시도' : '방 시작'
  if (disposed || joined) return
  if (lobby.status === 'matched') {
    void action(async () => connect(await api.assignment(lobby.id)))
  } else poll = setTimeout(() => { void action(async () => scheduleLobby(await api.get(lobby.id))) }, 750)
}
function scheduleTicket(ticket: MatchTicket): void {
  element('room-status').textContent = '매칭 상대를 기다리고 있습니다.'
  if (disposed || joined) return
  if (ticket.status === 'ended') {
    element('room-status').textContent = '매치가 종료됐습니다. 다시 매칭할 수 있습니다.'
    button('cancel').hidden = true
    button('match').disabled = false
    return
  }
  if (ticket.assignment) {
    button('cancel').hidden = true
    connect(ticket.assignment)
  } else poll = setTimeout(() => { void action(async () => scheduleTicket(await api.ticket(ticketId))) }, 750)
}
function connect(assignment: Assignment): void {
  joined = true
  lobbyId = assignment.roomId
  relay.connect(assignment)
}

canvas.addEventListener('pointerdown', event => {
  if (relay.status !== 'active') return
  const rect = canvas.getBoundingClientRect()
  target = { x: Math.max(.04, Math.min(.96, (event.clientX - rect.left) / rect.width)), y: Math.max(.06, Math.min(.94, (event.clientY - rect.top) / rect.height)) }
})
canvas.addEventListener('keydown', event => {
  const directions: Record<string, [number, number]> = { ArrowLeft: [-.1, 0], ArrowRight: [.1, 0], ArrowUp: [0, -.1], ArrowDown: [0, .1] }
  const delta = directions[event.key]
  if (!delta || relay.status !== 'active') return
  event.preventDefault()
  target = { x: Math.max(.04, Math.min(.96, target.x + delta[0])), y: Math.max(.06, Math.min(.94, target.y + delta[1])) }
})
let previous = performance.now()
let lastPublish = 0
let lastMembers = 0
let frame = 0
function draw(now: number): void {
  const dt = Math.min(.05, (now - previous) / 1000)
  previous = now
  const factor = 1 - Math.exp(-12 * dt)
  const oldX = own.x, oldY = own.y
  own = { ...own, x: own.x + (target.x - own.x) * factor, y: own.y + (target.y - own.y) * factor }
  if (relay.status === 'active' && now - lastPublish >= 50 && (Math.abs(own.x - oldX) + Math.abs(own.y - oldY) > .00001)) {
    relay.publishState(encoder.encode(JSON.stringify(own)))
    lastPublish = now
  }
  const width = canvas.clientWidth
  const height = canvas.clientHeight
  const pixelRatio = Math.min(devicePixelRatio || 1, 2)
  if (canvas.width !== Math.round(width * pixelRatio) || canvas.height !== Math.round(height * pixelRatio)) {
    canvas.width = Math.round(width * pixelRatio)
    canvas.height = Math.round(height * pixelRatio)
  }
  context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0)
  context.clearRect(0, 0, width, height)
  context.strokeStyle = '#dbe4d5'
  context.lineWidth = 1
  for (let x = 20; x < width; x += 40) { context.beginPath(); context.moveTo(x, 0); context.lineTo(x, height); context.stroke() }
  for (let y = 20; y < height; y += 40) { context.beginPath(); context.moveTo(0, y); context.lineTo(width, y); context.stroke() }
  const points = new Map<string, { x: number; y: number }>()
  if (joined) points.set(api.participantId, own)
  for (const [id, state] of relay.states) {
    if (id === api.participantId) continue
    const point = decodePosition(state.payload)
    if (!point) continue
    const shown = drawings.get(id) ?? point
    const smoothed = { x: shown.x + (point.x - shown.x) * factor, y: shown.y + (point.y - shown.y) * factor }
    drawings.set(id, smoothed)
    points.set(id, smoothed)
  }
  for (const id of drawings.keys()) if (!relay.peers.has(id)) drawings.delete(id)
  for (const [id, point] of points) {
    const x = point.x * width, y = point.y * height
    const self = id === api.participantId
    context.fillStyle = self ? '#367754' : '#c38d48'
    context.shadowColor = '#234c3020'; context.shadowBlur = 16
    context.beginPath(); context.arc(x, y, 16, 0, Math.PI * 2); context.fill()
    context.shadowBlur = 0
    context.fillStyle = '#fff'; context.beginPath(); context.arc(x - 4, y - 3, 2, 0, Math.PI * 2); context.arc(x + 4, y - 3, 2, 0, Math.PI * 2); context.fill()
    context.fillStyle = '#567460'; context.font = '13px monospace'; context.textAlign = 'center'
    context.fillText(self ? 'YOU' : id.slice(0, 10), x, y + 36)
    if (now - pulse < 600) {
      context.strokeStyle = '#b9985470'; context.lineWidth = 2
      context.beginPath(); context.arc(x, y, 22 + (now - pulse) / 10, 0, Math.PI * 2); context.stroke()
    }
  }
  if (now - lastMembers > 500) { renderMembers(); lastMembers = now; element('latency').textContent = `RTT ${relay.rttMs === undefined ? '—' : `${Math.round(relay.rttMs)} ms`}` }
  frame = requestAnimationFrame(draw)
}
frame = requestAnimationFrame(draw)

// Native app lifecycle owns reconnect; web background tabs keep their session.
if (Capacitor.isNativePlatform()) {
  void App.addListener('appStateChange', ({ isActive }) => { if (isActive) { void api.renew().catch(error => addLog(error.message)).finally(() => relay.resume()) } else relay.suspend() })
}
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && relay.status === 'active') relay.resync()
})
window.addEventListener('pagehide', () => {
  disposed = true
  clearTimeout(poll)
  cancelAnimationFrame(frame)
  relay.close()
  api.dispose()
  viewport.dispose()
}, { once: true })
void action(async () => {
  await api.guest()
  element('identity').textContent = api.participantId
  for (const id of ['create', 'join', 'match']) button(id).disabled = false
  addLog('게스트 인증 완료')
})
// MinbWebKit's shared debugger remains available without game-specific copies.
if (__MINB_SR_DEBUG__) {
  void import('@minbgames/web-kit/debugger').then(({ mountDebugger }) => mountDebugger({
    badge: { text: 'SR' },
    settings: { triggerMode: 'enabled', triggerPosition: 'bottomRight', triggerSizePx: 36, tapCount: 2, tapWindowMs: 500, keyboardShortcut: true, requireEntryCode: false, entryCode: '', logCapacity: 200 },
    pages: () => [],
    info: () => ({ participant: api.participantId, connection: relay.status, protocol: 'relay.v2' }),
  }))
}
