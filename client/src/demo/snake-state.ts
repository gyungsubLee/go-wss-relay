// Game payload contract only. The relay SDK treats these bytes as opaque data.
export const snakeProtocol = 'snake-demo.v1'
export const maxBodyPoints = 64
export type Point = [number, number]
export interface SharedScene { revision: number; seed: number; ended: boolean }
export interface SnakeState {
  x: number
  y: number
  lifeId: string
  heading: number
  speed: number
  length: number
  mass: number
  score: number
  isAlive: boolean
  bodyPath: Point[]
  shared?: SharedScene
}
const encoder = new TextEncoder()
const decoder = new TextDecoder('utf-8', { fatal: true })
const coordinate = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1
const uint32 = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value >= 0 && value <= 0xffffffff

export function encodeSnakeState(state: SnakeState): Uint8Array {
  const payload = encoder.encode(JSON.stringify({ ...state,
    x: Math.round(state.x * 65535) / 65535,
    y: Math.round(state.y * 65535) / 65535,
    heading: Math.round(state.heading * 1000) / 1000,
    speed: Math.round(state.speed * 1000) / 1000,
    bodyPath: state.bodyPath.map(([x, y]) => [Math.round(x * 65535), Math.round(y * 65535)]),
  }))
  if (!decodeSnakeState(payload)) throw new Error('Invalid full snake state')
  return payload
}

export function decodeSnakeState(payload?: Uint8Array): SnakeState | undefined {
  if (!payload || payload.length > 4096) return
  try {
    const state = JSON.parse(decoder.decode(payload))
    if (!state || typeof state !== 'object' || !coordinate(state.x) || !coordinate(state.y) ||
      typeof state.lifeId !== 'string' || !/^[A-Za-z0-9-]{1,64}$/.test(state.lifeId) ||
      typeof state.heading !== 'number' || !Number.isFinite(state.heading) || Math.abs(state.heading) > 3.142 ||
      typeof state.speed !== 'number' || !Number.isFinite(state.speed) || state.speed < 0 || state.speed > 16 ||
      !uint32(state.mass) || !uint32(state.score) || typeof state.isAlive !== 'boolean' ||
      !Array.isArray(state.bodyPath) || state.bodyPath.length < 1 || state.bodyPath.length > maxBodyPoints ||
      state.length !== state.bodyPath.length) return
    const bodyPath: Point[] = []
    for (const point of state.bodyPath) {
      if (!Array.isArray(point) || point.length !== 2 || !point.every(value => uint32(value) && value <= 65535)) return
      bodyPath.push([point[0] / 65535, point[1] / 65535])
    }
    const scene = state.shared
    if (scene !== undefined && (!scene || !uint32(scene.revision) || scene.revision === 0 || !uint32(scene.seed) || typeof scene.ended !== 'boolean')) return
    return { ...state, bodyPath }
  } catch { return }
}

// Authority is the immutable lobby owner, obtained before opening WSS.
// A disconnected authority keeps its role through the grace period; no migration occurs.
export function acceptScene(authority: string, sender: string, candidate: SharedScene | undefined, previous?: SharedScene): SharedScene | undefined {
  if (sender !== authority || !candidate || previous && candidate.revision <= previous.revision) return previous
  return { ...candidate }
}

// Reserved players are absent from Welcome until their first WSS connection.
// Only disappearance after a confirmed join proves permanent departure.
export function authorityHasLeft(authority: string, joinedBefore: boolean, peers: ReadonlyMap<string, unknown>): boolean {
  return joinedBefore && !peers.has(authority)
}
