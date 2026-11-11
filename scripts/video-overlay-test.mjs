import test from 'node:test'
import assert from 'node:assert/strict'
import {frameAtTime, pathAtTime} from '../frontend/src/components/videoOverlay.mjs'

const frames = [
  {time_ms: 0, tracks: []},
  {time_ms: 100, tracks: [{id: 1, x: 10, y: 20}]},
  {time_ms: 200, tracks: []},
  {time_ms: 300, tracks: [{id: 1, x: 30, y: 40}]},
]
const points = [{time: 100, x: 10, y: 20}, {time: 300, x: 30, y: 40}]

test('selects the current recorded frame when playing or seeking', () => {
  assert.equal(frameAtTime(frames, -0.1), undefined)
  assert.equal(frameAtTime(frames, 0.15), frames[1])
  assert.equal(frameAtTime(frames, 0.25), frames[2])
  assert.equal(frameAtTime(frames, 0.3), frames[3])
  assert.equal(frameAtTime(frames, 0.05), frames[0])
})

test('shows only the path already travelled at the current time', () => {
  assert.equal(pathAtTime(points, 0), '')
  assert.equal(pathAtTime(points, 0.2), '10,20')
  assert.equal(pathAtTime(points, 0.3), '10,20 30,40')
  assert.equal(pathAtTime(points, 0.1), '10,20')
})

test('limits a long path while retaining both ends', () => {
  const many = Array.from({length: 5000}, (_, i) => ({time: i, x: i, y: 0}))
  const path = pathAtTime(many, 5).split(' ')
  assert.equal(path.length, 1000)
  assert.equal(path[0], '0,0')
  assert.equal(path.at(-1), '4999,0')
})
