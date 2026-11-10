import type {
  GatewayHealth,
  GatewayMetricsSummary,
  BackendNodeStatus,
  BackendDiagnostic,
  BackendHealthEvent,
  BackendHealthState,
  RequestLogEntry,
  MetricSample,
  CircuitBreakerState,
  TimeSeriesPoint,
  PlaygroundResponseData,
  NormalizedTarget,
  ApplicationRecord,
  APIKeyRecord,
  LoadBalancingSettings,
  AuditLogRecord,
  ManagedBackend,
  ManagedRoute,
} from '../types'

/**
 * Parses Prometheus text exposition format into structured samples.
 */
export function parsePrometheusText(text: string): MetricSample[] {
  const lines = text.split('\n')
  const samples: MetricSample[] = []
  const helpMap = new Map<string, string>()
  const typeMap = new Map<string, string>()

  for (const line of lines) {
    const trimmed = line.trim()
    if (!trimmed) continue

    if (trimmed.startsWith('# HELP ')) {
      const parts = trimmed.substring(7).split(' ')
      const name = parts[0]
      const help = parts.slice(1).join(' ')
      helpMap.set(name, help)
      continue
    }

    if (trimmed.startsWith('# TYPE ')) {
      const parts = trimmed.substring(7).split(' ')
      if (parts.length >= 2) {
        typeMap.set(parts[0], parts[1])
      }
      continue
    }

    if (trimmed.startsWith('#')) continue

    // Parse metric name, labels, and value
    // Formats:
    // name{label="val",...} 123.45
    // name 123.45
    const match = trimmed.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})?\s+([^\s]+)(?:\s+\d+)?$/)
    if (!match) continue

    const [, name, rawLabels, rawValue] = match
    const value = parseFloat(rawValue)
    if (isNaN(value)) continue

    const labels: Record<string, string> = {}
    if (rawLabels) {
      const labelRegex = /([a-zA-Z_][a-zA-Z0-9_]*)=(?:"([^"\\]*(?:\\.[^"\\]*)*)")/g
      let lMatch
      while ((lMatch = labelRegex.exec(rawLabels)) !== null) {
        labels[lMatch[1]] = lMatch[2]
      }
    }

    samples.push({
      name,
      help: helpMap.get(name),
      type: typeMap.get(name) || typeMap.get(name.replace(/_(bucket|sum|count)$/, '')),
      labels,
      value,
    })
  }

  return samples
}

const rawApiUrl = (import.meta as unknown as { env?: { VITE_API_URL?: string } }).env?.VITE_API_URL
export const API_BASE = typeof rawApiUrl === 'string' ? rawApiUrl.trim().replace(/\/+$/, '') : ''

// Management-only requests notify the shell on expiry; consumer 401s in the
// playground must never sign the dashboard user out.
export const SESSION_EXPIRED_EVENT = 'gatekeeper:session-expired'
async function sessionFetch(input: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(input, init)
  if (response.status === 401 && typeof window !== 'undefined') {
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT))
  }
  return response
}

/**
 * Checks the gateway public /health endpoint.
 */
export async function fetchGatewayHealth(): Promise<GatewayHealth> {
  const startTime = Date.now()
  try {
    const response = await fetch(`${API_BASE}/health`, {
    signal: AbortSignal.timeout(8000),
      method: 'GET',
      cache: 'no-store',
    })

    const text = await response.text()
    if (response.ok && text.includes('Gatekeeper is healthy')) {
      return {
        status: 'healthy',
        message: text.trim(),
        statusCode: response.status,
        checkedAt: new Date(startTime).toLocaleTimeString(),
      }
    }

    return {
      status: 'unhealthy',
      message: `Unexpected response: ${response.status} ${text.trim()}`,
      statusCode: response.status,
      checkedAt: new Date(startTime).toLocaleTimeString(),
    }
  } catch (err: unknown) {
    const errorMsg = err instanceof Error ? err.message : 'Connection failed'
    return {
      status: 'unhealthy',
      message: `Failed to reach gateway health check: ${errorMsg}`,
      checkedAt: new Date(startTime).toLocaleTimeString(),
    }
  }
}

