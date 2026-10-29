import { mergeHistoryPoints, refreshBefore, type TrackPoint } from './trackHistory.ts'

export type TracePoint = TrackPoint & {timestamp: string; altitudeFeet: number}
export type TraceResponse = {lookbackSeconds: number; points: TracePoint[]}
type Track = {latest: TrackPoint; observedAt: number; queued: string[]; controller?: AbortController; disabled: boolean; points: TracePoint[]; failed: boolean; error?: string; loaded: boolean}

// Each track keeps its first request ahead of later position requests. In-flight
// work is bounded globally and later updates coalesce per aircraft.
export class FlightHistoryManager {
  private tracks = new Map<string, Track>()
  private active = 0
  private stopped = false
  private cursor = 0
  private readonly live: boolean
  private readonly request: (icao: string, before: string, signal: AbortSignal) => Promise<TraceResponse>
  private readonly changed: (points: Map<string, TracePoint[]>, status: string) => void
  private readonly now: () => number
  constructor(live: boolean, request: (icao: string, before: string, signal: AbortSignal) => Promise<TraceResponse>, changed: (points: Map<string, TracePoint[]>, status: string) => void, now = () => Date.now()) { this.live = live; this.request = request; this.changed = changed; this.now = now }

  update(signals: Map<string, TrackPoint[]>, cursor: number) {
    if (this.stopped) return
    let changed = false
    if (!this.live && this.cursor && cursor < this.cursor) { this.clear(); changed = true }
    this.cursor = cursor
    for (const [icao, track] of this.tracks) if (!signals.has(icao)) { track.controller?.abort(); this.tracks.delete(icao); changed = true }
    for (const [icao, points] of signals) {
      const valid = points.filter(p => Number.isFinite(Date.parse(p.timestamp ?? ''))).slice().sort((a, b) => Date.parse(a.timestamp!) - Date.parse(b.timestamp!))
      if (!valid.length) continue
      let track = this.tracks.get(icao)
      if (!track) {
        track = {latest: valid[0], observedAt: this.now(), queued: [valid[0].timestamp!], disabled: false, points: [], failed: false, loaded: false}
        this.tracks.set(icao, track)
        changed = true
      }
      const latest = valid[valid.length - 1]
      if (Date.parse(latest.timestamp!) > Date.parse(track.latest.timestamp!)) {
        track.latest = latest; track.observedAt = this.now(); this.queue(track, latest.timestamp!)
      }
    }
    if (changed) this.publish()
    this.pump()
  }

  refresh() {
    if (this.stopped) return
    for (const track of this.tracks.values()) {
      const before = this.live ? refreshBefore(track.latest, track.observedAt, this.now()) : track.failed ? track.latest.timestamp : undefined
      if (before) this.queue(track, before)
    }
    this.pump()
  }

  dispose() { this.stopped = true; this.clear() }
  private clear() { for (const track of this.tracks.values()) track.controller?.abort(); this.tracks.clear() }
  private queue(track: Track, before: string) {
    if (track.disabled) return
    const ms = Date.parse(before)
    if (!Number.isFinite(ms)) return
    if (track.queued.length === 0) track.queued.push(before)
    else if (ms > Date.parse(track.queued[track.queued.length - 1])) {
      if (track.controller || track.queued.length > 1) track.queued[track.queued.length - 1] = before
      else track.queued.push(before)
    }
  }
  private pump() {
    if (this.stopped) return
    for (const [icao, track] of this.tracks) {
      if (this.active >= 4) break
      if (track.controller || !track.queued.length || track.disabled) continue
      const before = track.queued.shift()!, controller = new AbortController(); track.controller = controller; this.active++
      void this.request(icao, before, controller.signal).then(result => {
        if (controller.signal.aborted || this.tracks.get(icao) !== track) return
        if (result.lookbackSeconds === 0) { track.disabled = true; track.queued = []; track.points = [] }
        else track.points = mergeHistoryPoints(track.points, result.points, before).slice(-10000)
        track.failed = false; track.error = undefined; track.loaded = true
      }).catch(error => { if (!controller.signal.aborted && this.tracks.get(icao) === track) { track.failed = true; track.error = error instanceof Error ? error.message : 'External history unavailable' } }).finally(() => {
        this.active--; if (track.controller === controller) track.controller = undefined
        if (!this.stopped) { this.publish(); this.pump() }
      })
    }
  }
  private publish() {
    if (this.stopped) return
    const map = new Map<string, TracePoint[]>(); let failures = 0, disabled = false, available = 0, loaded = 0, error = ''
    for (const [icao, track] of this.tracks) { map.set(icao, track.points); if (track.failed) { failures++; error ||= track.error ?? '' }; if (track.disabled) disabled = true; if (track.points.length) available++; if (track.loaded) loaded++ }
    this.changed(map, failures ? `${error || 'External history unavailable'}; receiver tracks retained` : disabled ? 'External history disabled' : available ? 'External history follows received positions' : loaded ? 'No external history found for these aircraft' : map.size ? 'Loading external history…' : '')
  }
}
