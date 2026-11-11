export type VideoTrack = {id: number; x: number; y: number; box_x: number; box_y: number; box_width: number; box_height: number; missed_frames?: number}
export type VideoFrame = {time_ms: number; tracks: VideoTrack[]}
export type OverlayPoint = {x: number; y: number; time: number}
export function frameAtTime(frames: VideoFrame[], seconds: number): VideoFrame | undefined
export function pathAtTime(points: OverlayPoint[], seconds: number, maxPoints?: number): string