/**
 * Converts circuit breaker numeric gauge value to state enum.
 * 0 = Closed, 1 = Open, 2 = Half-Open
 */
function numToCircuitState(num: number): CircuitBreakerState {
  switch (num) {
    case 0:
      return 'closed'
    case 1:
      return 'open'
    case 2:
      return 'half-open'
    default:
      return 'unknown'
  }
}

/**
 * Fetches and aggregates Gatekeeper metrics from the authenticated dashboard endpoint.
 */
export async function fetchGatewayMetrics(): Promise<GatewayMetricsSummary> {
  const response = await sessionFetch(`${API_BASE}/api/metrics`, {
    signal: AbortSignal.timeout(8000),
    method: 'GET',
    credentials: 'include',
    cache: 'no-store',
  })

  if (!response.ok) {
    throw new Error(`Failed to fetch dashboard metrics: ${response.status} ${response.statusText}`)
  }

  const text = await response.text()
  const samples = parsePrometheusText(text)

  let totalRequests = 0
  const requestsByCode: Record<string, number> = {}
  let rateLimitRejections = 0
  let durationSum = 0
  let durationCount = 0

  const backendStates: Record<string, CircuitBreakerState> = {}
  const backendTrips: Record<string, number> = {}
  const backendHealthStates: Record<string, BackendHealthState> = {}
  const backendHealthFailures: Record<string, number> = {}
  const backendHealthSuccesses: Record<string, number> = {}

  for (const s of samples) {
    if (s.name === 'gateway_requests_total') {
      const code = s.labels.code || 'unknown'
      requestsByCode[code] = (requestsByCode[code] || 0) + s.value
      totalRequests += s.value
    } else if (s.name === 'gateway_rate_limit_rejections_total') {
      rateLimitRejections += s.value
    } else if (s.name === 'gateway_request_duration_seconds_sum') {
      durationSum += s.value
    } else if (s.name === 'gateway_request_duration_seconds_count') {
      durationCount += s.value
    } else if (s.name === 'gateway_backend_circuit_breaker_state') {
      const backend = s.labels.backend
      if (backend) {
        backendStates[backend] = numToCircuitState(s.value)
      }
    } else if (s.name === 'gateway_backend_circuit_breaker_trips_total') {
      const backend = s.labels.backend
      if (backend) {
        backendTrips[backend] = s.value
      }
    } else if (s.name === 'gateway_backend_health_status') {
      const backend = s.labels.backend
      if (backend) {
        backendHealthStates[backend] = s.value === 0 ? 'healthy' : s.value === 1 ? 'unhealthy' : 'recovering'
      }
    } else if (s.name === 'gateway_backend_health_check_failures_total') {
      const backend = s.labels.backend
      if (backend) {
        backendHealthFailures[backend] = s.value
      }
    } else if (s.name === 'gateway_backend_health_check_successes_total') {
      const backend = s.labels.backend
      if (backend) {
        backendHealthSuccesses[backend] = s.value
      }
    }
  }

  const avgLatencyMs = durationCount > 0 ? (durationSum / durationCount) * 1000 : null

  // Format backend statuses
  const knownBackends: BackendNodeStatus[] = Object.entries(backendStates).map(([id, state]) => ({
    id,
    displayName: id,
    target: id,
    state,
    trips: backendTrips[id] || 0,
    healthStatus: backendHealthStates[id] || 'healthy',
    consecutiveFailures: backendHealthFailures[id] || 0,
    consecutiveSuccesses: backendHealthSuccesses[id] || 0,
  }))

  return {
    totalRequests,
    requestsByCode,
    rateLimitRejections,
    avgLatencyMs,
    totalDurationSum: durationSum,
    totalDurationCount: durationCount,
    backends: knownBackends,
    rawSamples: samples,
    scrapedAt: new Date().toLocaleTimeString(),
  }
}

