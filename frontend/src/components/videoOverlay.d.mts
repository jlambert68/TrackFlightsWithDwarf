export type VideoTrack = {id: number; x: number; y: number; box_x: number; box_y: number; box_width: number; box_height: number; missed_frames?: number}
export type VideoFrame = {time_ms: number; tracks: VideoTrack[]}
export type OverlayPoint = {x: number; y: number; time: number}
export function frameAtTime(frames: VideoFrame[], seconds: number): VideoFrame | undefined
export function selectedTrackAtTime(frames: VideoFrame[], selected: number | null, seconds: number): VideoTrack | undefined
export function selectionSeekTime(frames: VideoFrame[], selected: number | null, seconds: number): number | undefined
export function nearestCrop<T extends {frame: number}>(crops: T[], frame: number, maxDistance: number): T | undefined
export function visibleTrackBox(track: VideoTrack, width: number, height: number, scale?: number): {x: number; y: number; width: number; height: number}
export function pathAtTime(points: OverlayPoint[], seconds: number, maxPoints?: number): string
