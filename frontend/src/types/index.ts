export type TabType = 'overview' | 'backends' | 'ratelimit' | 'metrics' | 'playground'

export type CircuitBreakerState = 'closed' | 'open' | 'half-open' | 'unknown'

export interface GatewayHealth {
  status: 'healthy' | 'unhealthy' | 'checking'
  message: string
  checkedAt?: string
  statusCode?: number
}

export interface BackendNodeStatus {
  id: string
  displayName: string
  target: string
  state: CircuitBreakerState
  trips: number
  lastActive?: string
}

export interface MetricSample {
  name: string
  help?: string
  type?: string
  labels: Record<string, string>
  value: number
}

export interface GatewayMetricsSummary {
  totalRequests: number
  requestsByCode: Record<string, number>
  rateLimitRejections: number
  avgLatencyMs: number | null
  totalDurationSum: number
  totalDurationCount: number
  backends: BackendNodeStatus[]
  rawSamples: MetricSample[]
  scrapedAt: string
}

export interface TimeSeriesPoint {
  time: string
  timestamp: number
  rate200: number
  rate401: number
  rate429: number
  rate5xx: number
  rateError: number
  totalRate: number
  requests200: number
  requests401?: number
  requests429?: number
  requests5xx?: number
  requestsError: number
  total: number
  isFallback?: boolean
}

export interface NormalizedTarget {
  effectiveUrl: string
  normalized: boolean
  originalUrl: string
  isExternal: boolean
  warning?: string
}

export interface PlaygroundResponseData {
  status: number
  statusText: string
  durationMs: number
  headers: Record<string, string>
  body: string
  timestamp: string
  backendId?: string
  rateLimitLimit?: string
  rateLimitRemaining?: string
  retryAfter?: string
  isSuccess: boolean
}

