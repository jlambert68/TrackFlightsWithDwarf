import type { TrackPoint } from './trackHistory'

export type Motion = {speedKnots: number; headingDegrees: number}
export function captureMotion(speed: number | undefined, heading: number | undefined): Motion | null {
  if (speed == null || heading == null || !Number.isFinite(speed) || speed < 0 || !Number.isFinite(heading)) return null
  return {speedKnots: speed, headingDegrees: ((heading % 360) + 360) % 360}
}
export function projectPosition(origin: TrackPoint, motion: Motion, seconds: number): TrackPoint | null {
  if (!Number.isFinite(origin.latitude) || Math.abs(origin.latitude) > 90 || !Number.isFinite(origin.longitude) || Math.abs(origin.longitude) > 180 || !Number.isFinite(seconds) || seconds < 0) return null
  const radians = Math.PI / 180, distance = motion.speedKnots * 1852 * seconds / 3600 / 6371008.8
  const lat = origin.latitude * radians, lon = origin.longitude * radians, bearing = motion.headingDegrees * radians
  const nextLat = Math.asin(Math.sin(lat) * Math.cos(distance) + Math.cos(lat) * Math.sin(distance) * Math.cos(bearing))
  const nextLon = lon + Math.atan2(Math.sin(bearing) * Math.sin(distance) * Math.cos(lat), Math.cos(distance) - Math.sin(lat) * Math.sin(nextLat))
  return {latitude: nextLat / radians, longitude: ((nextLon / radians + 540) % 360) - 180, altitudeFeet: origin.altitudeFeet}
}
export function historyDiverges(origin: TrackPoint, motion: Motion, history: TrackPoint[], limit = 500): boolean {
  const start = Date.parse(origin.timestamp ?? '')
  if (!Number.isFinite(start)) return false
  // Reconstruct from the received fix using the same five-second steps used
  // to draw estimates; nearby history points can share the preceding steps.
  let cursor: TrackPoint = origin, completed = 0
  const ordered = history.filter(point => Date.parse(point.timestamp ?? '') > start).slice().sort((a, b) => Date.parse(a.timestamp!) - Date.parse(b.timestamp!))
  for (const point of ordered) {
    const elapsed = (Date.parse(point.timestamp ?? '') - start) / 1000
    if (!Number.isFinite(elapsed) || elapsed <= 0) continue
    while (completed + 5 <= elapsed) { const next = projectPosition(cursor, motion, 5); if (!next) return false; cursor = next; completed += 5 }
    const projected = projectPosition(cursor, motion, elapsed - completed)
    if (!projected) continue
    const rad = Math.PI / 180, dLat = (point.latitude - projected.latitude) * rad, dLon = (point.longitude - projected.longitude) * rad
    const a = Math.sin(dLat / 2) ** 2 + Math.cos(projected.latitude * rad) * Math.cos(point.latitude * rad) * Math.sin(dLon / 2) ** 2
    if (6371008.8 * 2 * Math.atan2(Math.sqrt(Math.min(1, a)), Math.sqrt(Math.max(0, 1 - a))) >= limit) return true
  }
  return false
}
