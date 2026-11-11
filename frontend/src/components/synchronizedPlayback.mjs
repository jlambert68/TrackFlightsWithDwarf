// Both videos use the same timeline. Only this controller starts, pauses or
// seeks them; the first available video supplies the playback clock.
export function createSynchronizedPlayback(videos, {onState, onTime, onError}) {
  let wanted = false, buffering = false, starting = false, disposed = false, generation = 0
  const cleanups = []
  const notify = () => onState({wanted, buffering})
  const pauseMedia = () => { generation++; starting = false; for (const video of videos) video.pause() }
  const ready = () => videos.every(video => !video.seeking && video.readyState >= 3)
  const startMedia = () => {
    if (disposed || !wanted || starting || videos.some(video => video.seeking) || (buffering && !ready())) return
    buffering = false
    starting = true
    const current = ++generation
    notify()
    // Invoke play synchronously from the button handler to retain the user gesture.
    let starts
    try { starts = videos.map(video => video.play()) } catch (error) { starts = [Promise.reject(error)] }
    Promise.all(starts).then(() => {
      if (disposed || current !== generation) return
      starting = false
      notify()
    }).catch(error => {
      if (disposed || current !== generation) return
      wanted = false; buffering = false; pauseMedia(); notify(); onError(error)
    })
  }
  const waiting = () => {
    if (!wanted || disposed) return
    buffering = true
    pauseMedia()
    notify()
  }
  const available = () => {
    if (wanted && (buffering || videos.some(video => video.paused)) && !videos.some(video => video.seeking) && (!buffering || ready())) startMedia()
  }
  const ended = () => { wanted = false; buffering = false; pauseMedia(); notify(); onTime(videos[0]?.currentTime ?? 0) }
  for (const video of videos) {
    for (const [name, handler] of [['waiting', waiting], ['canplay', available], ['seeked', available], ['ended', ended]]) {
      video.addEventListener(name, handler)
      cleanups.push(() => video.removeEventListener(name, handler))
    }
  }
  const seek = seconds => {
    if (!videos.length || !Number.isFinite(seconds)) return
    const durations = videos.map(video => video.duration).filter(Number.isFinite)
    const end = durations.length ? Math.min(...durations) : Infinity
    const target = Math.max(0, Math.min(seconds, end))
    pauseMedia()
    buffering = false
    for (const video of videos) if (Math.abs(video.currentTime - target) > 0.001) video.currentTime = target
    onTime(target)
    notify()
    if (wanted && !videos.some(video => video.seeking)) startMedia()
  }
  return {
    play() {
      if (!videos.length || disposed) return
      const durations = videos.map(video => video.duration).filter(Number.isFinite)
      if (durations.length && videos[0].currentTime >= Math.min(...durations) - 0.001) seek(0)
      wanted = true; notify(); startMedia()
    },
    pause() { wanted = false; buffering = false; pauseMedia(); notify() },
    seek,
    update() {
      if (!videos.length || disposed) return
      const primary = videos[0]
      onTime(primary.currentTime)
      if (!wanted || buffering || primary.seeking || primary.paused) return
      for (const video of videos.slice(1)) {
        if (!video.seeking && Math.abs(video.currentTime - primary.currentTime) > 0.04) { seek(primary.currentTime); return }
      }
    },
    dispose() { disposed = true; wanted = false; pauseMedia(); for (const cleanup of cleanups) cleanup() },
  }
}
