import { useEffect, useRef, useState } from 'react'
import { FlightHistoryManager, type TracePoint, type TraceResponse } from './flightHistoryManager'
import type { TrackPoint } from './trackHistory'

export function useFlightTraces(signals: Map<string, TrackPoint[]>, cursor: number, live: boolean, enabled = true, session = '') {
  const [traces, setTraces] = useState<Map<string, TracePoint[]>>(new Map())
  const [status, setStatus] = useState('')
  const manager = useRef<FlightHistoryManager | null>(null)
  const key = [...signals].map(([icao, points]) => `${icao}:${points[0]?.timestamp ?? ''}:${points.at(-1)?.timestamp ?? ''}:${points.length}`).sort().join(',')
  useEffect(() => {
    setTraces(new Map())
    if (!enabled) { setStatus('External history disabled'); manager.current = null; return }
    const current = new FlightHistoryManager(live, async (icao, before, signal): Promise<TraceResponse> => {
      const response = await fetch(`/api/v1/flights/trace?${new URLSearchParams({icao, before})}`, {signal, cache: 'no-store'})
      if (!response.ok) { const result = await response.json().catch(() => null); throw new Error(result?.error || 'External history unavailable') }
      return response.json()
    }, (points, message) => { setTraces(points); setStatus(message) })
    manager.current = current
    const timer = window.setInterval(() => current.refresh(), 16000)
    return () => { current.dispose(); if (manager.current === current) manager.current = null; clearInterval(timer) }
  }, [live, session, enabled])
  useEffect(() => { manager.current?.update(signals, cursor) }, [key, cursor, live, session, enabled])
  return {traces, status}
}

export function useAircraftMetadata(aircraft: {icao: string; callsign?: string}[]) {
  const [values, setValues] = useState<Map<string, Record<string, string>>>(new Map())
  const key = aircraft.map(a => `${a.icao}:${a.callsign ?? ''}`).sort().join(',')
  useEffect(() => {
    const controller = new AbortController(), ids = key ? key.split(',') : []
    setValues(previous => new Map([...previous].filter(([icao]) => ids.some(item => item.startsWith(`${icao}:`)))))
    let running = false
    const load = async () => { if (running) return; running = true; const pending = [...ids]
      const worker = async () => { while (pending.length && !controller.signal.aborted) { const [icao, callsign] = pending.shift()!.split(':'); try { const query = new URLSearchParams({icao, callsign}); const response = await fetch(`/api/v1/flights/metadata?${query}`, {signal: controller.signal}); if (!response.ok) continue; const info: Record<string, string> = await response.json(); if (!controller.signal.aborted) setValues(previous => new Map(previous).set(icao, info)) } catch { /* local aircraft data remains available */ } } }
      await Promise.all(Array.from({length: Math.min(4, pending.length)}, () => worker())); running = false
    }
    void load(); const timer = window.setInterval(() => void load(), 60000)
    return () => { controller.abort(); clearInterval(timer) }
  }, [key])
  return values
}
