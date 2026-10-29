import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './style.css'
import { FlightGlobe, type AircraftDistances } from './components/FlightGlobe'
import { useAircraftMetadata, useFlightTraces } from './useFlightTraces'
import { useReplayTracks } from './useReplayTracks'

type Status = { accessMode: string; services: Record<string, string> }
type Aircraft = { icao: string; callsign?: string; altitudeFeet?: number; speedKnots?: number; headingDeg?: number; latitude?: number; longitude?: number; positionTimestamp?: string; positionReady: boolean; frames: number; lastSeen: string }
type LiveState = { receiver: { name?: string; latitude: number; longitude: number }; aircraft: Aircraft[]; liveAvailable: boolean; updatedAt: string }
type ReplayAircraft = { icao: string; callsign?: string; registration?: string; altitudeFeet?: number; speedKnots?: number; headingDeg?: number; verticalFPM?: number; latitude?: number; longitude?: number; positionTimestamp?: string; positionReference?: string; departureAirport?: string; destinationAirport?: string; raw?: string; downlinkFormat?: number; messageType?: number; frames: number }
type Replay = { date: string; startMilliseconds: number; endMilliseconds: number; windowStartMilliseconds: number; windowEndMilliseconds: number; aircraftExpirySeconds: number; events: { milliseconds: number; aircraft: ReplayAircraft }[]; skippedLines: number }
type AppConfig = { version: number; flight: { homeName?: string; latitude?: number; longitude?: number; tileUrl?: string; terrainUrl?: string; showAirports?: boolean; historyLookbackMinutes?: number; historyBaseUrl?: string; aircraftExpirySeconds?: number; [key: string]: unknown }; [key: string]: unknown }
const sections = ['Overview', 'Flight map', 'Flight replay', 'DWARF camera', 'Object tracking', 'Events', 'Library', 'Publishing', 'Settings', 'Diagnostics']

