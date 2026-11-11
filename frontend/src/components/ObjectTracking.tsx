import { useCallback, useEffect, useState } from 'react'
import { TrackingEvents } from './TrackingEvents'
import './objectTracking.css'

type Job = { id?: string; state?: string; progress?: number; message?: string; error?: string }
type Status = { workerAvailable: boolean; job: Job; hasPreview: boolean; frame?: { frame?: number; detections?: number; tracks?: number; recording?: boolean; fixedLength?: boolean } }
type Input = {id: string; name: string}

export function ObjectTracking() {
  const [status, setStatus] = useState<Status | null>(null)
  const [inputs, setInputs] = useState<Input[]>([])
  const [input, setInput] = useState('sample')
  const [mode, setMode] = useState<'file' | 'live'>('file')
  const [url, setURL] = useState('rtsp://192.168.88.1/ch0/stream0')
  const [profile, setProfile] = useState('general')
  const [original, setOriginal] = useState(false), [mask, setMask] = useState(false), [fullVideo, setFullVideo] = useState(true)
  const [minArea, setMinArea] = useState(''), [slowSpeed, setSlowSpeed] = useState(''), [fastSpeed, setFastSpeed] = useState(''), [threshold, setThreshold] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [previewVersion, setPreviewVersion] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/v1/settings', {signal: controller.signal}).then(r => r.ok ? r.json() : Promise.reject()).then((settings: {dwarf?: {host?: string}}) => {
      const host = settings.dwarf?.host?.trim()
      if (host && /^[a-zA-Z0-9.:-]+$/.test(host)) setURL(current => current === 'rtsp://192.168.88.1/ch0/stream0' ? `rtsp://${host}/ch0/stream0` : current)
    }).catch(() => {})
    return () => controller.abort()
  }, [])

  const refreshInputs = useCallback(async () => {
    const response = await fetch('/api/v1/tracking/inputs')
    if (!response.ok) throw new Error('Could not list videos')
    const data: {items: Input[]} = await response.json()
    setInputs(data.items)
    setInput(current => data.items.some(item => item.id === current) ? current : data.items[0]?.id ?? '')
  }, [])
  useEffect(() => {
    let alive = true
    const refresh = async () => {
      try {
        const response = await fetch('/api/v1/tracking', {cache: 'no-store'})
        if (!response.ok) throw new Error('Tracking service unavailable')
        const next: Status = await response.json()
        if (!alive) return
        setStatus(next)
        if (next.hasPreview) setPreviewVersion(v => v + 1)
      } catch { if (alive) setError('Tracking service unavailable') }
    }
    void refresh()
    void refreshInputs().catch(e => { if (alive) setError(e.message) })
    const timer = window.setInterval(() => void refresh(), 1000)
    return () => { alive = false; clearInterval(timer) }
  }, [refreshInputs])
  const active = status?.job.state === 'running' || status?.job.state === 'queued'
  const request = async (path: string, options: RequestInit) => {
    setBusy(true); setError('')
    try {
      const response = await fetch(path, options)
      if (!response.ok) throw new Error((await response.text()).trim() || 'Request failed')
      return response
    } catch (e) { setError(e instanceof Error ? e.message : 'Request failed'); return null } finally { setBusy(false) }
  }
  const start = async () => {
    const response = await request('/api/v1/tracking/start', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({mode, input: mode === 'file' ? input : url, profile, original, mask, fullVideo: mode === 'file' && fullVideo, ...(minArea !== '' && {minArea: Number(minArea)}), ...(slowSpeed !== '' && {slowSpeed: Number(slowSpeed)}), ...(fastSpeed !== '' && {fastSpeed: Number(fastSpeed)}), ...(threshold !== '' && {threshold: Number(threshold)})})})
    if (response) { const job: Job = await response.json(); setStatus(current => current ? {...current, job, hasPreview: false, frame: undefined} : current) }
  }
  const upload = async (file?: File) => {
    if (!file) return
    const body = new FormData(); body.append('video', file)
    const response = await request('/api/v1/tracking/inputs', {method: 'POST', body})
    if (response) { const result: Input = await response.json(); await refreshInputs(); setInput(result.id); setMode('file') }
  }
  return <section className="tracking-workspace">
    <div className="tracking-controls">
      <label>Source<select value={mode} disabled={active} onChange={e => setMode(e.target.value as 'file' | 'live')}><option value="file">Recorded video</option><option value="live">Live RTSP stream</option></select></label>
      {mode === 'file' ? <><label>Video<select value={input} disabled={active || inputs.length === 0} onChange={e => setInput(e.target.value)}>{inputs.length === 0 && <option value="">No videos available</option>}{inputs.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><label>Upload video<input type="file" accept=".mp4,.avi,.mov,.mkv" disabled={active || busy} onChange={e => { void upload(e.target.files?.[0]); e.target.value = '' }}/></label></> : <label>RTSP URL<input value={url} disabled={active} onChange={e => setURL(e.target.value)} /></label>}
      <label>Tracking profile<select value={profile} disabled={active} onChange={e => setProfile(e.target.value)}><option value="general">General / aircraft</option><option value="rolling_ball">Rolling or thrown ball</option></select></label>
      {mode === 'file' && <label className="tracking-checkbox"><input type="checkbox" checked={fullVideo} disabled={active} onChange={e => setFullVideo(e.target.checked)}/> Save full tracked video (MP4)</label>}<label className="tracking-checkbox"><input type="checkbox" checked={original} disabled={active} onChange={e => setOriginal(e.target.checked)}/> Save original video</label><label className="tracking-checkbox"><input type="checkbox" checked={mask} disabled={active} onChange={e => setMask(e.target.checked)}/> Save motion mask</label>
      {!active ? <button onClick={() => void start()} disabled={busy || !status?.workerAvailable || (mode === 'file' ? !input : !url)}>Start tracking</button> : <button onClick={() => void request(`/api/v1/jobs/${encodeURIComponent(status?.job.id ?? '')}/cancel`, {method: 'POST'})} disabled={busy}>Stop tracking</button>}
    </div>
    <details className="tracking-tuning"><summary>Detection settings</summary><div><label>Minimum area (px²)<input type="number" min="0" step="any" placeholder="Profile default" value={minArea} disabled={active} onChange={e => setMinArea(e.target.value)}/></label><label>Slow speed (px/s)<input type="number" min="0" step="any" placeholder="Profile default" value={slowSpeed} disabled={active} onChange={e => setSlowSpeed(e.target.value)}/></label><label>Fast speed (px/s)<input type="number" min="0" step="any" placeholder="Profile default" value={fastSpeed} disabled={active} onChange={e => setFastSpeed(e.target.value)}/></label><label>Foreground threshold (0–255)<input type="number" min="0" max="255" step="any" placeholder="Profile default" value={threshold} disabled={active} onChange={e => setThreshold(e.target.value)}/></label></div><small>Leave a field empty to use the selected profile’s default.</small></details>
    {status && !status.workerAvailable && <p className="error">Tracking worker unavailable. Install the OpenCV prerequisites and run <code>make build-tracking</code>.</p>}
    {error && <p className="error" role="alert">{error}</p>}
    {status?.job.error && <p className="error" role="alert">{status.job.error}</p>}
    <div className="tracking-layout"><div className="tracking-preview">{status?.hasPreview ? <img src={`/api/v1/tracking/preview?v=${previewVersion}`} alt="Live object tracking preview" /> : <p className="muted">Choose a video or live stream to begin tracking.</p>}</div><aside className="tracking-info"><h2>Tracking</h2><p>{status?.job.state || 'Idle'}</p>{status?.frame?.fixedLength && <progress max={1} value={status.job.progress ?? 0} />}<p>{status?.job.message || 'Waiting for a tracking session'}</p><p>Frame {status?.frame?.frame ?? '—'} · {status?.frame?.detections ?? 0} detections · {status?.frame?.tracks ?? 0} tracks</p>{active && status?.frame?.recording && <strong>Recording event</strong>}</aside></div>
    <TrackingEvents/>
  </section>
}
