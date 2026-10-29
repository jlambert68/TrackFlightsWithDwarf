import { test } from 'node:test'
import { strict as assert } from 'node:assert'
import { splitAtGaps, unresolvedSignalSegments } from './trackHistory.ts'

const point = seconds => ({timestamp: new Date(seconds * 1000).toISOString(), latitude: 59, longitude: 18})

test('track gaps and externally covered intervals are not connected twice', () => {
  const external = [point(0), point(30), point(300), point(330)]
  assert.equal(splitAtGaps(external).length, 2)
  const local = [point(0), point(30), point(60), point(300), point(330)]
  const fallback = unresolvedSignalSegments(local, external)
  assert.equal(fallback.length, 1)
  assert.deepEqual(fallback[0].map(p => p.timestamp), [point(30).timestamp, point(60).timestamp])
})
