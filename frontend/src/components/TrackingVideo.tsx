import { forwardRef, useEffect, useId, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { selectedTrackAtTime, visibleTrackBox, pathAtTime } from './videoOverlay.mjs'
import type { VideoFrame } from './videoOverlay.mjs'
import { createSynchronizedPlayback } from './synchronizedPlayback.mjs'
import type { SynchronizedPlayback } from './synchronizedPlayback.mjs'

type Props = {trackedURL?: string; originalURL?: string; trackedNeedsConversion: boolean; frames: VideoFrame[]; selected: number | null; name: string; sourceWidth: number; sourceHeight: number; objectOptions: {id: number; name: string}[]; onSelectObject: (id: number | null) => void; onTimeChange: (time: number) => void; onPlayingChange: (playing: boolean) => void; onDurationChange: (duration: number) => void; onReadyChange: (ready: boolean) => void}
export type TrackingVideoHandle = {play: () => void; pause: () => void; seek: (time: number) => void}
type OverlaySettings = {overlayBoxScale: number; overlayBoxThickness: number; overlayPathThickness: number}
type AppSettings = {tracking: Partial<OverlaySettings> & Record<string, unknown>; [key: string]: unknown}
const overlayDefaults: OverlaySettings = {overlayBoxScale: 4, overlayBoxThickness: 1.5, overlayPathThickness: 1}
const draftFrom = (settings: OverlaySettings) => ({overlayBoxScale: String(settings.overlayBoxScale), overlayBoxThickness: String(settings.overlayBoxThickness), overlayPathThickness: String(settings.overlayPathThickness)})

export const TrackingVideo = forwardRef<TrackingVideoHandle, Props>(function TrackingVideo({trackedURL, originalURL, trackedNeedsConversion, frames, selected, name, sourceWidth, sourceHeight, objectOptions, onSelectObject, onTimeChange, onPlayingChange, onDurationChange, onReadyChange}, ref) {
  const tracked = useRef<HTMLVideoElement>(null)
  const original = useRef<HTMLVideoElement>(null)
  const controller = useRef<SynchronizedPlayback | null>(null)
  useImperativeHandle(ref, () => ({play: () => controller.current?.play(), pause: () => controller.current?.pause(), seek: time => controller.current?.seek(time)}), [])
  const [time, setTime] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [buffering, setBuffering] = useState(false)
  const [error, setError] = useState('')
  const [trackedInfo, setTrackedInfo] = useState({loaded: false, failed: false, duration: 0})
  const [originalInfo, setOriginalInfo] = useState({loaded: false, failed: false, duration: 0})
  const [dimensions, setDimensions] = useState({width: 0, height: 0})
  const [showOverlay, setShowOverlay] = useState(false)
  const maskID = `tracking-path-mask-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`
  const [overlaySettings, setOverlaySettings] = useState(overlayDefaults)
  const [overlayDraft, setOverlayDraft] = useState(() => draftFrom(overlayDefaults))
  const [settingsMessage, setSettingsMessage] = useState('')
  const [savingSettings, setSavingSettings] = useState(false)

  useEffect(() => {
    const abort = new AbortController()
    fetch('/api/v1/settings', {signal: abort.signal}).then(r => r.ok ? r.json() : Promise.reject()).then((config: AppSettings) => {
      const values = {...overlayDefaults, ...Object.fromEntries(Object.entries(config.tracking ?? {}).filter(([key, value]) => key in overlayDefaults && typeof value === 'number'))} as OverlaySettings
      setOverlaySettings(values); setOverlayDraft(draftFrom(values))
    }).catch(() => { if (!abort.signal.aborted) setSettingsMessage('Could not load overlay settings') })
    return () => abort.abort()
  }, [])

  const nextOverlaySettings: OverlaySettings = {overlayBoxScale: Number(overlayDraft.overlayBoxScale), overlayBoxThickness: Number(overlayDraft.overlayBoxThickness), overlayPathThickness: Number(overlayDraft.overlayPathThickness)}
  const validOverlaySettings = Object.values(overlayDraft).every(value => value.trim() !== '') && Object.values(nextOverlaySettings).every(Number.isFinite) && nextOverlaySettings.overlayBoxScale >= 1 && nextOverlaySettings.overlayBoxScale <= 20 && nextOverlaySettings.overlayBoxThickness >= 0.25 && nextOverlaySettings.overlayBoxThickness <= 12 && nextOverlaySettings.overlayPathThickness >= 0.25 && nextOverlaySettings.overlayPathThickness <= 12
  const saveOverlaySettings = async () => {
    if (!validOverlaySettings) { setSettingsMessage('Enter a box scale from 1 to 20 and line thicknesses from 0.25 to 12 pixels.'); return }
    setSavingSettings(true); setSettingsMessage('')
    try {
      const latestResponse = await fetch('/api/v1/settings')
      if (!latestResponse.ok) throw new Error('Could not load settings')
      const latest: AppSettings = await latestResponse.json()
      const response = await fetch('/api/v1/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({...latest, tracking: {...latest.tracking, ...nextOverlaySettings}})})
      if (!response.ok) throw new Error('Could not save overlay settings')
      setOverlaySettings(nextOverlaySettings); setSettingsMessage('Overlay settings saved')
    } catch (e) { setSettingsMessage(e instanceof Error ? e.message : 'Could not save overlay settings') } finally { setSavingSettings(false) }
  }

  useEffect(() => {
    setTime(0); setPlaying(false); setBuffering(false); setError('')
    setTrackedInfo({loaded: false, failed: false, duration: 0})
    setOriginalInfo({loaded: false, failed: false, duration: 0})
    setDimensions({width: 0, height: 0})
  }, [trackedURL, originalURL])

  useEffect(() => {
    const videos = [trackedURL && !trackedInfo.failed ? tracked.current : null, originalURL && !originalInfo.failed ? original.current : null].filter((video): video is HTMLVideoElement => video !== null)
    setPlaying(false); setBuffering(false); onPlayingChange(false)
    if (!videos.length) return
    const player = createSynchronizedPlayback(videos, {
      onState: state => { setPlaying(state.wanted); setBuffering(state.buffering); onPlayingChange(state.wanted) },
      onTime: time => { setTime(time); onTimeChange(time) },
      onError: () => setError('Playback could not start. Please try again.'),
    })
    controller.current = player
    let request = 0
    const update = () => { player.update(); request = requestAnimationFrame(update) }
    request = requestAnimationFrame(update)
    return () => { cancelAnimationFrame(request); player.dispose(); if (controller.current === player) controller.current = null }
  }, [trackedURL, originalURL, trackedInfo.failed, originalInfo.failed, onPlayingChange, onTimeChange])

  const points = useMemo(() => {
    if (selected === null) return []
    return frames.flatMap(frame => {
      const track = frame.tracks?.find(item => item.id === selected && !item.missed_frames)
      return track ? [{x: track.x, y: track.y, time: frame.time_ms}] : []
    })
  }, [frames, selected])
  const track = selectedTrackAtTime(frames, selected, time)
  const path = pathAtTime(points, time)
  const width = dimensions.width || sourceWidth, height = dimensions.height || sourceHeight
  const box = track && width > 0 && height > 0 ? visibleTrackBox(track, width, height, overlaySettings.overlayBoxScale) : null
  const labelSize = Math.max(width, height) / 45
  const labelGap = Math.max(width, height) / 100
  const labelY = box ? (box.y >= labelSize + labelGap ? box.y - labelGap : Math.min(height - labelGap, box.y + box.height + labelSize + labelGap)) : 0
  const label = name ? `${name} (#${selected})` : `#${selected}`
  const available = [trackedURL && !trackedInfo.failed ? trackedInfo : null, originalURL && !originalInfo.failed ? originalInfo : null].filter((info): info is typeof trackedInfo => info !== null)
  const ready = available.length > 0 && available.every(info => info.loaded)
  const durations = available.map(info => info.duration).filter(duration => Number.isFinite(duration) && duration > 0)
  const duration = durations.length ? Math.min(...durations) : 0
  useEffect(() => { onDurationChange(duration); onReadyChange(ready) }, [duration, ready, onDurationChange, onReadyChange])
  const mismatch = durations.length === 2 && Math.abs(durations[0] - durations[1]) > 0.15
  const formatTime = (seconds: number) => { const n = Math.max(0, Math.floor(seconds)); return `${Math.floor(n / 60)}:${String(n % 60).padStart(2, '0')}` }
  return <section className="video-comparison" aria-label="Tracked and original videos">
    <div className="video-overlay-controls">
      <label>Selected object <select aria-label="Selected object for video overlay" value={selected ?? ''} disabled={objectOptions.length === 0} onChange={e => onSelectObject(e.target.value ? Number(e.target.value) : null)}><option value="">Select an object</option>{objectOptions.map(object => <option key={object.id} value={object.id}>#{object.id}{object.name ? ` — ${object.name}` : ''}</option>)}</select></label>
      <label className="original-overlay-option"><input type="checkbox" checked={showOverlay} disabled={!originalURL} onChange={e => setShowOverlay(e.target.checked)}/> Show selected object on original video</label>
      <button type="button" disabled={!ready || points.length === 0} onClick={() => controller.current?.seek(points[0].time / 1000)}>Go to object’s first position</button>
    </div>
    <details className="video-overlay-settings"><summary>Overlay appearance</summary><div>
      <label>Box size multiplier <input type="number" min="1" max="20" step="0.5" value={overlayDraft.overlayBoxScale} onChange={e => setOverlayDraft(current => ({...current, overlayBoxScale: e.target.value}))}/></label>
      <label>Box line thickness (px) <input type="number" min="0.25" max="12" step="0.25" value={overlayDraft.overlayBoxThickness} onChange={e => setOverlayDraft(current => ({...current, overlayBoxThickness: e.target.value}))}/></label>
      <label>Tracking line thickness (px) <input type="number" min="0.25" max="12" step="0.25" value={overlayDraft.overlayPathThickness} onChange={e => setOverlayDraft(current => ({...current, overlayPathThickness: e.target.value}))}/></label>
      <button type="button" disabled={savingSettings || !validOverlaySettings} onClick={() => void saveOverlaySettings()}>{savingSettings ? 'Saving…' : 'Save appearance'}</button>
    </div>{settingsMessage && <p role="status">{settingsMessage}</p>}</details>
    <div className="video-comparison-grid">
      <div><h3>Tracked video</h3>{trackedURL ? <><div className="tracked-video-player"><video ref={tracked} muted playsInline preload="auto" src={trackedURL} onLoadedMetadata={e => setTrackedInfo({loaded: true, failed: false, duration: e.currentTarget.duration})} onError={() => setTrackedInfo({loaded: false, failed: true, duration: 0})}>Your browser cannot play this video.</video></div>{trackedInfo.failed ? <p className="error" role="alert">The tracked video could not be played.</p> : trackedNeedsConversion && !trackedInfo.loaded ? <p className="muted" role="status">Preparing tracked video for playback…</p> : null}</> : <p className="muted">No tracked video is available.</p>}</div>
      <div><h3>Original video</h3>{originalURL ? <><div className="tracked-video-player"><video ref={original} muted playsInline preload="auto" src={originalURL} onLoadedMetadata={e => { setOriginalInfo({loaded: true, failed: false, duration: e.currentTarget.duration}); setDimensions({width: e.currentTarget.videoWidth, height: e.currentTarget.videoHeight}) }} onError={() => setOriginalInfo({loaded: false, failed: true, duration: 0})}>Your browser cannot play this video.</video>{showOverlay && selected !== null && width > 0 && height > 0 && <svg className="tracked-video-overlay" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
        {box && <defs><mask id={maskID} maskUnits="userSpaceOnUse" x="0" y="0" width={width} height={height}><rect x="0" y="0" width={width} height={height} fill="white"/><rect x={box.x} y={box.y} width={box.width} height={box.height} fill="black"/></mask></defs>}
        {path && <polyline points={path} fill="none" stroke="#ffe358" strokeWidth={overlaySettings.overlayPathThickness} vectorEffect="non-scaling-stroke" mask={box ? `url(#${maskID})` : undefined}/>}
        {track && box && <><rect x={box.x} y={box.y} width={box.width} height={box.height} fill="none" stroke="#ff5858" strokeWidth={overlaySettings.overlayBoxThickness} vectorEffect="non-scaling-stroke"/><text x={Math.max(0, Math.min(box.x, width-width/5))} y={labelY} fontSize={labelSize} fill="#fff" stroke="#000" strokeWidth={Math.max(width, height)/400} paintOrder="stroke">{label}{track.missed_frames ? ' (last seen)' : ''}</text></>}
      </svg>}</div>{originalInfo.failed ? <p className="error" role="alert">The original video could not be played. Conversion requires FFmpeg on the server.</p> : !originalInfo.loaded ? <p className="muted" role="status">Preparing original video for playback…</p> : showOverlay && selected === null ? <p className="muted">Select an object above to see its overlay.</p> : showOverlay && !track ? <p className="muted">The selected object has no recorded position at this point in the video.</p> : null}</> : <p className="muted">No original video was saved or could be matched to this event. New tracking sessions save an original event video by default.</p>}</div>
    </div>
    <div className="video-comparison-controls">
      <button type="button" disabled={!ready} onClick={() => { setError(''); if (playing) controller.current?.pause(); else controller.current?.play() }}>{playing ? 'Pause' : 'Play'}</button>
      <button type="button" disabled={!ready} onClick={() => controller.current?.seek(time - 5)}>−5 seconds</button>
      <input aria-label="Video position" type="range" min="0" max={duration || 0} step="0.01" value={Math.min(time, duration || 0)} disabled={!ready || !duration} onChange={e => controller.current?.seek(Number(e.target.value))}/>
      <button type="button" disabled={!ready} onClick={() => controller.current?.seek(time + 5)}>+5 seconds</button>
      <span>{formatTime(time)} / {formatTime(duration)}</span>
      {buffering && <span role="status">Buffering both videos…</span>}
    </div>
    {mismatch && <p className="muted">The recordings have different lengths; playback stops at the end of the shorter video.</p>}
    {error && <p className="error" role="alert">{error}</p>}
  </section>
})
