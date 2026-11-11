import test from 'node:test'
import assert from 'node:assert/strict'
import {createSynchronizedPlayback} from '../frontend/src/components/synchronizedPlayback.mjs'
import {selectedTrackAtTime} from '../frontend/src/components/videoOverlay.mjs'

class FakeVideo extends EventTarget {
  _time = 0
  duration = 20
  readyState = 4
  seeking = false
  paused = true
  playCount = 0
  get currentTime() { return this._time }
  set currentTime(value) { this._time = value; this.seeking = true }
  play() { this.playCount++; this.paused = false; return Promise.resolve() }
  pause() { this.paused = true }
  finishSeek() { this.seeking = false; this.dispatchEvent(new Event('seeked')) }
}

const makePlayer = videos => {
  const states = [], times = [], errors = []
  const player = createSynchronizedPlayback(videos, {onState: state => states.push(state), onTime: time => times.push(time), onError: error => errors.push(error)})
  return {player, states, times, errors}
}

test('play, pause and forward/backward seeking control both videos', async () => {
  const a = new FakeVideo(), b = new FakeVideo()
  const {player, states} = makePlayer([a, b])
  player.play(); await Promise.resolve()
  assert.equal(a.paused, false); assert.equal(b.paused, false)
  player.seek(9)
  assert.equal(a.currentTime, 9); assert.equal(b.currentTime, 9)
  assert.equal(a.paused, true); assert.equal(b.paused, true)
  a.finishSeek(); assert.equal(a.paused, true)
  b.finishSeek(); await Promise.resolve()
  assert.equal(a.paused, false); assert.equal(b.paused, false)
  player.seek(2); a.finishSeek(); b.finishSeek(); await Promise.resolve()
  assert.equal(a.currentTime, 2); assert.equal(b.currentTime, 2)
  player.pause()
  assert.equal(a.paused, true); assert.equal(b.paused, true)
  assert.equal(states.at(-1).wanted, false)
  player.dispose()
})

test('pauses both on buffering and resumes only when both are ready', async () => {
  const a = new FakeVideo(), b = new FakeVideo()
  const {player, states} = makePlayer([a, b])
  player.play(); await Promise.resolve()
  b.readyState = 2; b.dispatchEvent(new Event('waiting'))
  assert.equal(a.paused, true); assert.equal(b.paused, true)
  assert.equal(states.at(-1).buffering, true)
  a.dispatchEvent(new Event('canplay')); assert.equal(a.paused, true)
  b.readyState = 4; b.dispatchEvent(new Event('canplay')); await Promise.resolve()
  assert.equal(a.paused, false); assert.equal(b.paused, false)
  player.dispose()
})

test('corrects drift, clamps to the shorter video, and stops both at the end', async () => {
  const a = new FakeVideo(), b = new FakeVideo(); b.duration = 12
  const {player} = makePlayer([a, b])
  player.play(); await Promise.resolve()
  a._time = 5; b._time = 4.8; player.update()
  assert.equal(b.currentTime, 5)
  b.finishSeek()
  player.seek(30)
  assert.equal(a.currentTime, 12); assert.equal(b.currentTime, 12)
  a.finishSeek(); b.finishSeek(); await Promise.resolve()
  b.dispatchEvent(new Event('ended'))
  assert.equal(a.paused, true); assert.equal(b.paused, true)
  player.dispose()
})

test('playback failures stop both videos', async () => {
  const a = new FakeVideo(), b = new FakeVideo()
  b.play = () => Promise.reject(new Error('playback blocked'))
  const {player, errors} = makePlayer([a, b])
  player.play(); await new Promise(resolve => setTimeout(resolve, 0))
  assert.equal(a.paused, true); assert.equal(b.paused, true); assert.equal(errors.length, 1)
  player.dispose()
})

test('seeking while paused leaves both videos paused', () => {
  const a = new FakeVideo(), b = new FakeVideo()
  const {player} = makePlayer([a, b])
  player.seek(4)
  a.finishSeek(); b.finishSeek()
  assert.equal(a.currentTime, 4); assert.equal(b.currentTime, 4)
  assert.equal(a.paused, true); assert.equal(b.paused, true)
  assert.equal(a.playCount, 0); assert.equal(b.playCount, 0)
  player.dispose()
})

test('paused playback does not resume when delayed play promises settle', async () => {
  const a = new FakeVideo(), b = new FakeVideo()
  let resolvePlay
  b.play = () => { b.paused = false; return new Promise(resolve => { resolvePlay = resolve }) }
  const {player, states} = makePlayer([a, b])
  player.play()
  player.pause()
  resolvePlay(); await Promise.resolve(); await Promise.resolve()
  assert.equal(a.paused, true); assert.equal(b.paused, true)
  assert.equal(states.at(-1).wanted, false)
  player.dispose()
})

test('the selected-object overlay follows both videos when seeking', () => {
  const a = new FakeVideo(), b = new FakeVideo()
  const frames = [{time_ms: 0, tracks: [{id: 4, x: 10}]}, {time_ms: 5000, tracks: [{id: 4, x: 30}]}, {time_ms: 10000, tracks: [{id: 4, x: 60}]}]
  let overlay
  const player = createSynchronizedPlayback([a, b], {onState() {}, onTime: time => { overlay = selectedTrackAtTime(frames, 4, time) }, onError(error) { throw error }})
  player.seek(10)
  assert.equal(a.currentTime, 10); assert.equal(b.currentTime, 10); assert.equal(overlay.x, 60)
  a.finishSeek(); b.finishSeek()
  player.seek(0)
  assert.equal(a.currentTime, 0); assert.equal(b.currentTime, 0); assert.equal(overlay.x, 10)
  player.dispose()
})
