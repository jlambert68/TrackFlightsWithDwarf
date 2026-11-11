import { useEffect, useMemo, useRef, useState } from 'react'
import { frameAtTime, pathAtTime } from './videoOverlay.mjs'
import type { VideoFrame } from './videoOverlay.mjs'

export function TrackingVideo({url, frames, selected, name, sourceWidth, sourceHeight, needsConversion}: {url: string; frames: VideoFrame[]; selected: number | null; name: string; sourceWidth: number; sourceHeight: number; needsConversion: boolean}) {
  const video = useRef<HTMLVideoElement>(null)
  const [time, setTime] = useState(0)
  const [dimensions, setDimensions] = useState({width: 0, height: 0})
  const [playing, setPlaying] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!playing) return
    let request = 0
    const update = () => { if (video.current) setTime(video.current.currentTime); request = requestAnimationFrame(update) }
    request = requestAnimationFrame(update)
    return () => cancelAnimationFrame(request)
  }, [playing])
  const points = useMemo(() => {
    if (selected === null) return []
    return frames.flatMap(frame => {
      const track = frame.tracks?.find(item => item.id === selected && !item.missed_frames)
      return track ? [{x: track.x, y: track.y, time: frame.time_ms}] : []
    })
  }, [frames, selected])
  const frame = frameAtTime(frames, time)
  const track = selected === null ? undefined : frame?.tracks?.find(item => item.id === selected)
  const path = pathAtTime(points, time)
  const width = dimensions.width || sourceWidth, height = dimensions.height || sourceHeight
  const label = name ? `${name} (#${selected})` : `#${selected}`
  return <><div className="tracked-video-player">
    <video ref={video} controls preload="metadata" src={url} onLoadStart={() => { setLoaded(false); setFailed(false); setTime(0); setPlaying(false); setDimensions({width: 0, height: 0}) }} onError={() => setFailed(true)} onLoadedMetadata={e => { setLoaded(true); setDimensions({width: e.currentTarget.videoWidth, height: e.currentTarget.videoHeight}); setTime(e.currentTarget.currentTime) }} onTimeUpdate={e => setTime(e.currentTarget.currentTime)} onSeeked={e => setTime(e.currentTarget.currentTime)} onPlay={() => setPlaying(true)} onPause={() => setPlaying(false)} onEnded={() => setPlaying(false)}>Your browser cannot play this video.</video>
    {selected !== null && width > 0 && height > 0 && <svg className="tracked-video-overlay" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
      {path && <polyline points={path} fill="none" stroke="#ffe358" strokeWidth="3" vectorEffect="non-scaling-stroke" />}
      {track && <><rect x={track.box_x} y={track.box_y} width={Math.max(1, track.box_width)} height={Math.max(1, track.box_height)} fill="none" stroke="#ff5858" strokeWidth="3" vectorEffect="non-scaling-stroke"/><circle cx={track.x} cy={track.y} r={Math.max(width, height) / 200} fill="#ff5858"/><text x={Math.max(0, Math.min(track.x + width / 150, width - width / 5))} y={Math.max(height / 30, track.y - height / 100)} fontSize={Math.max(width, height) / 45} fill="#fff" stroke="#000" strokeWidth={Math.max(width, height) / 400} paintOrder="stroke">{label}{track.missed_frames ? ' (last seen)' : ''}</text></>}
    </svg>}
  </div>{failed ? <p className="error" role="alert">The video could not be played. Video conversion requires FFmpeg on the server.</p> : needsConversion && !loaded ? <p className="muted" role="status">Preparing this video for browser playback. The first conversion may take some time.</p> : null}</>
}
