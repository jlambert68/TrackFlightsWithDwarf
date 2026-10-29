import { test } from 'node:test'
import { strict as assert } from 'node:assert'
import { FlightHistoryManager } from './flightHistoryManager.ts'
import { mergeHistoryPoints, unresolvedSignalSegments } from './trackHistory.ts'
import { captureMotion, historyDiverges, projectPosition } from './flightProjection.ts'

const start = Date.parse('2026-10-08T19:00:00Z')
const point = seconds => ({timestamp: new Date(start + seconds * 1000).toISOString(), latitude: 59, longitude: 18, altitudeFeet: 9000})
const tick = () => new Promise(resolve => setImmediate(resolve))

test('first fix loads its lookback; new fixes queue refresh and retain earlier history', async () => {
  const requests = [], releases = []; let latest = new Map()
  const manager = new FlightHistoryManager(false, (icao, before, signal) => { requests.push({icao, before, signal}); return new Promise(resolve => releases.push(resolve)) }, points => { latest = points })
  manager.update(new Map([['ABC123', [point(0)]]]), start)
  assert.equal(requests[0].before, point(0).timestamp)
  manager.update(new Map([['ABC123', [point(0), point(10), point(20)]]]), start + 20000)
  assert.equal(requests.length, 1)
  releases.shift()({lookbackSeconds: 300, points: [point(-20), point(-10)]}); await tick()
  assert.equal(requests[1].before, point(20).timestamp)
  releases.shift()({lookbackSeconds: 300, points: [point(0), point(10)]}); await tick()
  assert.deepEqual(latest.get('ABC123').map(p => p.timestamp), [point(-20), point(-10), point(0), point(10)].map(p => p.timestamp))
  manager.dispose()
})

test('rewinding cancels stale requests and starts at the rewound fix', async () => {
  const requests = []; let latest = new Map()
  const manager = new FlightHistoryManager(false, (icao, before, signal) => { requests.push({before, signal}); return new Promise(() => {}) }, points => { latest = points })
  manager.update(new Map([['ABC123', [point(20)]]]), start + 20000)
  manager.update(new Map([['ABC123', [point(0)]]]), start)
  assert.equal(requests[0].signal.aborted, true)
  assert.equal(requests[1].before, point(0).timestamp)
  assert.deepEqual(latest.get('ABC123'), [])
  manager.dispose()
})

test('internet segments replace covered receiver lines but preserve the bridge and gaps', () => {
  const signals = [point(0), point(10), point(20)]
  const partial = mergeHistoryPoints([], [point(-10), point(-5)], point(0).timestamp)
  assert.deepEqual(unresolvedSignalSegments(signals, partial).map(s => s.map(p => p.timestamp)), [[-5, 0, 10, 20].map(s => point(s).timestamp)])
  const covered = mergeHistoryPoints(partial, [point(0), point(10), point(20)], point(21).timestamp)
  assert.deepEqual(unresolvedSignalSegments(signals, covered), [])
})

test('replay connects earlier receiver fixes even across long gaps', () => {
  const signals = [point(0), point(300), point(600)]
  assert.deepEqual(unresolvedSignalSegments(signals, [], Infinity, 120000).map(segment => segment.map(p => p.timestamp)), [signals.map(p => p.timestamp)])
})

test('projection retains received position and stops on 500m divergence', () => {
  const origin = point(0), motion = captureMotion(360, 90)
  const projected = projectPosition(origin, motion, 5)
  assert.ok(projected.longitude > origin.longitude)
  assert.equal(origin.longitude, 18)
  assert.equal(historyDiverges(origin, motion, [{...projected, timestamp: point(5).timestamp}]), false)
  assert.equal(historyDiverges(origin, motion, [{...projected, latitude: projected.latitude + .01, timestamp: point(5).timestamp}]), true)
})

test('live refresh advances from the latest fix and a failed refresh keeps cached history', async () => {
  const calls = []; let currentTime = start + 100, published = new Map(), status = ''
  const manager = new FlightHistoryManager(true, async (_icao, before) => { calls.push(before); if (calls.length === 2) throw new Error('offline'); return {lookbackSeconds: 300, points: [point(-20), point(-10)]} }, (points, value) => { published = points; status = value }, () => currentTime)
  manager.update(new Map([['ABC123', [point(0)]]]), 0); await tick()
  assert.equal(calls[0], point(0).timestamp)
  currentTime = start + 16000; manager.refresh(); await tick()
  assert.equal(calls[1], point(16).timestamp)
  assert.deepEqual(published.get('ABC123').map(p => p.timestamp), [point(-20).timestamp, point(-10).timestamp])
  assert.match(status, /offline/)
  manager.dispose()
})

test('paused replay retries failed history at the recorded time', async () => {
  const calls = []; let published = new Map()
  const manager = new FlightHistoryManager(false, async (_icao, before) => { calls.push(before); if (calls.length === 1) throw new Error('offline'); return {lookbackSeconds: 300, points: [point(-10), point(0), point(10)]} }, points => { published = points })
  manager.update(new Map([['ABC123', [point(0)]]]), start); await tick()
  manager.refresh(); await tick()
  assert.deepEqual(calls, [point(0).timestamp, point(0).timestamp])
  assert.deepEqual(published.get('ABC123').map(p => p.timestamp), [point(-10).timestamp])
  manager.refresh(); await tick()
  assert.equal(calls.length, 2)
  manager.dispose()
})
