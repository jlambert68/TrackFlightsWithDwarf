export type SynchronizedPlayback = {play(): void; pause(): void; seek(seconds: number): void; update(): void; dispose(): void}
export function createSynchronizedPlayback(videos: HTMLVideoElement[], handlers: {onState(state: {wanted: boolean; buffering: boolean}): void; onTime(time: number): void; onError(error: unknown): void}): SynchronizedPlayback