/**
 * Normalizes user-specified playground URLs.
 * Default: relative paths through the local Vite reverse proxy (/api/...).
 * If http://localhost:8082/api/... is entered, normalizes to relative /api/... to prevent CORS blocking.
 * If a custom external absolute URL is entered, preserves it explicitly and returns an external target warning.
 */
export function normalizePlaygroundUrl(inputUrl: string): NormalizedTarget {
  const trimmed = inputUrl.trim()
  if (!trimmed) {
    return {
      effectiveUrl: API_BASE ? `${API_BASE}/api/students` : '/api/students',
      normalized: true,
      originalUrl: inputUrl,
      isExternal: false,
    }
  }

  // 1. Already a relative path: route through API_BASE or Vite dev proxy
  if (trimmed.startsWith('/')) {
    return {
      effectiveUrl: API_BASE ? `${API_BASE}${trimmed}` : trimmed,
      normalized: Boolean(API_BASE),
      originalUrl: trimmed,
      isExternal: false,
    }
  }

  // 2. Relative path without leading slash
  if (!trimmed.startsWith('http://') && !trimmed.startsWith('https://')) {
    return {
      effectiveUrl: API_BASE ? `${API_BASE}/${trimmed}` : `/${trimmed}`,
      normalized: true,
      originalUrl: trimmed,
      isExternal: false,
    }
  }

  // 3. Absolute URL: check if it targets the local Gateway or configured API_BASE
  try {
    const parsed = new URL(trimmed)
    const isLocalGateway =
      (parsed.hostname === 'localhost' || parsed.hostname === '127.0.0.1') &&
      (parsed.port === '8082' || parsed.port === '8080')

    const isCurrentOrigin =
      typeof window !== 'undefined' && parsed.origin === window.location.origin

    const isConfiguredApiBase =
      Boolean(API_BASE) && (parsed.origin === new URL(API_BASE, window.location.href).origin)

    if (isLocalGateway || isCurrentOrigin || isConfiguredApiBase) {
      const relativePath = parsed.pathname + parsed.search
      const pathWithSlash = relativePath.startsWith('/') ? relativePath : `/${relativePath}`
      return {
        effectiveUrl: API_BASE ? `${API_BASE}${pathWithSlash}` : pathWithSlash,
        normalized: true,
        originalUrl: trimmed,
        isExternal: false,
      }
    }

    // 4. Custom external target: handle explicitly without silent rewrite
    return {
      effectiveUrl: trimmed,
      normalized: false,
      originalUrl: trimmed,
      isExternal: true,
      warning: `Direct external request to ${parsed.origin}. This bypasses the GateKeeper local proxy and requires the remote server to allow CORS.`,
    }
  } catch {
    return {
      effectiveUrl: trimmed,
      normalized: false,
      originalUrl: trimmed,
      isExternal: false,
    }
  }
}

/**
 * Attempts to query Prometheus range API for historical request rates (requests per second).
 * Queries `sum by (code) (rate(gateway_requests_total[1m]))`.
 * Falls back to null if Prometheus proxy is unavailable.
 */
