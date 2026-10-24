import type {
  GatewayHealth,
  GatewayMetricsSummary,
  BackendNodeStatus,
  MetricSample,
  CircuitBreakerState,
  TimeSeriesPoint,
  PlaygroundResponseData,
  NormalizedTarget,
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
      type: typeMap.get(name),
      labels,
      value,
    })
  }

  return samples
}

const rawApiUrl = (import.meta as unknown as { env?: { VITE_API_URL?: string } }).env?.VITE_API_URL
export const API_BASE = typeof rawApiUrl === 'string' ? rawApiUrl.trim().replace(/\/+$/, '') : ''

/**
 * Checks the gateway public /health endpoint.
 */
export async function fetchGatewayHealth(): Promise<GatewayHealth> {
  const startTime = Date.now()
  try {
    const response = await fetch(`${API_BASE}/health`, {
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
 * Fetches and aggregates Gatekeeper Prometheus metrics from /metrics.
 */
export async function fetchGatewayMetrics(): Promise<GatewayMetricsSummary> {
  const response = await fetch(`${API_BASE}/metrics`, {
    method: 'GET',
    cache: 'no-store',
  })

  if (!response.ok) {
    throw new Error(`Failed to fetch /metrics: ${response.status} ${response.statusText}`)
  }

  const text = await response.text()
  const samples = parsePrometheusText(text)

  let totalRequests = 0
  const requestsByCode: Record<string, number> = {}
  let rateLimitRejections = 0
  let durationSum = 0
  let durationCount = 0

  const backendStates: Record<string, CircuitBreakerState> = {
    'backend-1:8081': 'closed',
    'backend-2:8081': 'closed',
  }
  const backendTrips: Record<string, number> = {
    'backend-1:8081': 0,
    'backend-2:8081': 0,
  }

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
    }
  }

  const avgLatencyMs = durationCount > 0 ? (durationSum / durationCount) * 1000 : null

  // Format backend statuses
  const knownBackends: BackendNodeStatus[] = [
    {
      id: 'backend-1:8081',
      displayName: 'backend-1',
      target: 'http://backend-1:8081',
      state: backendStates['backend-1:8081'] || 'closed',
      trips: backendTrips['backend-1:8081'] || 0,
    },
    {
      id: 'backend-2:8081',
      displayName: 'backend-2',
      target: 'http://backend-2:8081',
      state: backendStates['backend-2:8081'] || 'closed',
      trips: backendTrips['backend-2:8081'] || 0,
    },
  ]

  // Add any other backend found dynamically
  for (const [key, state] of Object.entries(backendStates)) {
    if (key !== 'backend-1:8081' && key !== 'backend-2:8081') {
      const cleanName = key.split(':')[0]
      knownBackends.push({
        id: key,
        displayName: cleanName,
        target: `http://${key}`,
        state,
        trips: backendTrips[key] || 0,
      })
    }
  }

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
    const res = await fetch(url, { cache: 'no-store' })
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
      rateLimitLimit: resHeaders['x-ratelimit-limit'],
      rateLimitRemaining: resHeaders['x-ratelimit-remaining'],
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


