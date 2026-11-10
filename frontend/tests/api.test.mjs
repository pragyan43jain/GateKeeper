import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  fetchGatewayMetrics,
  normalizePlaygroundUrl,
  parsePrometheusText,
  executePlaygroundRequest,
  fetchRequestLogs,
  fetchBackendDiagnostics,
  fetchApplications,
  createApplication,
  fetchAPIKeys,
  createAPIKey,
  revokeAPIKey,
  updateAPIKeyPolicy,
  fetchLoadBalancingSettings,
  updateLoadBalancingSettings,
  fetchAuditLogs,
  fetchManagedBackends,
  fetchBackendHealthHistory,
  createManagedBackend,
  updateManagedBackend,
  deleteManagedBackend,
  fetchManagedRoutes,
  createManagedRoute,
  updateManagedRoute,
  deleteManagedRoute,
  registerDashboard,
} from '../src/services/api.ts'

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

test('dashboard self-registration creates a session without sending a consumer API key', async () => {
  const original = globalThis.fetch
  let request
  globalThis.fetch = async (url, init) => {
    request = { url: String(url), init }
    return Response.json({ id: 'viewer-1', email: 'viewer@example.test', role: 'viewer' }, { status: 200 })
  }
  try {
    const principal = await registerDashboard('viewer@example.test', 'correct-horse-battery')
    assert.equal(principal.role, 'viewer')
    assert.ok(request.url.endsWith('/api/auth/register'))
    assert.equal(request.init.credentials, 'include')
    assert.equal(request.init.headers['X-API-Key'], undefined)
    assert.deepEqual(JSON.parse(request.init.body), { email: 'viewer@example.test', password: 'correct-horse-battery' })
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

test('playground request captures rate limit reset and degraded headers', async () => {
  const original = globalThis.fetch
  globalThis.fetch = async () => new Response('{"status":"ok"}', {
    status: 200,
    headers: {
      'content-type': 'application/json',
      'x-backend-id': 'backend-1',
      'x-ratelimit-limit': '10',
      'x-ratelimit-remaining': '8',
      'x-ratelimit-reset': '1730635200',
      'x-ratelimit-degraded': 'true',
      'x-request-id': 'req-trace-uuid-12345',
    }
  })
  try {
    const res = await executePlaygroundRequest('/api/students', 'GET', 'test-key')
    assert.equal(res.status, 200)
    assert.equal(res.backendId, 'backend-1')
    assert.equal(res.requestId, 'req-trace-uuid-12345')
    assert.equal(res.rateLimitRemaining, '8')
    assert.equal(res.rateLimitReset, '1730635200')
    assert.equal(res.rateLimitDegraded, true)
  } finally { globalThis.fetch = original }
})

test('metrics parsing extracts backend health status', async () => {
  const original = globalThis.fetch
  globalThis.fetch = async () => new Response(
    'gateway_backend_circuit_breaker_state{backend="backend-1"} 0\n' +
    'gateway_backend_health_status{backend="backend-1"} 0\n' +
    'gateway_backend_circuit_breaker_state{backend="backend-2"} 1\n' +
    'gateway_backend_health_status{backend="backend-2"} 1\n'
  )
  try {
    const metrics = await fetchGatewayMetrics()
    assert.equal(metrics.backends.length, 2)
    const b1 = metrics.backends.find(b => b.id === 'backend-1')
    const b2 = metrics.backends.find(b => b.id === 'backend-2')
    assert.equal(b1?.healthStatus, 'healthy')
    assert.equal(b2?.healthStatus, 'unhealthy')
  } finally { globalThis.fetch = original }
})

test('fetchRequestLogs dispatches authenticated query', async () => {
  const original = globalThis.fetch
  let capturedHeaders = {}
  let capturedUrl = ''
  globalThis.fetch = async (url, init) => {
    capturedUrl = url
    capturedHeaders = init?.headers || {}
    return new Response(JSON.stringify({
      requests: [
        {
          id: 'test-req-1',
          method: 'GET',
          path: '/api/students',
          status: 200,
          durationMs: 1.5,
          backend: 'backend-1',
          timestamp: '2026-11-03T12:00:00Z',
        }
      ]
    }), { status: 200, headers: { 'content-type': 'application/json' } })
  }
  try {
    const logs = await fetchRequestLogs('adminkey-123', { limit: 25, status: 200 })
    assert.equal(capturedHeaders['X-API-Key'], 'adminkey-123')
    assert.ok(capturedUrl.includes('limit=25'))
    assert.ok(capturedUrl.includes('status=200'))
    assert.equal(logs.length, 1)
    assert.equal(logs[0].id, 'test-req-1')
    assert.equal(logs[0].status, 200)
  } finally { globalThis.fetch = original }
})

test('fetchBackendDiagnostics dispatches authenticated request and returns backends', async () => {
  const original = globalThis.fetch
  let capturedHeaders = {}
  globalThis.fetch = async (_url, init) => {
    capturedHeaders = init?.headers || {}
    return new Response(JSON.stringify({
      backends: [
        {
          id: 'backend-1',
          url: 'http://localhost:8081',
          healthStatus: 'healthy',
          circuitBreakerState: 'closed',
          consecutiveFailures: 0,
          consecutiveSuccesses: 4,
          lastCheckTime: '2026-11-03T12:00:00Z',
        }
      ]
    }), { status: 200, headers: { 'content-type': 'application/json' } })
  }
  try {
    const diags = await fetchBackendDiagnostics('adminkey-123')
    assert.equal(capturedHeaders['X-API-Key'], 'adminkey-123')
    assert.equal(diags.length, 1)
    assert.equal(diags[0].id, 'backend-1')
    assert.equal(diags[0].healthStatus, 'healthy')
  } finally { globalThis.fetch = original }
})

test('API key management service authenticates requests and exposes lifecycle operations', async () => {
  const original = globalThis.fetch
  const calls = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init })
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    if (String(url).endsWith('/api/applications') && init?.method === 'POST') {
      return Response.json({ id: 'app-1', name: 'Mobile App', description: '', createdAt: '2026-11-04T00:00:00Z' }, { status: 201 })
    }
    if (String(url).endsWith('/api/applications')) return Response.json({ applications: [] })
    if (String(url).endsWith('/api/keys') && init?.method === 'POST') {
      return Response.json({ id: 'key-1', prefix: 'gk_abc12345', secret: 'gk_one-time-secret' }, { status: 201 })
    }
    return Response.json({ keys: [] })
  }
  try {
    assert.deepEqual(await fetchApplications('admin-key'), [])
    const app = await createApplication('admin-key', { name: 'Mobile App', description: '' })
    assert.equal(app.id, 'app-1')
    assert.deepEqual(await fetchAPIKeys('admin-key'), [])
    const created = await createAPIKey('admin-key', {
      applicationId: app.id,
      name: 'Mobile read key',
      permissions: ['read'],
      requestsPerWindow: 50,
      windowSeconds: 60,
      burst: 5,
    })
    assert.equal(created.secret, 'gk_one-time-secret')
    await revokeAPIKey('admin-key', created.id)
    await updateAPIKeyPolicy('admin-key', created.id, {
      permissions: ['read'],
      requestsPerWindow: 20,
      windowSeconds: 30,
      burst: 2,
    })
    assert.equal(calls.length, 6)
    assert.ok(calls.every(({ init }) => init?.headers?.['X-API-Key'] === 'admin-key'))
    assert.equal(JSON.parse(calls[3].init.body).requestsPerWindow, 50)
    assert.equal(JSON.parse(calls[3].init.body).burst, 5)
    assert.equal(calls[4].init.method, 'DELETE')
    assert.equal(calls[5].init.method, 'PATCH')
  } finally { globalThis.fetch = original }
})

