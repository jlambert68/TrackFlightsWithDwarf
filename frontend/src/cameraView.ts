export type CameraView = { x: number; y: number; z: number; heading: number; pitch: number; roll: number }

const storageKey = 'trackflights-default-camera-v1'
export const cameraViewChanged = 'trackflights-default-camera-changed'

export function readDefaultCamera(): CameraView | null {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(storageKey) ?? 'null')
    if (!value || typeof value !== 'object') return null
    const camera = value as CameraView
    if (![camera.x, camera.y, camera.z, camera.heading, camera.pitch, camera.roll].every(Number.isFinite) || Math.hypot(camera.x, camera.y, camera.z) < 1) return null
    return camera
  } catch { return null }
}

export function saveDefaultCamera(camera: CameraView): boolean {
  try {
    localStorage.setItem(storageKey, JSON.stringify(camera))
    window.dispatchEvent(new Event(cameraViewChanged))
    return true
  } catch { return false }
}