function AircraftVisibilityControl({config}: {config: AppConfig | null}) {
  const [seconds, setSeconds] = useState('')
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState('')
  useEffect(() => { if (config) setSeconds(String(config.flight.aircraftExpirySeconds ?? 300)) }, [config?.flight.aircraftExpirySeconds])
  const value = Number(seconds), valid = seconds !== '' && Number.isInteger(value) && value >= 1 && value <= 86400
  const save = async () => {
    if (!valid) { setMessage('Enter 1–86400 seconds'); return }
    setSaving(true); setMessage('')
    try {
      const current = await fetch('/api/v1/settings')
      if (!current.ok) throw new Error()
      const latest: AppConfig = await current.json()
      const response = await fetch('/api/v1/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({...latest, flight: {...latest.flight, aircraftExpirySeconds: value}})})
      if (!response.ok) throw new Error()
      const saved: AppConfig = await response.json()
      window.dispatchEvent(new CustomEvent('trackflights-settings-changed', {detail: saved}))
      setMessage('Saved')
    } catch { setMessage('Could not save') } finally { setSaving(false) }
  }
  return <div className="aircraft-visibility"><label>Keep aircraft and track after last signal (seconds)<input aria-label="Keep aircraft and track after last signal (seconds)" type="number" min="1" max="86400" step="1" value={seconds} onChange={e => { setSeconds(e.target.value); setMessage('') }} disabled={!config}/></label><button type="button" onClick={() => void save()} disabled={!config || saving || !valid}>{saving ? 'Saving…' : 'Save time'}</button>{message && <small role="status">{message}</small>}</div>
}

function AircraftDetails({ aircraft, distances, traceStatus, onClose }: { aircraft: ReplayAircraft; distances: AircraftDistances | null; traceStatus: string; onClose: () => void }) {
  const [photoFailed, setPhotoFailed] = useState(false)
  const [metadata, setMetadata] = useState<Record<string, string>>({})
  const [retry, setRetry] = useState(0)
  useEffect(() => setPhotoFailed(false), [aircraft.icao, aircraft.registration, metadata.registration, retry])
  useEffect(() => { const controller = new AbortController(); setMetadata({}); const query = new URLSearchParams({icao: aircraft.icao, callsign: aircraft.callsign ?? ''}); fetch(`/api/v1/flights/metadata?${query}`, {signal: controller.signal}).then(r => r.ok ? r.json() : Promise.reject()).then(setMetadata).catch(() => { if (!controller.signal.aborted) setMetadata({metadataStatus: 'Aircraft metadata unavailable'}) }); return () => controller.abort() }, [aircraft.icao, aircraft.callsign, retry])
  const photo = new URLSearchParams({icao: aircraft.icao, registration: aircraft.registration || metadata.registration || ''})
  const fields: [string, string][] = [
    ['ICAO', aircraft.icao], ['Registration', aircraft.registration || metadata.registration || 'Unavailable'], ['Callsign', aircraft.callsign || 'Unavailable'],
    ['Manufacturer', metadata.manufacturer || 'Unavailable'], ['Model', metadata.model || 'Unavailable'], ['Aircraft type', metadata.typecode || 'Unavailable'], ['Operator', metadata.operator || 'Unavailable'], ['Country', metadata.country || 'Unavailable'],
    ['Departure', aircraft.departureAirport || metadata.departureAirport || 'Unavailable'], ['Destination', aircraft.destinationAirport || metadata.destinationAirport || 'Unavailable'],
    ['Altitude', aircraft.altitudeFeet == null ? 'Unavailable' : `${aircraft.altitudeFeet.toLocaleString()} ft`],
    ['Speed', aircraft.speedKnots == null ? 'Unavailable' : `${Math.round(aircraft.speedKnots)} kt`],
    ['Heading', aircraft.headingDeg == null ? 'Unavailable' : `${Math.round(aircraft.headingDeg)}°`],
    ['Vertical rate', aircraft.verticalFPM == null ? 'Unavailable' : `${aircraft.verticalFPM} ft/min`],
    ['Coordinates', aircraft.latitude == null || aircraft.longitude == null ? 'Unavailable' : `${aircraft.latitude.toFixed(5)}, ${aircraft.longitude.toFixed(5)}`],
    ['Position source', aircraft.positionReference || 'Recorded position'], ['Distance to camera', distances ? `${distances.camera.toLocaleString()} m` : 'Unavailable'], ['Distance to home', distances?.home == null ? 'Unavailable' : `${distances.home.toLocaleString()} m`], ['Messages', String(aircraft.frames)],
    ['Downlink format', aircraft.downlinkFormat?.toString() || 'Unavailable'], ['Message type', aircraft.messageType?.toString() || 'Unavailable'],
  ]
  return <section className="aircraft-info"><header><h2>{aircraft.callsign || aircraft.registration || aircraft.icao}</h2><button onClick={onClose}>Close</button></header><div className="aircraft-info-body"><div><div className="info-grid">{fields.map(([label, value]) => <div key={label}><small>{label}</small><strong>{value}</strong></div>)}</div><p className="muted">{traceStatus} · receiver fixes and fallback in red, internet history in brown, estimates in gold</p>{(metadata.metadataStatus || metadata.routeStatus || photoFailed) && <p className="muted">{metadata.metadataStatus} {metadata.routeStatus} <button onClick={() => setRetry(value => value + 1)}>Retry lookups</button></p>}{aircraft.raw && <p className="muted">Latest frame: <code>{aircraft.raw}</code></p>}</div><figure>{!photoFailed ? <img src={`/api/v1/flights/photo?${photo}`} onError={() => setPhotoFailed(true)} alt={`Aircraft ${aircraft.registration || aircraft.icao}`}/> : <span>Aircraft photo unavailable</span>}<figcaption><a href={`https://www.planespotters.net/hex/${encodeURIComponent(aircraft.icao)}`} target="_blank" rel="noreferrer">Photo via PlaneSpotters</a></figcaption></figure></div></section>
}

function ReplayView({ active }: { active: boolean }) {
  const [dates, setDates] = useState<string[]>([]), [replay, setReplay] = useState<Replay | null>(null), [time, setTime] = useState(0), [playing, setPlaying] = useState(false), [speed, setSpeed] = useState(1), [error, setError] = useState<string | null>(null), [loading, setLoading] = useState(false), [selected, setSelected] = useState<string | null>(null)
  const [selectedDate, setSelectedDate] = useState('')
  const [distances, setDistances] = useState<AircraftDistances | null>(null)
  const [mapConfig, setMapConfig] = useState<AppConfig | null>(null)
  const replayRequest = React.useRef<AbortController | null>(null)
  const replayState = React.useRef({date: '', time: 0, expiry: 300})
  replayState.current = {date: replay?.date === selectedDate ? selectedDate : '', time, expiry: mapConfig?.flight.aircraftExpirySeconds ?? 300}
  const load = async (date: string, cursor?: number) => {
    if (!date) return
    replayRequest.current?.abort(); const controller = new AbortController(); replayRequest.current = controller
    setLoading(true); setError(null); if (cursor === undefined) { setPlaying(false); setSelected(null); setSelectedDate(date) }
    try {
      const params = new URLSearchParams({date}); if (cursor !== undefined) params.set('from', String(Math.max(0, Math.floor(cursor - 120000))))
      const response = await fetch(`/api/v1/flights/history/window?${params}`, {signal: controller.signal})
      if (!response.ok || !response.body) throw new Error(`Replay request failed (${response.status})`)
      const reader = response.body.getReader(), decoder = new TextDecoder(); let buffer = '', complete = false, pending: Replay['events'] = [], result: Replay | null = null
      while (true) {
        const {value, done} = await reader.read(); if (done) break
        buffer += decoder.decode(value, {stream: true}); let newline: number
        while ((newline = buffer.indexOf('\n')) >= 0) {
          const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1); if (!line) continue
          const message = JSON.parse(line)
          if (message.type === 'header') result = {...message.replay, events: []}
          else if (message.type === 'chunk') pending.push(...message.events)
          else if (message.type === 'end') complete = true
          else if (message.type === 'error') throw new Error(message.error)
        }
      }
      if (!complete || !result) throw new Error('Replay stream ended unexpectedly')
      if (controller.signal.aborted) return
      result.events = pending; setReplay(result); setTime(cursor === undefined ? result.startMilliseconds : Math.max(result.startMilliseconds, Math.min(cursor, result.endMilliseconds)))
    } catch (e) { if (!controller.signal.aborted) { setPlaying(false); setError(e instanceof Error ? e.message : 'Could not load replay') } } finally { if (replayRequest.current === controller) { replayRequest.current = null; setLoading(false) } }
  }
  useEffect(() => { const controller = new AbortController(); fetch('/api/v1/flights/recordings', {signal: controller.signal}).then(r => r.ok ? r.json() : Promise.reject(new Error(`Recordings request failed (${r.status})`))).then((v: { dates: string[] }) => { setDates(v.dates); if (v.dates[0]) void load(v.dates[0]) }).catch(e => { if (!controller.signal.aborted) setError(e.message) }); return () => { controller.abort(); replayRequest.current?.abort() } }, [])
  useEffect(() => { const controller = new AbortController(); fetch('/api/v1/settings', {signal: controller.signal}).then(r => r.ok ? r.json() : Promise.reject()).then(setMapConfig).catch(() => {}); return () => controller.abort() }, [])
  useEffect(() => { const update = (event: Event) => { const config = (event as CustomEvent<AppConfig>).detail, current = replayState.current; setMapConfig(config); if (current.date && current.expiry !== (config.flight.aircraftExpirySeconds ?? 300)) void load(current.date, current.time) }; window.addEventListener('trackflights-settings-changed', update); return () => window.removeEventListener('trackflights-settings-changed', update) }, [])
  useEffect(() => { if (!active || !playing || !replay || loading) return; let previous = performance.now(); const id = setInterval(() => { const now = performance.now(), elapsed = now - previous; previous = now; setTime(current => Math.max(current, Math.min(replay.endMilliseconds, replay.windowEndMilliseconds, current + elapsed * speed))) }, 100); return () => clearInterval(id) }, [active, playing, replay, speed, loading])
  useEffect(() => { if (active && playing && replay && !loading && replay.windowEndMilliseconds < replay.endMilliseconds && time >= replay.windowEndMilliseconds - 30000) void load(replay.date, time) }, [active, playing, replay, loading, time])
  useEffect(() => { if (playing && replay && time >= replay.endMilliseconds) setPlaying(false) }, [playing, replay, time])
  const indexed = React.useMemo(() => { const index = new Map<string, Replay['events']>(); for (const event of replay?.date === selectedDate ? replay.events : []) { const entries = index.get(event.aircraft.icao) ?? []; entries.push(event); index.set(event.aircraft.icao, entries) }; return index }, [replay, selectedDate])
  const latest = new Map<string, { milliseconds: number; aircraft: ReplayAircraft; index: number }>()
  for (const [icao, events] of indexed) { let low = 0, high = events.length; while (low < high) { const mid = (low + high) >>> 1; if (events[mid].milliseconds <= time) low = mid + 1; else high = mid }; if (low > 0 && replay && time - events[low - 1].milliseconds <= replay.aircraftExpirySeconds * 1000) latest.set(icao, {...events[low - 1], index: low - 1}) }
  const basicAircraft = [...latest.values()].map(event => event.aircraft).sort((a, b) => (b.altitudeFeet ?? 0) - (a.altitudeFeet ?? 0))
  const metadata = useAircraftMetadata(basicAircraft)
  const aircraft = basicAircraft.map(item => ({ ...item, registration: item.registration || metadata.get(item.icao)?.registration, departureAirport: item.departureAirport || metadata.get(item.icao)?.departureAirport, destinationAirport: item.destinationAirport || metadata.get(item.icao)?.destinationAirport }))
  const {tracks: previousTracks, error: trackError} = useReplayTracks(replay?.date === selectedDate ? selectedDate : '', replay?.date === selectedDate ? replay.windowStartMilliseconds : 0, aircraft.map(a => a.icao), replay?.aircraftExpirySeconds ?? 300)
  const trails = new Map<string, { latitude: number; longitude: number; altitudeFeet?: number; timestamp?: string }[]>()
  for (const [icao, last] of latest) {
    const events = indexed.get(icao)!, points = [...(previousTracks.get(icao) ?? [])].filter(point => Date.parse(point.timestamp ?? '') <= time)
    const seen = new Set(points.map(point => `${point.timestamp}:${point.latitude}:${point.longitude}`))
    for (let i = 0; i <= last.index; i++) {
      const event = events[i], a = event.aircraft
      if (a.latitude === undefined || a.longitude === undefined) continue
      const timestamp = a.positionTimestamp && Date.parse(a.positionTimestamp) > 0 ? a.positionTimestamp : new Date(event.milliseconds).toISOString()
      if (Date.parse(timestamp) > time) continue
      const key = `${timestamp}:${a.latitude}:${a.longitude}`
      if (!seen.has(key)) { points.push({latitude: a.latitude, longitude: a.longitude, altitudeFeet: a.altitudeFeet, timestamp}); seen.add(key) }
    }
    points.sort((a, b) => Date.parse(a.timestamp ?? '') - Date.parse(b.timestamp ?? ''))
    trails.set(icao, points)
  }
  const { traces: onlineTraces, status: traceStatus } = useFlightTraces(trails, time, false, (mapConfig?.flight.historyLookbackMinutes ?? 5) > 0, `${selectedDate}:${replay?.date === selectedDate ? replay.windowStartMilliseconds : ''}:${replay?.aircraftExpirySeconds ?? 300}`)
  const selectedAircraft = aircraft.find(a => a.icao === selected)
  return <section className="replay-workspace"><div className="replay-controls"><label>Recorded day<select value={selectedDate} onChange={e => void load(e.target.value)}><option value="">Select recorded day</option>{dates.map(d => <option key={d}>{d}</option>)}</select></label><button disabled={!replay || loading || replay.startMilliseconds === 0} onClick={() => { if (!playing && replay && time >= replay.endMilliseconds) { void load(replay.date, replay.startMilliseconds) }; setPlaying(!playing) }}>{playing ? 'Pause' : 'Play'}</button><label>Speed (×)<input type="number" min="0.1" max="1000" step="0.1" value={speed} onChange={e => setSpeed(Math.min(1000, Math.max(0.1, Number(e.target.value) || 0.1)))} /></label><AircraftVisibilityControl config={mapConfig}/><span>{loading ? `Buffering replay · ${aircraft.length} active aircraft` : `${aircraft.length} active aircraft`}</span></div>{!replay ? <p className={error ? 'error' : 'workspace'}>{error || (loading ? 'Loading recorded flights…' : 'No recorded flight days were found.')}</p> : <>{error && <p className="error">{error}</p>}<div className="replay-timeline"><input className="timeline" disabled={loading && replay.date !== selectedDate} type="range" min={replay.startMilliseconds} max={replay.endMilliseconds} value={time} onChange={e => { setPlaying(false); const target = Number(e.target.value); if (target >= replay.windowStartMilliseconds && target <= replay.windowEndMilliseconds) { replayRequest.current?.abort(); replayRequest.current = null; setLoading(false); setTime(target) } else void load(replay.date, target) }}/><output>{new Date(time).toLocaleString()}</output><small>{replay.skippedLines ? `${replay.skippedLines} corrupt lines skipped · ` : ''}{trackError ? `${trackError} · ` : ''}{traceStatus || 'Waiting for positioned aircraft'}</small></div><div className="replay-layout"><FlightGlobe aircraft={aircraft} trails={trails} externalTrails={new Map([...onlineTraces].map(([icao, points]) => [icao, points.filter(point => Date.parse(point.timestamp) <= time)]))} receiver={mapConfig && typeof mapConfig.flight.latitude === "number" && typeof mapConfig.flight.longitude === "number" && (mapConfig.flight.latitude !== 0 || mapConfig.flight.longitude !== 0) ? {latitude: mapConfig.flight.latitude, longitude: mapConfig.flight.longitude, name: mapConfig.flight.homeName} : undefined} tileURL={mapConfig?.flight.tileUrl} terrainURL={mapConfig?.flight.terrainUrl} showAirports={mapConfig?.flight.showAirports} selected={selected} onSelect={setSelected} onDistances={setDistances} title="Historical 3D flight replay"/><aside className="replay-aircraft"><h2>Replay aircraft <span>{aircraft.length}</span></h2>{aircraft.map(a => <button key={a.icao} className={selected === a.icao ? 'selected' : ''} onClick={() => setSelected(a.icao)}><strong>{a.callsign || a.registration || a.icao}</strong><small>{a.latitude === undefined ? 'Position unavailable' : `${a.altitudeFeet?.toLocaleString() ?? '—'} ft · ${Math.round(a.speedKnots ?? 0)} kt`}</small></button>) || <p className="muted">No aircraft at this time.</p>}</aside></div>{selectedAircraft && <AircraftDetails aircraft={selectedAircraft} distances={distances} traceStatus={traceStatus} onClose={() => setSelected(null)}/>}</>}</section>
}

