// Metadata timestamps are relative to the first frame in each recorded video.
export function frameAtTime(frames, seconds) {
  const time = seconds * 1000
  let low = 0, high = frames.length
  while (low < high) {
    const middle = (low + high) >>> 1
    if (frames[middle].time_ms <= time + 0.1) low = middle + 1
    else high = middle
  }
  return low ? frames[low - 1] : undefined
}

export function selectedTrackAtTime(frames, selected, seconds) {
  if (selected == null) return undefined
  return frameAtTime(frames, seconds)?.tracks?.find(track => track.id === selected)
}

// Selecting an object outside its visible interval should reveal it in all views.
export function selectionSeekTime(frames, selected, seconds) {
  if (selected == null) return undefined
  const current = selectedTrackAtTime(frames, selected, seconds)
  if (current && !current.missed_frames) return undefined
  const first = frames.find(frame => frame.tracks?.some(track => track.id === selected && !track.missed_frames))
  return first ? first.time_ms / 1000 : undefined
}

// Crops can be sampled less often than tracking frames. Avoid showing a stale
// image when there is a long gap in the saved crops.
export function nearestCrop(crops, frame, maxDistance) {
  let low = 0, high = crops.length
  while (low < high) {
    const middle = (low + high) >>> 1
    if (crops[middle].frame < frame) low = middle + 1
    else high = middle
  }
  const before = crops[low - 1], after = crops[low]
  const closest = !before ? after : !after || frame - before.frame <= after.frame - frame ? before : after
  return closest && Math.abs(closest.frame - frame) <= maxDistance ? closest : undefined
}

// Expand tiny boxes for display while retaining the detected center.
export function visibleTrackBox(track, width, height, scale = 4) {
  const boxWidth = Math.min(width, Math.max(track.box_width * scale, width * scale * 0.01))
  const boxHeight = Math.min(height, Math.max(track.box_height * scale, height * scale * 0.01))
  const centerX = track.box_x + track.box_width / 2
  const centerY = track.box_y + track.box_height / 2
  return {
    x: Math.max(0, Math.min(width - boxWidth, centerX - boxWidth / 2)),
    y: Math.max(0, Math.min(height - boxHeight, centerY - boxHeight / 2)),
    width: boxWidth,
    height: boxHeight,
  }
}

export function pathAtTime(points, seconds, maxPoints = 1000) {
  const time = seconds * 1000
  let low = 0, high = points.length
  while (low < high) {
    const middle = (low + high) >>> 1
    if (points[middle].time <= time + 0.1) low = middle + 1
    else high = middle
  }
  const count = Math.min(low, maxPoints)
  if (!count) return ''
  if (count === 1) return `${points[0].x},${points[0].y}`
  return Array.from({length: count}, (_, i) => {
    const point = points[Math.floor(i * (low - 1) / (count - 1))]
    return `${point.x},${point.y}`
  }).join(' ')
}
