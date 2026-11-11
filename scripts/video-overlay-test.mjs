import test from 'node:test'
import assert from 'node:assert/strict'
import {frameAtTime, pathAtTime, selectedTrackAtTime, visibleTrackBox} from '../frontend/src/components/videoOverlay.mjs'

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

test('selects only the requested object at the shared playback time', () => {
  const records = [
    {time_ms: 0, tracks: [{id: 1, x: 10, y: 20}, {id: 2, x: 90, y: 80}]},
    {time_ms: 100, tracks: [{id: 2, x: 95, y: 85}]},
    {time_ms: 200, tracks: [{id: 1, x: 30, y: 40}]},
  ]
  assert.equal(selectedTrackAtTime(records, null, 0), undefined)
  assert.equal(selectedTrackAtTime(records, 1, 0.1), undefined)
  assert.equal(selectedTrackAtTime(records, 2, 0.1).x, 95)
  assert.equal(selectedTrackAtTime(records, 1, 0.2).x, 30)
  assert.equal(selectedTrackAtTime(records, 1, 0).x, 10)
})

test('makes a tiny marker visible without moving it outside the frame', () => {
  const box = visibleTrackBox({box_x: 2000, box_y: 1000, box_width: 4, box_height: 4}, 3840, 2160)
  assert.ok(box.width >= 153 && box.height >= 86)
  assert.equal(box.x + box.width / 2, 2002)
  assert.equal(box.y + box.height / 2, 1002)
  const edge = visibleTrackBox({box_x: 0, box_y: 0, box_width: 4, box_height: 4}, 3840, 2160)
  assert.equal(edge.x, 0); assert.equal(edge.y, 0)
  const larger = visibleTrackBox({box_x: 2000, box_y: 1000, box_width: 4, box_height: 4}, 3840, 2160, 8)
  assert.equal(larger.width, box.width * 2)
  assert.equal(larger.height, box.height * 2)
})