test('load balancing settings service reads and persists selected strategy and weights', async () => {
  const original = globalThis.fetch
  const calls = []
  const settings = { strategy: 'weighted_round_robin', weights: { 'backend-a:8081': 2, 'backend-b:8081': 1 } }
  globalThis.fetch = async (_url, init) => {
    calls.push(init)
    return Response.json(settings)
  }
  try {
    assert.deepEqual(await fetchLoadBalancingSettings('admin-key'), settings)
    assert.deepEqual(await updateLoadBalancingSettings('admin-key', settings), settings)
    assert.equal(calls[0].method, undefined)
    assert.equal(calls[1].method, 'PUT')
    assert.deepEqual(JSON.parse(calls[1].body), settings)
    assert.ok(calls.every((call) => call.headers['X-API-Key'] === 'admin-key'))
  } finally { globalThis.fetch = original }
})

test('audit log service requests a bounded authenticated event list', async () => {
  const original = globalThis.fetch
  let capturedUrl = ''
  let capturedHeaders = {}
  globalThis.fetch = async (url, init) => {
    capturedUrl = String(url)
    capturedHeaders = init?.headers || {}
    return Response.json({ events: [{ id: 3, actor: 'bootstrap_admin', action: 'PUT', resourceType: 'setting', resourceId: 'load_balancing', details: { status: 200 }, createdAt: '2026-11-05T00:00:00Z' }] })
  }
  try {
    const events = await fetchAuditLogs('admin-key', 25)
    assert.ok(capturedUrl.endsWith('/api/audit-logs?limit=25'))
    assert.equal(capturedHeaders['X-API-Key'], 'admin-key')
    assert.equal(events[0].resourceType, 'setting')
  } finally { globalThis.fetch = original }
})