export async function fetchPrometheusTimeSeries(minutes: number = 15): Promise<TimeSeriesPoint[] | null> {
  try {
    const end = Math.floor(Date.now() / 1000)
    const start = end - minutes * 60
    const step = Math.max(15, Math.floor((minutes * 60) / 20))

    const query = 'sum by (code) (rate(gateway_requests_total[1m]))'
    const url = `${API_BASE}/prometheus/api/v1/query_range?query=${encodeURIComponent(query)}&start=${start}&end=${end}&step=${step}s`
    const res = await fetch(url, { cache: 'no-store', signal: AbortSignal.timeout(8000) })
    if (!res.ok) return null

    const data = await res.json()
    if (data.status !== 'success' || !data.data?.result) return null

    const timeMap = new Map<number, TimeSeriesPoint>()

    for (const series of data.data.result) {
      const code = series.metric?.code || 'unknown'
      const values: [number, string][] = series.values || []

      for (const [timestamp, valStr] of values) {
        const rawVal = parseFloat(valStr) || 0
        const rateVal = Math.round(rawVal * 1000) / 1000 // rounded to 3 decimal places
        if (!timeMap.has(timestamp)) {
          const d = new Date(timestamp * 1000)
          timeMap.set(timestamp, {
            timestamp,
            time: d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
            rate200: 0,
            rate401: 0,
            rate429: 0,
            rate5xx: 0,
            rateError: 0,
            totalRate: 0,
            requests200: 0,
            requests401: 0,
            requests429: 0,
            requests5xx: 0,
            requestsError: 0,
            total: 0,
            isFallback: false,
          })
        }

        const point = timeMap.get(timestamp)!
        if (code === '200') {
          point.rate200 += rateVal
          point.requests200 += rateVal
        } else if (code === '401') {
          point.rate401 += rateVal
          point.requests401 = (point.requests401 || 0) + rateVal
          point.rateError += rateVal
          point.requestsError += rateVal
        } else if (code === '429') {
          point.rate429 += rateVal
          point.requests429 = (point.requests429 || 0) + rateVal
          point.rateError += rateVal
          point.requestsError += rateVal
        } else if (code.startsWith('5')) {
          point.rate5xx += rateVal
          point.requests5xx = (point.requests5xx || 0) + rateVal
          point.rateError += rateVal
          point.requestsError += rateVal
        }
        point.totalRate += rateVal
        point.total += rateVal
      }
    }

    const sorted = Array.from(timeMap.values()).sort((a, b) => a.timestamp - b.timestamp)
    return sorted.length > 0 ? sorted : null
  } catch {
    return null
  }
}

/**
 * Sends a real HTTP request from the API Playground to the Gateway.
 * Kept entirely in-memory.
 */
export async function executePlaygroundRequest(
  url: string,
  method: string,
  apiKey: string,
  body?: string
): Promise<PlaygroundResponseData> {
  const target = normalizePlaygroundUrl(url)
  const startTime = performance.now()
  const headers: Record<string, string> = {}

  if (apiKey) {
    headers['X-API-Key'] = apiKey
  }
  if (body && (method === 'POST' || method === 'PUT' || method === 'PATCH')) {
    headers['Content-Type'] = 'application/json'
  }

  try {
    const response = await fetch(target.effectiveUrl, {
      signal: AbortSignal.timeout(15000),
      method,
      headers,
      body: body && (method === 'POST' || method === 'PUT' || method === 'PATCH') ? body : undefined,
    })

    const durationMs = Math.round(performance.now() - startTime)

    // Capture response headers
    const resHeaders: Record<string, string> = {}
    response.headers.forEach((val, key) => {
      resHeaders[key.toLowerCase()] = val
    })

    const rawText = await response.text()
    let formattedBody = rawText

    try {
      const parsed = JSON.parse(rawText)
      formattedBody = JSON.stringify(parsed, null, 2)
    } catch {
      // Keep plain text
    }

    return {
      status: response.status,
      statusText: response.statusText,
      durationMs,
      headers: resHeaders,
      body: formattedBody,
      timestamp: new Date().toLocaleTimeString(),
      backendId: resHeaders['x-backend-id'],
      requestId: resHeaders['x-request-id'],
      rateLimitLimit: resHeaders['x-ratelimit-limit'],
      rateLimitRemaining: resHeaders['x-ratelimit-remaining'],
      rateLimitReset: resHeaders['x-ratelimit-reset'],
      rateLimitDegraded: resHeaders['x-ratelimit-degraded'] === 'true',
      retryAfter: resHeaders['retry-after'],
      isSuccess: response.ok,
    }
  } catch (err: unknown) {
    const durationMs = Math.round(performance.now() - startTime)
    const errorMsg = err instanceof Error ? err.message : 'Network error'
    return {
      status: 0,
      statusText: 'Client Error',
      durationMs,
      headers: {},
      body: JSON.stringify({ error: 'Request failed to reach target', details: errorMsg, targetUrl: target.effectiveUrl }, null, 2),
      timestamp: new Date().toLocaleTimeString(),
      isSuccess: false,
    }
  }
}