function FlightMapView() {
  const [distances, setDistances] = useState<AircraftDistances | null>(null)
  const [state, setState] = useState<LiveState | null>(null), [connected, setConnected] = useState(false), [selected, setSelected] = useState<string | null>(null), [config, setConfig] = useState<AppConfig | null>(null), [saving, setSaving] = useState(false), [configMessage, setConfigMessage] = useState('')
  useEffect(() => {
    let live = true
    fetch('/api/v1/flights/live').then(r => r.ok ? r.json() : Promise.reject()).then(v => { if (live) setState(v) }).catch(() => {})
    const stream = new EventSource('/api/v1/flights/stream')
    stream.addEventListener('flight.snapshot', event => { if (live) { setState(JSON.parse(event.data).payload); setConnected(true) } })
    stream.onerror = () => { if (live) setConnected(false) }
    return () => { live = false; stream.close() }
  }, [])
  useEffect(() => { fetch('/api/v1/settings').then(r => r.ok ? r.json() : Promise.reject()).then(setConfig).catch(() => setConfigMessage('Map configuration unavailable')) }, [])
  useEffect(() => { const update = (event: Event) => { const saved = (event as CustomEvent<AppConfig>).detail; setConfig(current => current ? {...current, flight: {...current.flight, aircraftExpirySeconds: saved.flight.aircraftExpirySeconds}} : saved) }; window.addEventListener('trackflights-settings-changed', update); return () => window.removeEventListener('trackflights-settings-changed', update) }, [])
  const saveMapConfig = async () => { if (!config) return; setSaving(true); setConfigMessage(''); try { const response = await fetch('/api/v1/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(config) }); if (!response.ok) throw new Error(); const saved: AppConfig = await response.json(); setConfig(saved); window.dispatchEvent(new CustomEvent('trackflights-settings-changed', {detail: saved})); setConfigMessage('Home and map settings saved.') } catch { setConfigMessage('Could not save map configuration; check the entered values.') } finally { setSaving(false) } }
  const updateFlightSetting = (key: string, value: string | boolean | number) => setConfig(current => current ? { ...current, flight: { ...current.flight, [key]: value } } : current)
  const metadata = useAircraftMetadata(state?.aircraft ?? [])
  const selectedItem = state?.aircraft.find(a => a.icao === selected)
  const selectedAircraft = selectedItem ? {...selectedItem, registration: metadata.get(selectedItem.icao)?.registration} : undefined
  const liveSignals = new Map<string, { latitude: number; longitude: number; altitudeFeet?: number; timestamp?: string }[]>()
  if (connected && state?.liveAvailable) for (const item of state.aircraft) if (item.positionReady && item.latitude !== undefined && item.longitude !== undefined && item.positionTimestamp && Date.parse(item.positionTimestamp) > 0) liveSignals.set(item.icao, [{latitude: item.latitude, longitude: item.longitude, altitudeFeet: item.altitudeFeet, timestamp: item.positionTimestamp}])
  const { traces: onlineTraces, status: traceStatus } = useFlightTraces(liveSignals, 0, true, (config?.flight.historyLookbackMinutes ?? 5) > 0)
  return <section className="flight-map-workspace">
    <header className="map-toolbar"><div><span className={connected ? 'connection online' : 'connection'}></span>{connected ? 'Live stream connected' : 'Connecting to live stream…'}</div><div>{state?.liveAvailable ? 'Receiver data active' : 'Historical-only mode · send a frame to begin live data'}</div>{traceStatus && <div>{traceStatus}</div>}<AircraftVisibilityControl config={config}/></header>
    <div className="map-layout"><FlightGlobe liveAlerts={connected && !!state?.liveAvailable} externalTrails={onlineTraces} aircraft={(state?.aircraft ?? []).filter(a => a.positionReady)} receiver={state?.receiver ? { ...state.receiver, name: config?.flight.homeName || state.receiver.name } : undefined} tileURL={config?.flight.tileUrl} terrainURL={config?.flight.terrainUrl} showAirports={config?.flight.showAirports} selected={selected} onSelect={setSelected} onDistances={setDistances} title="Live 3D flight map"/><aside className="aircraft-panel"><div className="panel-heading"><h2>Aircraft</h2><span>{state?.aircraft.length ?? 0}</span></div><div className="aircraft-rows">{(state?.aircraft ?? []).slice().sort((a,b) => (b.altitudeFeet ?? 0) - (a.altitudeFeet ?? 0)).map(a => <button key={a.icao} className={selected === a.icao ? 'selected' : ''} onClick={() => setSelected(a.icao)}><strong>{a.callsign || a.icao}</strong><small>{a.positionReady ? `${a.altitudeFeet?.toLocaleString() ?? '—'} ft · ${Math.round(a.speedKnots ?? 0)} kt` : 'Awaiting position decode'}</small></button>) || <p className="muted">No ADS-B frames received.</p>}</div></aside></div>
    {config && <details className="map-settings"><summary>Map configuration</summary><div><label>Home name<input value={config.flight.homeName ?? ''} onChange={e => updateFlightSetting('homeName', e.target.value)}/></label><label>Home latitude<input type="number" min="-90" max="90" step="any" value={config.flight.latitude ?? 0} onChange={e => updateFlightSetting("latitude", Number(e.target.value))}/></label><label>Home longitude<input type="number" min="-180" max="180" step="any" value={config.flight.longitude ?? 0} onChange={e => updateFlightSetting("longitude", Number(e.target.value))}/></label><label>Internet history service URL<input value={config.flight.historyBaseUrl ?? "https://adsb.lol"} onChange={e => updateFlightSetting("historyBaseUrl", e.target.value)}/></label><label>Internet history lookback (minutes; 0 disables)<input type="number" min="0" max="1440" step="1" value={config.flight.historyLookbackMinutes ?? 5} onChange={e => updateFlightSetting("historyLookbackMinutes", Number(e.target.value))}/></label><label>Tile template<input value={config.flight.tileUrl ?? ''} onChange={e => updateFlightSetting('tileUrl', e.target.value)} placeholder="https://…/{z}/{x}/{y}.png"/></label><label>Terrain endpoint<input value={config.flight.terrainUrl ?? ''} onChange={e => updateFlightSetting('terrainUrl', e.target.value)}/></label><label className="checkbox"><input type="checkbox" checked={config.flight.showAirports ?? true} onChange={e => updateFlightSetting('showAirports', e.target.checked)}/> Show Arlanda and Bromma markers</label><label className="checkbox"><input type="checkbox" checked={config.flight.hostBellEnabled === true} onChange={e => updateFlightSetting("hostBellEnabled", e.target.checked)}/> Enable host sight bell</label><button onClick={() => void saveMapConfig()} disabled={saving}>{saving ? 'Saving…' : 'Save map configuration'}</button><small>{configMessage}</small></div></details>}
    {selectedAircraft && <AircraftDetails aircraft={selectedAircraft} distances={distances} traceStatus={traceStatus} onClose={() => setSelected(null)}/>}
  </section>
}

function App() {
  const [active, setActive] = useState('Overview')
  const [replayVisited, setReplayVisited] = useState(false)
  const [status, setStatus] = useState<Status | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => { fetch('/api/v1/status').then(r => r.ok ? r.json() : Promise.reject(new Error(String(r.status)))).then(setStatus).catch(e => setError(e.message)) }, [])
  return <div className="shell"><aside><strong>◉ TrackFlights</strong><nav>{sections.map(section => <button className={active === section ? 'active' : ''} onClick={() => { setActive(section); if (section === 'Flight replay') setReplayVisited(true) }} key={section}>{section}</button>)}</nav><small>{status ? `${status.accessMode} · ${status.services.tracking}` : 'Checking system…'}</small></aside><main><p className="eyebrow">OBSERVATORY CONSOLE</p><h1>{active}</h1>{error ? <p className="error">Unable to load system status: {error}</p> : active === 'Overview' ? <div className="cards">{Object.entries(status?.services ?? {}).map(([name, state]) => <article key={name}><h2>{name}</h2><p>{state}</p></article>)}</div> : active === 'Flight map' ? <FlightMapView/> : active === 'Flight replay' ? null : <section className="workspace">This workspace is ready for the {active.toLowerCase()} service migration.</section>}{replayVisited && <div hidden={active !== 'Flight replay'}><ReplayView active={active === 'Flight replay'}/></div>}</main></div>
}
function useState<T>(value: T) { return React.useState<T>(value) }
function useEffect(effect: React.EffectCallback, dependencies: React.DependencyList) { return React.useEffect(effect, dependencies) }
import React from 'react'
createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
