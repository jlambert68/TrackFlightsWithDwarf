export type TrackPoint = { timestamp?: string; latitude: number; longitude: number; altitudeFeet?: number }
const time = (point: TrackPoint) => Date.parse(point.timestamp ?? '')
export function mergeHistoryPoints<T extends TrackPoint>(existing: T[], incoming: T[], before: string): T[] {
  const end = Date.parse(before), points = new Map<number, T>()
  for (const point of existing) { const t = time(point); if (Number.isFinite(t)) points.set(t, point) }
  for (const point of incoming) { const t = time(point); if (Number.isFinite(t) && t < end && Number.isFinite(point.latitude) && Math.abs(point.latitude) <= 90 && Number.isFinite(point.longitude) && Math.abs(point.longitude) <= 180 && Number.isFinite(point.altitudeFeet)) points.set(t, point) }
  return [...points.values()].sort((a, b) => time(a) - time(b))
}
export function refreshBefore(signal: TrackPoint, observedMilliseconds: number, nowMilliseconds: number, liveWindowMilliseconds = 600000): string | null {
  const t = time(signal)
  if (!Number.isFinite(t) || !Number.isFinite(observedMilliseconds) || !Number.isFinite(nowMilliseconds)) return null
  const end = Math.abs(observedMilliseconds - t) <= liveWindowMilliseconds ? Math.max(t, nowMilliseconds) : t + Math.max(0, nowMilliseconds - observedMilliseconds)
  return new Date(end).toISOString()
}
export function splitAtGaps(points: TrackPoint[], gap = 120000): TrackPoint[][] {
  const sorted = points.filter(point => Number.isFinite(time(point))).slice().sort((a, b) => time(a) - time(b))
  const result: TrackPoint[][] = []; let segment: TrackPoint[] = []
  for (const point of sorted) { if (segment.length && time(point) - time(segment[segment.length - 1]) >= gap) { if (segment.length > 1) result.push(segment); segment = [] }; segment.push(point) }
  if (segment.length > 1) result.push(segment)
  return result
}
export function unresolvedSignalSegments(signals: TrackPoint[], history: TrackPoint[], gap = 120000, historyGap = gap): TrackPoint[][] {
  const ordered = signals.filter(point => Number.isFinite(time(point))).slice().sort((a, b) => time(a) - time(b))
  const known = splitAtGaps(history, historyGap), result: TrackPoint[][] = []; let current: TrackPoint[] | undefined
  const covered = (start: number, end: number) => known.some(segment => time(segment[0]) <= start && time(segment[segment.length - 1]) >= end)
  const first = ordered[0]
  if (first) {
    const prior = history.filter(point => Number.isFinite(time(point)) && time(point) < time(first)).sort((a, b) => time(a) - time(b)).at(-1)
    if (prior && time(first) - time(prior) < gap && !covered(time(first), time(first))) { current = [prior, first]; result.push(current) }
  }
  for (let i = 1; i < ordered.length; i++) {
    const first = ordered[i - 1], second = ordered[i], start = time(first), end = time(second)
    if (end - start >= gap || covered(start, end)) { current = undefined; continue }
    if (!current || current[current.length - 1] !== first) { current = [first]; result.push(current) }
    current.push(second)
  }
  return result
}
