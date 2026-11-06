export type TabType =
  | 'overview'
  | 'backends'
  | 'ratelimit'
  | 'metrics'
  | 'playground'
  | 'inspector'
  | 'keys'
  | 'audit'
  | 'routes'
  | 'settings'

export type CircuitBreakerState = 'closed' | 'open' | 'half-open' | 'unknown'

export type BackendHealthState = 'healthy' | 'unhealthy' | 'recovering' | 'unknown'

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
  healthStatus?: BackendHealthState
  consecutiveFailures?: number
  consecutiveSuccesses?: number
  lastCheckTime?: string
  lastError?: string
  weight?: number
  activeRequests?: number
  strategy?: string
}

export interface BackendDiagnostic {
  id: string
  url: string
  healthStatus: BackendHealthState
  circuitBreakerState: CircuitBreakerState
  consecutiveFailures: number
  consecutiveSuccesses: number
  weight?: number
  activeRequests?: number
  strategy?: string
  lastCheckTime?: string
  lastError?: string
}

export interface ManagedBackend {
  configId: string
  id: string
  name: string
  url: string
  weight: number
  enabled: boolean
  maintenance: boolean
  healthPath: string
  healthStatus?: string
}

export interface ManagedRoute {
  id: string
  name: string
  pathPrefix: string
  methods: string[]
  backendId: string
  backendName: string
  authRequired: boolean
  enabled: boolean
  requestsPerWindow?: number
  windowSeconds?: number
  burst?: number
  createdAt: string
  updatedAt: string
}

export interface RequestLogEntry {
  id: string
  method: string
  path: string
  status: number
  durationMs: number
  backend: string
  timestamp: string
  failureCategory?: string
}

export interface ApplicationRecord {
  id: string
  name: string
  description: string
  createdAt: string
}

export interface APIKeyRecord {
  id: string
  applicationId: string
  applicationName: string
  name: string
  prefix: string
  permissions: string[]
  requestsPerWindow: number
  windowSeconds: number
  burst: number
  expiresAt?: string
  revokedAt?: string
  createdAt: string
  lastUsedAt?: string
  usageCount: number
  secret?: string
}

export type LoadBalancingStrategy = 'round_robin' | 'weighted_round_robin' | 'least_connections' | 'random'

export interface LoadBalancingSettings {
  strategy: LoadBalancingStrategy
  weights: Record<string, number>
}

export interface AuditLogRecord {
  id: number
  actor: string
  action: string
  resourceType: string
  resourceId: string
  details: Record<string, unknown>
  createdAt: string
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
  requestId?: string
  backendId?: string
  rateLimitLimit?: string
  rateLimitRemaining?: string
  rateLimitReset?: string
  rateLimitDegraded?: boolean
  retryAfter?: string
  isSuccess: boolean
}
