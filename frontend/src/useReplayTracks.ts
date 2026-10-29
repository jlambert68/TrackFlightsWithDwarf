import { useEffect, useRef, useState } from 'react'
import type { TrackPoint } from './trackHistory'

type RecordedPoint = TrackPoint & { icao: string; timestamp: string }
type Cache = { session: string; points: Map<string, TrackPoint[]> }

// Keep only fixes for currently active aircraft, rather than retaining the
// day's receiver frames. The server streams the older portion of each track.
export function useReplayTracks(date: string, before: number, aircraft: string[], expiry: number) {
  const [result, setResult] = useState<Cache>({session: '', points: new Map()})
  const [error, setError] = useState('')
  const cache = useRef<Cache>({session: '', points: new Map()})
  const ids = [...aircraft].sort().join(',')
  useEffect(() => {
    const session = `${date}:${before}:${expiry}`, wanted = ids ? ids.split(',') : []
    if (cache.current.session !== session) cache.current = {session, points: new Map()}
    const current = cache.current
    for (const id of current.points.keys()) if (!wanted.includes(id)) current.points.delete(id)
    setResult({session, points: new Map(current.points)}); setError('')
    if (!date || !before || !wanted.length) return
    const controller = new AbortController()
    const missing = wanted.filter(id => !current.points.has(id))
    const load = async () => {
      try {
        for (let offset = 0; offset < missing.length; offset += 32) {
          const group = missing.slice(offset, offset + 32), query = new URLSearchParams({date, before: String(before)})
          for (const id of group) query.append('icao', id)
          const response = await fetch(`/api/v1/flights/history/tracks?${query}`, {signal: controller.signal, cache: 'no-store'})
          if (!response.ok || !response.body) throw new Error('Recorded tracks unavailable')
          const reader = response.body.getReader(), decoder = new TextDecoder(), result = new Map(group.map(id => [id, [] as TrackPoint[]]))
          let buffer = '', complete = false
          while (true) {
            const {value, done} = await reader.read(); if (done) break
            buffer += decoder.decode(value, {stream: true}); let newline: number
            while ((newline = buffer.indexOf('\n')) >= 0) {
              const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1); if (!line) continue
              const message = JSON.parse(line)
              if (message.type === 'chunk') for (const point of message.points as RecordedPoint[]) result.get(point.icao)?.push(point)
              else if (message.type === 'end') complete = true
              else if (message.type === 'error') throw new Error(message.error)
            }
          }
          if (!complete) throw new Error('Recorded track stream ended unexpectedly')
          if (controller.signal.aborted || cache.current !== current) return
          for (const [id, points] of result) current.points.set(id, points)
          setResult({session, points: new Map(current.points)})
        }
      } catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Recorded tracks unavailable') }
    }
    void load()
    return () => controller.abort()
  }, [date, before, ids, expiry])
  return {tracks: result.session === `${date}:${before}:${expiry}` ? result.points : new Map<string, TrackPoint[]>(), error}
}
