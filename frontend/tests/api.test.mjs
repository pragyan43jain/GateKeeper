import { test } from 'node:test'
import assert from 'node:assert/strict'
import { fetchGatewayMetrics, normalizePlaygroundUrl, parsePrometheusText } from '../src/services/api.ts'

test('dashboard discovers only actual upstreams', async () => {
  const original = globalThis.fetch
  globalThis.fetch = async () => new Response('gateway_backend_circuit_breaker_state{backend="actual:9000"} 1\ngateway_backend_circuit_breaker_trips_total{backend="actual:9000"} 3\n')
  try {
    const metrics = await fetchGatewayMetrics()
    assert.deepEqual(metrics.backends.map(b => b.id), ['actual:9000'])
    assert.equal(metrics.backends[0].state, 'open')
    assert.equal(metrics.backends[0].trips, 3)
  } finally { globalThis.fetch = original }
})

test('parses request counters and preserves labels', () => {
  assert.deepEqual(parsePrometheusText('gateway_requests_total{code="429",method="GET"} 4')[0].labels, {code: '429', method: 'GET'})
})

test('local gateway URLs use the configured proxy', () => {
  assert.equal(normalizePlaygroundUrl('http://localhost:8082/api/students').effectiveUrl, '/api/students')
})

test('histogram components inherit their family type for explorer filtering', () => {
  const samples = parsePrometheusText('# TYPE gateway_request_duration_seconds histogram\ngateway_request_duration_seconds_bucket{le="0.1"} 5\ngateway_request_duration_seconds_sum 0.2\ngateway_request_duration_seconds_count 5')
  assert.equal(samples.length, 3)
  assert.ok(samples.every(s => s.type === 'histogram'))
})