/**
 * Fetches recent request logs from the Gateway Request Inspector (/api/requests).
 */
export async function fetchRequestLogs(
  apiKey: string,
  options?: {
    limit?: number
    status?: number
    method?: string
    search?: string
  }
): Promise<RequestLogEntry[]> {
  const params = new URLSearchParams()
  if (options?.limit) params.set('limit', String(options.limit))
  if (options?.status) params.set('status', String(options.status))
  if (options?.method) params.set('method', options.method)
  if (options?.search) params.set('search', options.search)

  const queryStr = params.toString() ? `?${params.toString()}` : ''
  const response = await sessionFetch(`${API_BASE}/api/requests${queryStr}`, {
    method: 'GET',
    credentials: 'include',
    headers: {
      ...(apiKey && apiKey !== '__session__' ? { 'X-API-Key': apiKey } : {}),
      Accept: 'application/json',
    },
    cache: 'no-store',
    signal: AbortSignal.timeout(6000),
  })

  if (!response.ok) {
    if (response.status === 401) {
      throw new Error('Your session has expired. Please sign in again.')
    }
    throw new Error(`Failed to fetch request logs: HTTP ${response.status}`)
  }

  const data = (await response.json()) as { requests?: RequestLogEntry[] }
  return data.requests || []
}

/**
 * Fetches real-time backend health diagnostics from /api/backends.
 */
export async function fetchBackendDiagnostics(apiKey: string): Promise<BackendDiagnostic[]> {
  const response = await sessionFetch(`${API_BASE}/api/backends`, {
    method: 'GET',
    credentials: 'include',
    headers: {
      ...(apiKey && apiKey !== '__session__' ? { 'X-API-Key': apiKey } : {}),
      Accept: 'application/json',
    },
    cache: 'no-store',
    signal: AbortSignal.timeout(6000),
  })

  if (!response.ok) {
    if (response.status === 401) {
      throw new Error('Your session has expired. Please sign in again.')
    }
    throw new Error(`Failed to fetch backend diagnostics: HTTP ${response.status}`)
  }

  const data = (await response.json()) as { backends?: BackendDiagnostic[] }
  return data.backends || []
}

async function managementRequest<T>(apiKey: string, path: string, init: RequestInit = {}): Promise<T> {
  const response = await sessionFetch(`${API_BASE}${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      ...(apiKey && apiKey !== '__session__' ? { 'X-API-Key': apiKey } : {}),
      Accept: 'application/json',
      ...(init.body ? { 'Content-Type': 'application/json' } : {}),
      ...init.headers,
    },
    cache: 'no-store',
    signal: AbortSignal.timeout(8000),
  })
  if (!response.ok) {
    const message = response.status === 401
      ? 'Your session has expired. Please sign in again.'
      : response.status === 403 ? 'Your role does not allow this action.' : `Management request failed: HTTP ${response.status}`
    throw new Error(message)
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export interface DashboardPrincipal {
  id: string
  email: string
  role: 'admin' | 'developer' | 'viewer'
}

export interface DashboardUser extends DashboardPrincipal { createdAt: string }

export async function fetchDashboardUsers(apiKey: string): Promise<DashboardUser[]> {
  const data = await managementRequest<{ users?: DashboardUser[] }>(apiKey, '/api/users')
  return data.users || []
}

export async function createDashboardUser(apiKey: string, input: { email: string; password: string; role: DashboardPrincipal['role'] }): Promise<DashboardUser> {
  return managementRequest<DashboardUser>(apiKey, '/api/users', { method: 'POST', body: JSON.stringify(input) })
}

export async function updateDashboardUserRole(apiKey: string, id: string, role: DashboardPrincipal['role']): Promise<DashboardUser> {
  return managementRequest<DashboardUser>(apiKey, `/api/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ role }) })
}

