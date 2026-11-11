import { useEffect, useMemo, useState } from 'react'
import './objectTracking.css'
import { TrackingVideo } from './TrackingVideo'
import type { VideoTrack } from './videoOverlay.mjs'

type Point = {x: number; y: number}
type Track = Point & VideoTrack & {speed_px_s?: number}
type Frame = {source_frame: number; time_ms: number; tracks: Track[]}
type Observation = {frame: number; time: number; track: Track}

export function TrackedObjects({eventID, files, fileURL, trackedURL, originalURL, trackedNeedsConversion}: {eventID: string; files: string[]; fileURL: (path: string) => string; trackedURL?: string; originalURL?: string; trackedNeedsConversion: boolean}) {
  const [frames, setFrames] = useState<Frame[]>([])
  const [cropFiles, setCropFiles] = useState<string[]>([])
  const [names, setNames] = useState<Record<string, string>>({})
  const [selected, setSelected] = useState<number | null>(null)
  const [position, setPosition] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [name, setName] = useState('')
  const [message, setMessage] = useState('')
  const [fps, setFPS] = useState(10)
  const [sourceSize, setSourceSize] = useState({width: 0, height: 0})

  useEffect(() => {
    const controller = new AbortController()
    setFrames([]); setNames({}); setSelected(null); setPosition(0); setPlaying(false); setMessage(''); setSourceSize({width: 0, height: 0})
    fetch(`/api/v1/tracking/events/${encodeURIComponent(eventID)}/names`, {signal: controller.signal})
      .then(r => r.ok ? r.json() : Promise.reject()).then(setNames)
      .catch(() => { if (!controller.signal.aborted) setMessage('Could not load object names') })
    if (files.includes('tracking.json')) {
      fetch(fileURL('tracking.json'), {signal: controller.signal})
        .then(r => r.ok ? r.json() : Promise.reject()).then((data: {frames: Frame[]; fps?: number; width?: number; height?: number}) => { setFrames(data.frames ?? []); setFPS(Math.max(1, Math.min(30, data.fps || 10))); setSourceSize({width: data.width ?? 0, height: data.height ?? 0}) })
        .catch(() => { if (!controller.signal.aborted) setMessage('Could not load tracking data') })
    }
    return () => controller.abort()
  // fileURL is determined by eventID; files arrive separately after event selection.
  }, [eventID, files])

  const objects = useMemo(() => {
    const byID = new Map<number, Observation[]>()
    for (const frame of frames) for (const track of frame.tracks ?? []) {
      if (track.missed_frames) continue
      const entries = byID.get(track.id) ?? []
      entries.push({frame: frame.source_frame, time: frame.time_ms, track})
      byID.set(track.id, entries)
    }
    for (const file of files) {
      const match = /^track_crops\/object_(\d+)\//.exec(file)
      if (match && !byID.has(Number(match[1]))) byID.set(Number(match[1]), [])
    }
    return byID
  }, [frames, files])
  const observations = selected === null ? [] : objects.get(selected) ?? []
  useEffect(() => {
    setCropFiles([])
    if (selected === null) return
    const controller = new AbortController()
    fetch(`/api/v1/tracking/events/${encodeURIComponent(eventID)}/objects/${selected}/crops`, {signal: controller.signal})
      .then(r => r.ok ? r.json() : Promise.reject()).then((data: {items: string[]}) => setCropFiles(data.items))
      .catch(() => { if (!controller.signal.aborted) setMessage('Could not load object close-ups') })
    return () => controller.abort()
  }, [eventID, selected])
  const crops = useMemo(() => selected === null ? [] : cropFiles.flatMap(path => {
    const match = /^track_crops\/object_(\d+)\/frame_(\d+)_\d+ms\.jpe?g$/i.exec(path)
    return match && Number(match[1]) === selected ? [{path, frame: Number(match[2])}] : []
  }).sort((a, b) => a.frame - b.frame), [cropFiles, selected])
  const length = Math.max(observations.length, crops.length)
  const index = Math.min(position, Math.max(0, length - 1))
  const current = observations[Math.min(index, Math.max(0, observations.length - 1))]
  const crop = current && crops.length ? crops.reduce((a, b) => Math.abs(b.frame - current.frame) < Math.abs(a.frame - current.frame) ? b : a) : crops[Math.min(index, crops.length - 1)]
  const points = observations.map(o => o.track)
  const bounds = points.reduce<{minX: number; minY: number; maxX: number; maxY: number} | null>((b, p) => b ? {minX: Math.min(b.minX, p.x), minY: Math.min(b.minY, p.y), maxX: Math.max(b.maxX, p.x), maxY: Math.max(b.maxY, p.y)} : {minX: p.x, minY: p.y, maxX: p.x, maxY: p.y}, null)
  const padding = bounds ? Math.max(10, (bounds.maxX - bounds.minX) * .05, (bounds.maxY - bounds.minY) * .05) : 10

  useEffect(() => { setPosition(0); setPlaying(false) }, [selected, eventID])
  useEffect(() => { setName(selected === null ? '' : names[String(selected)] ?? '') }, [selected, eventID, names])
  useEffect(() => {
    if (!playing || length < 2) return
    const timer = window.setInterval(() => setPosition(i => i + 1 >= length ? 0 : i + 1), 1000 / fps)
    return () => window.clearInterval(timer)
  }, [playing, length, fps])
  const save = async () => {
    if (selected === null) return
    setMessage('')
    try {
      const r = await fetch(`/api/v1/tracking/events/${encodeURIComponent(eventID)}/names/${selected}`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({name})})
      if (!r.ok) throw new Error('Could not save object name')
      setNames(await r.json()); setMessage('Name saved')
    } catch (e) { setMessage(e instanceof Error ? e.message : 'Could not save object name') }
  }
  return <section className="tracked-objects">{(trackedURL || originalURL) && <TrackingVideo key={eventID} trackedURL={trackedURL} originalURL={originalURL} trackedNeedsConversion={trackedNeedsConversion} frames={frames} selected={selected} name={selected === null ? '' : names[String(selected)] ?? ''} sourceWidth={sourceSize.width} sourceHeight={sourceSize.height} objectOptions={[...objects.keys()].sort((a, b) => a - b).map(id => ({id, name: names[String(id)] ?? ''}))} onSelectObject={setSelected}/>}<h3>Tracked objects</h3>
    {objects.size === 0 ? <p className="muted">No object tracking data is available for this event.</p> : <>
      <label>Object <select value={selected ?? ''} onChange={e => setSelected(e.target.value ? Number(e.target.value) : null)}><option value="">Select an object</option>{[...objects.keys()].sort((a, b) => a - b).map(id => <option key={id} value={id}>#{id}{names[String(id)] ? ` — ${names[String(id)]}` : ''}</option>)}</select></label>
      {selected !== null && <><div className="object-name"><label>Name <input maxLength={128} value={name} onChange={e => setName(e.target.value)} /></label><button onClick={() => void save()}>Save name</button></div>
        <div className="object-review"><div><h4>Object close-up</h4>{crop ? <img className="object-closeup" src={fileURL(crop.path)} alt={`Object ${selected} close-up`} /> : <p className="muted">No close-up images were saved for this object.</p>}</div><div><h4>Tracking path</h4>{bounds ? <svg className="object-path" viewBox={`${bounds.minX-padding} ${bounds.minY-padding} ${Math.max(1, bounds.maxX-bounds.minX)+2*padding} ${Math.max(1, bounds.maxY-bounds.minY)+2*padding}`} aria-label={`Path for object ${selected}`}><polyline points={points.map(p => `${p.x},${p.y}`).join(' ')} fill="none" stroke="#f2c75c" strokeWidth={2} vectorEffect="non-scaling-stroke" />{current && <circle cx={current.track.x} cy={current.track.y} r={padding/2} fill="#ff6b5d" />}</svg> : <p className="muted">No path was saved for this object.</p>}</div></div>
        {length > 0 && <div className="object-playback"><button onClick={() => setPlaying(!playing)}>{playing ? 'Pause' : 'Play'}</button><input aria-label="Object playback position" type="range" min="0" max={length-1} value={index} onChange={e => setPosition(Number(e.target.value))}/><span>{current ? `${(current.time / 1000).toFixed(2)} s · frame ${current.frame}` : `${index + 1} / ${length}`}</span></div>}
      </>}
    </>}{message && <p role="status">{message}</p>}
  </section>
}
