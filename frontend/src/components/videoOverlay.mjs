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