test('backend management service lists, registers, updates, and removes nodes', async () => {
  const original = globalThis.fetch
  const calls = []
  const backend = { configId: 'node-1', id: 'backend-a:8081', name: 'backend-a', url: 'http://backend-a:8081', weight: 2, enabled: true, maintenance: false, healthPath: '/health' }
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init })
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    return Response.json({ backends: [backend] })
  }
  try {
    assert.deepEqual(await fetchManagedBackends('admin-key'), [backend])
    await createManagedBackend('admin-key', backend)
    await updateManagedBackend('admin-key', { ...backend, maintenance: true })
    await deleteManagedBackend('admin-key', backend.configId)
    assert.deepEqual(calls.map(call => call.init?.method || 'GET'), ['GET', 'POST', 'PUT', 'DELETE'])
    assert.ok(calls.every(call => call.init?.headers?.['X-API-Key'] === 'admin-key'))
    assert.equal(JSON.parse(calls[2].init.body).maintenance, true)
  } finally { globalThis.fetch = original }
})

test('backend health history service requests the selected backend with a bounded limit', async () => {
  const original = globalThis.fetch
  let captured
  globalThis.fetch = async (url, init) => {
    captured = { url: String(url), init }
    return Response.json({ events: [{ id: 1, backendId: 'backend-1', state: 'unhealthy' }] })
  }
  try {
    const events = await fetchBackendHealthHistory('admin-key', 'backend/1', 20)
    assert.equal(events.length, 1)
    assert.match(captured.url, /backendId=backend%2F1/)
    assert.match(captured.url, /limit=20/)
    assert.equal(captured.init.credentials, 'include')
    assert.equal(captured.init.headers['X-API-Key'], 'admin-key')
  } finally { globalThis.fetch = original }
})

test('route management service persists routing and authentication controls', async () => {
  const original = globalThis.fetch
  const calls = []
  const route = { id: 'route-1', name: 'Orders', pathPrefix: '/api/orders', methods: ['GET', 'POST'], backendId: 'backend-1', backendName: 'orders', authRequired: true, enabled: true, requestsPerWindow: 20, windowSeconds: 60, burst: 3 }
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init })
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    return Response.json({ routes: [route] })
  }
  try {
    assert.equal((await fetchManagedRoutes('admin-key'))[0].id, route.id)
    await createManagedRoute('admin-key', route)
    await updateManagedRoute('admin-key', { ...route, authRequired: false, enabled: false })
    await deleteManagedRoute('admin-key', route.id)
    assert.deepEqual(calls.map(call => call.init?.method || 'GET'), ['GET', 'POST', 'PUT', 'DELETE'])
    assert.ok(calls.every(call => call.init?.headers?.['X-API-Key'] === 'admin-key'))
    assert.equal(JSON.parse(calls[2].init.body).authRequired, false)
    assert.equal(JSON.parse(calls[2].init.body).burst, 3)
  } finally { globalThis.fetch = original }
})
