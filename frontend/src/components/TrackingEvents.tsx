import { useCallback, useEffect, useState } from 'react'
import './objectTracking.css'
import { TrackedObjects } from './TrackedObjects'

type TrackingEvent = {id: string; summary: unknown}

export function TrackingEvents() {
  const [events, setEvents] = useState<TrackingEvent[]>([])
  const [selectedID, setSelectedID] = useState('')
  const [files, setFiles] = useState<string[]>([])
  const [originalAvailable, setOriginalAvailable] = useState(false)
  const [error, setError] = useState('')
  const [filesError, setFilesError] = useState('')
  const [loading, setLoading] = useState(true)
  const selected = events.find(event => event.id === selectedID)

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      const response = await fetch('/api/v1/tracking/events', {signal, cache: 'no-store'})
      if (!response.ok) throw new Error('Could not list recorded events')
      const data: {items: TrackingEvent[]} = await response.json()
      if (!signal?.aborted) { setEvents(data.items); setError(''); setLoading(false) }
    } catch (e) {
      if (!signal?.aborted) { setError(e instanceof Error ? e.message : 'Could not list recorded events'); setLoading(false) }
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void refresh(controller.signal)
    const timer = window.setInterval(() => void refresh(controller.signal), 5000)
    return () => { controller.abort(); window.clearInterval(timer) }
  }, [refresh])

  useEffect(() => {
    setFiles([])
    setOriginalAvailable(false)
    setFilesError('')
    if (!selectedID) return
    const controller = new AbortController()
    fetch(`/api/v1/tracking/events/${encodeURIComponent(selectedID)}/files`, {signal: controller.signal})
      .then(r => r.ok ? r.json() : Promise.reject()).then((data: {items: string[]; originalAvailable?: boolean}) => { setFiles(data.items); setOriginalAvailable(data.originalAvailable === true) })
      .catch(() => { if (!controller.signal.aborted) setFilesError('Could not load event files') })
    return () => controller.abort()
  }, [selectedID])

  const fileURL = (path: string) => `/api/v1/tracking/events/${encodeURIComponent(selectedID)}/files/${path.split('/').map(encodeURIComponent).join('/')}`
  const crop = files.find(file => /\.(jpg|png)$/i.test(file))
  const video = files.find(file => file === 'tracked.mp4') ?? files.find(file => file === 'tracked.avi')
  const trackedURL = video === 'tracked.avi' ? `/api/v1/tracking/events/${encodeURIComponent(selectedID)}/video` : video ? fileURL(video) : undefined
  const originalURL = originalAvailable ? `/api/v1/tracking/events/${encodeURIComponent(selectedID)}/original-video` : undefined
  return <section className="tracking-results">
    <h2>Recorded events</h2>
    <button onClick={() => void refresh()}>Refresh events</button>
    {error && <p className="error" role="alert">{error}</p>}
    {loading ? <p className="muted">Loading events…</p> : events.length === 0 ? <p className="muted">No recorded events yet. Start a tracking session to record events.</p> : <div className="tracking-event-list">{events.map(event => <button key={event.id} className={selectedID === event.id ? 'selected' : ''} onClick={() => setSelectedID(event.id)}>{event.id}</button>)}</div>}
    {selected && <section><h3>{selected.id}</h3>{video && <p><a href={fileURL(video)} download={video}>Download tracked video ({video === 'tracked.avi' ? 'AVI' : 'MP4'})</a></p>}{crop && <img className="tracking-crop" src={fileURL(crop)} alt="Tracked object crop"/>}<TrackedObjects eventID={selectedID} files={files} fileURL={fileURL} trackedURL={trackedURL} originalURL={originalURL} trackedNeedsConversion={video === 'tracked.avi'}/><details><summary>Event details</summary><pre>{JSON.stringify(selected.summary, null, 2)}</pre></details><h4>Files</h4>{filesError && <p className="error" role="alert">{filesError}</p>}<ul>{files.slice(0, 100).map(file => <li key={file}><a href={fileURL(file)} target="_blank" rel="noreferrer">{file}</a></li>)}</ul>{files.length > 100 && <p className="muted">Showing the first 100 files.</p>}</section>}
  </section>
}