export async function deleteDashboardUser(apiKey: string, id: string): Promise<void> {
  await managementRequest<void>(apiKey, `/api/users/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function fetchDashboardPrincipal(): Promise<DashboardPrincipal | null> {
  const response = await fetch(`${API_BASE}/api/auth/me`, { credentials: 'include', cache: 'no-store' })
  if (response.status === 401) return null
  if (!response.ok) throw new Error(`Dashboard authentication failed: HTTP ${response.status}`)
  return (await response.json()) as DashboardPrincipal
}

export async function dashboardSetupRequired(): Promise<boolean> {
  const response = await fetch(`${API_BASE}/api/auth/setup`, { cache: 'no-store' })
  if (!response.ok) throw new Error(`Dashboard setup check failed: HTTP ${response.status}`)
  const data = (await response.json()) as { setupRequired?: boolean }
  return data.setupRequired === true
}

export async function loginDashboard(email: string, password: string): Promise<DashboardPrincipal> {
  return authenticateDashboard('/api/auth/login', { email, password })
}

export async function setupDashboard(bootstrapKey: string, email: string, password: string): Promise<DashboardPrincipal> {
  return authenticateDashboard('/api/auth/setup', { email, password }, bootstrapKey)
}

export async function registerDashboard(email: string, password: string): Promise<DashboardPrincipal> {
  return authenticateDashboard('/api/auth/register', { email, password })
}

async function authenticateDashboard(path: string, credentials: { email: string; password: string }, bootstrapKey?: string): Promise<DashboardPrincipal> {
  const response = await fetch(`${API_BASE}${path}`, {
    method: 'POST', credentials: 'include',
    headers: { 'Content-Type': 'application/json', ...(bootstrapKey ? { 'X-API-Key': bootstrapKey } : {}) },
    body: JSON.stringify(credentials), cache: 'no-store',
  })
  if (!response.ok) throw new Error(response.status === 401 ? 'Invalid credentials or bootstrap key' : `Dashboard authentication failed: HTTP ${response.status}`)
  return (await response.json()) as DashboardPrincipal
}

export async function logoutDashboard(): Promise<void> {
  const response = await fetch(`${API_BASE}/api/auth/logout`, { method: 'POST', credentials: 'include' })
  if (!response.ok && response.status !== 204) throw new Error(`Dashboard logout failed: HTTP ${response.status}`)
}

export async function fetchApplications(apiKey: string): Promise<ApplicationRecord[]> {
  const data = await managementRequest<{ applications?: ApplicationRecord[] }>(apiKey, '/api/applications')
  return data.applications || []
}

export async function createApplication(
  apiKey: string,
  input: Pick<ApplicationRecord, 'name' | 'description'>
): Promise<ApplicationRecord> {
  return managementRequest<ApplicationRecord>(apiKey, '/api/applications', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export async function fetchAPIKeys(apiKey: string): Promise<APIKeyRecord[]> {
  const data = await managementRequest<{ keys?: APIKeyRecord[] }>(apiKey, '/api/keys')
  return data.keys || []
}

export async function createAPIKey(
  apiKey: string,
  input: {
    applicationId: string
    name: string
    permissions: string[]
    requestsPerWindow: number
    windowSeconds: number
    burst: number
    expiresAt?: string
  }
): Promise<APIKeyRecord> {
  return managementRequest<APIKeyRecord>(apiKey, '/api/keys', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export async function revokeAPIKey(apiKey: string, id: string): Promise<void> {
  await managementRequest<void>(apiKey, `/api/keys/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function updateAPIKeyPolicy(
  apiKey: string,
  id: string,
  input: {
    permissions: string[]
    requestsPerWindow: number
    windowSeconds: number
    burst: number
    expiresAt?: string
  }
): Promise<void> {
  await managementRequest<void>(apiKey, `/api/keys/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(input),
  })
}

export async function fetchLoadBalancingSettings(apiKey: string): Promise<LoadBalancingSettings> {
  return managementRequest<LoadBalancingSettings>(apiKey, '/api/settings/load-balancing')
}

export async function updateLoadBalancingSettings(apiKey: string, settings: LoadBalancingSettings): Promise<LoadBalancingSettings> {
  return managementRequest<LoadBalancingSettings>(apiKey, '/api/settings/load-balancing', {
    method: 'PUT',
    body: JSON.stringify(settings),
  })
}

export async function fetchAuditLogs(apiKey: string, limit = 100): Promise<AuditLogRecord[]> {
  const data = await managementRequest<{ events?: AuditLogRecord[] }>(apiKey, `/api/audit-logs?limit=${limit}`)
  return data.events || []
}

export async function fetchManagedBackends(apiKey: string): Promise<ManagedBackend[]> {
  const data = await managementRequest<{ backends?: ManagedBackend[] }>(apiKey, '/api/backends')
  return data.backends || []
}

export async function fetchBackendHealthHistory(apiKey: string, backendId: string, limit = 50): Promise<BackendHealthEvent[]> {
  const data = await managementRequest<{ events?: BackendHealthEvent[] }>(
    apiKey,
    `/api/backend-health-history?backendId=${encodeURIComponent(backendId)}&limit=${limit}`
  )
  return data.events || []
}

export async function createManagedBackend(apiKey: string, backend: Omit<ManagedBackend, 'configId' | 'id' | 'healthStatus'>): Promise<ManagedBackend> {
  return managementRequest<ManagedBackend>(apiKey, '/api/backends', { method: 'POST', body: JSON.stringify(backend) })
}

export async function updateManagedBackend(apiKey: string, backend: ManagedBackend): Promise<ManagedBackend> {
  return managementRequest<ManagedBackend>(apiKey, `/api/backends/${encodeURIComponent(backend.configId)}`, {
    method: 'PUT',
    body: JSON.stringify({
      name: backend.name,
      url: backend.url,
      weight: backend.weight,
      enabled: backend.enabled,
      maintenance: backend.maintenance,
      healthPath: backend.healthPath,
    }),
  })
}

export async function deleteManagedBackend(apiKey: string, id: string): Promise<void> {
  await managementRequest<void>(apiKey, `/api/backends/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export async function fetchManagedRoutes(apiKey: string): Promise<ManagedRoute[]> {
  const data = await managementRequest<{ routes?: ManagedRoute[] }>(apiKey, '/api/routes')
  return data.routes || []
}

export async function createManagedRoute(apiKey: string, route: Pick<ManagedRoute, 'name' | 'pathPrefix' | 'methods' | 'backendId' | 'authRequired' | 'enabled' | 'requestsPerWindow' | 'windowSeconds' | 'burst'>): Promise<ManagedRoute> {
  return managementRequest<ManagedRoute>(apiKey, '/api/routes', { method: 'POST', body: JSON.stringify(route) })
}

export async function updateManagedRoute(apiKey: string, route: ManagedRoute): Promise<ManagedRoute> {
  return managementRequest<ManagedRoute>(apiKey, `/api/routes/${encodeURIComponent(route.id)}`, {
    method: 'PUT',
    body: JSON.stringify({
      name: route.name,
      pathPrefix: route.pathPrefix,
      methods: route.methods,
      backendId: route.backendId,
      authRequired: route.authRequired,
      enabled: route.enabled,
      requestsPerWindow: route.requestsPerWindow ?? 0,
      windowSeconds: route.windowSeconds ?? 0,
      burst: route.burst ?? 0,
    }),
  })
}

export async function deleteManagedRoute(apiKey: string, id: string): Promise<void> {
  await managementRequest<void>(apiKey, `/api/routes/${encodeURIComponent(id)}`, { method: 'DELETE' })
}
