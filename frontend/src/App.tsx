import { useState, useEffect, useCallback, useRef } from 'react'
import { Sidebar } from './components/Sidebar'
import { Header } from './components/Header'
import { OverviewSection } from './components/OverviewSection'
import { BackendsSection } from './components/BackendsSection'
import { RateLimitingSection } from './components/RateLimitingSection'
import { MetricsSection } from './components/MetricsSection'
import { PlaygroundSection } from './components/PlaygroundSection'
import { InspectorSection } from './components/InspectorSection'
import { APIKeysSection } from './components/APIKeysSection'
import { TrafficPoliciesSection } from './components/TrafficPoliciesSection'
import { AuditLogsSection } from './components/AuditLogsSection'
import { RoutesSection } from './components/RoutesSection'
import { AuthScreen } from './components/AuthScreen'
import { SettingsSection } from './components/SettingsSection'
import {
  fetchGatewayHealth,
  fetchGatewayMetrics,
  fetchPrometheusTimeSeries,
  fetchBackendDiagnostics,
  fetchDashboardPrincipal,
  logoutDashboard,
  SESSION_EXPIRED_EVENT,
} from './services/api'
import type { DashboardPrincipal } from './services/api'
import type {
  TabType,
  GatewayHealth,
  GatewayMetricsSummary,
  TimeSeriesPoint,
  PlaygroundResponseData,
  BackendDiagnostic,
} from './types'

export function App() {
  const [currentTab, setCurrentTab] = useState<TabType>('overview')
  const [principal, setPrincipal] = useState<DashboardPrincipal | null>(null)
  const [authChecked, setAuthChecked] = useState(false)
  const [sessionError, setSessionError] = useState('')
  const [authNotice, setAuthNotice] = useState('')
  const [loggingOut, setLoggingOut] = useState(false)
  const sessionGeneration = useRef(0)
  const managementKey = principal ? '__session__' : ''

  // Telemetry state
  const [health, setHealth] = useState<GatewayHealth | null>(null)
  const [metrics, setMetrics] = useState<GatewayMetricsSummary | null>(null)
  const [timeSeries, setTimeSeries] = useState<TimeSeriesPoint[] | null>(null)
  const [lastScraped, setLastScraped] = useState<string>('')
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(true)

  // API Playground state (IN-MEMORY ONLY)
  const [apiKey, setApiKey] = useState<string>('')

  useEffect(() => {
    fetchDashboardPrincipal().then(setPrincipal).catch((err: unknown) => {
      console.error('Dashboard authentication check failed:', err)
    }).finally(() => setAuthChecked(true))
  }, [])
  const [lastPlaygroundResponse, setLastPlaygroundResponse] =
    useState<PlaygroundResponseData | null>(null)
  const [responseHistory, setResponseHistory] = useState<PlaygroundResponseData[]>([])

  // Rolling client-side time-series buffer in case Prometheus proxy is not ready
  const rollingBuffer = useRef<TimeSeriesPoint[]>([])
  const lastScrapeSnapshot = useRef<{
    timestamp: number
    total: number
    c200: number
    c401: number
    c429: number
    c5xx: number
  } | null>(null)

  const refreshInFlight = useRef(false)
  const loadData = useCallback(async () => {
    if (!managementKey || refreshInFlight.current) return
    const generation = sessionGeneration.current
    refreshInFlight.current = true
    setIsRefreshing(true)
    try {
      const [h, m, ts] = await Promise.all([
        fetchGatewayHealth(),
        fetchGatewayMetrics().catch((err) => {
          console.error('Error fetching metrics:', err)
          return null
        }),
        fetchPrometheusTimeSeries(15).catch(() => null),
      ])

      if (generation !== sessionGeneration.current) return
      setHealth(h)
      if (m) {
        if (managementKey) {
          try {
            const diags: BackendDiagnostic[] = await fetchBackendDiagnostics(managementKey)
            if (diags && diags.length > 0) {
              const diagMap = new Map(diags.map((d) => [d.id, d]))
              m.backends = m.backends.map((b) => {
                const d = diagMap.get(b.id)
                if (d) {
                  return {
                    ...b,
                    healthStatus: d.healthStatus,
                    consecutiveFailures: d.consecutiveFailures,
                    consecutiveSuccesses: d.consecutiveSuccesses,
                    lastCheckTime: d.lastCheckTime,
                    lastError: d.lastError,
                    weight: d.weight,
                    activeRequests: d.activeRequests,
                    strategy: d.strategy,
                  }
                }
                return b
              })
            }
          } catch {
            // Ignore diagnostic fetch error; basic status from Prometheus remains
          }
        }
        if (generation !== sessionGeneration.current) return
        setMetrics(m)
        setLastScraped(m.scrapedAt)

        // If Prometheus time-series is available, use it; otherwise compute delta rates in fallback buffer
        if (ts && ts.length > 0) {
          setTimeSeries(ts)
        } else {
          const nowMs = Date.now()
          const nowSec = Math.floor(nowMs / 1000)
          const c200 = m.requestsByCode['200'] || 0
          const c401 = m.requestsByCode['401'] || 0
          const c429 = m.requestsByCode['429'] || 0
          const c5xx = Object.entries(m.requestsByCode).reduce(
            (acc, [code, val]) => (code.startsWith('5') ? acc + val : acc),
            0
          )
          const total = m.totalRequests

          let rate200 = 0
          let rate401 = 0
          let rate429 = 0
          let rate5xx = 0
          let rateError = 0
          let totalRate = 0

          if (lastScrapeSnapshot.current) {
            const dt = Math.max(1, nowSec - lastScrapeSnapshot.current.timestamp)
            rate200 = Math.max(0, Math.round(((c200 - lastScrapeSnapshot.current.c200) / dt) * 1000) / 1000)
            rate401 = Math.max(0, Math.round(((c401 - lastScrapeSnapshot.current.c401) / dt) * 1000) / 1000)
            rate429 = Math.max(0, Math.round(((c429 - lastScrapeSnapshot.current.c429) / dt) * 1000) / 1000)
            rate5xx = Math.max(0, Math.round(((c5xx - lastScrapeSnapshot.current.c5xx) / dt) * 1000) / 1000)
            rateError = rate401 + rate429 + rate5xx
            totalRate = Math.max(0, Math.round(((total - lastScrapeSnapshot.current.total) / dt) * 1000) / 1000)
          }

          lastScrapeSnapshot.current = { timestamp: nowSec, total, c200, c401, c429, c5xx }

          const newPoint: TimeSeriesPoint = {
            time: new Date(nowMs).toLocaleTimeString([], {
              hour: '2-digit',
              minute: '2-digit',
              second: '2-digit',
            }),
            timestamp: nowSec,
            rate200,
            rate401,
            rate429,
            rate5xx,
            rateError,
            totalRate,
            requests200: rate200,
            requests401: rate401,
            requests429: rate429,
            requests5xx: rate5xx,
            requestsError: rateError,
            total: totalRate,
            isFallback: true,
          }

          rollingBuffer.current = [...rollingBuffer.current.slice(-20), newPoint]
          setTimeSeries([...rollingBuffer.current])
        }
      } else {
        setMetrics(null)
        setTimeSeries(null)
      }
    } finally {
      refreshInFlight.current = false
      setIsRefreshing(false)
    }
  }, [managementKey])

  // Initial load
  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect
    loadData()
  }, [loadData])

  // Auto-refresh interval (5s)
  useEffect(() => {
    if (!autoRefresh || !principal) return
    const interval = setInterval(() => {
      loadData()
    }, 5000)
    return () => clearInterval(interval)
  }, [autoRefresh, loadData, principal])

  // Handle new response received from API Playground
  const handlePlaygroundResponse = (res: PlaygroundResponseData) => {
    setLastPlaygroundResponse(res)
    setResponseHistory((prev) => [res, ...prev.slice(0, 19)])
    // Trigger immediate telemetry refresh so counters update
    loadData()
  }

  const clearSession = useCallback((notice: string) => {
    sessionGeneration.current += 1
    setPrincipal(null); setApiKey(''); setMetrics(null); setHealth(null)
    setTimeSeries(null); setLastScraped(''); setLastPlaygroundResponse(null)
    setResponseHistory([]); setSessionError(''); setCurrentTab('overview')
    rollingBuffer.current = []; lastScrapeSnapshot.current = null
    setAuthNotice(notice)
  }, [])
  useEffect(() => {
    const expire = () => clearSession('Your session has expired. Please sign in again.')
    window.addEventListener(SESSION_EXPIRED_EVENT, expire)
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, expire)
  }, [clearSession])
  async function handleLogout() {
    if (loggingOut) return
    setLoggingOut(true)
    try { await logoutDashboard(); clearSession('You have been signed out securely.') }
    catch { setSessionError('Sign out failed. Your session is still active; please try again.') }
    finally { setLoggingOut(false) }
  }

  if (!authChecked) return <div className="flex min-h-screen items-center justify-center bg-zinc-950 text-sm text-zinc-400">Checking dashboard session…</div>
  if (!principal) return <AuthScreen notice={authNotice} onAuthenticated={user => { setAuthNotice(''); setPrincipal(user) }} />

  return (
    <div className="console-app flex h-screen w-screen overflow-hidden bg-zinc-950 text-zinc-100 antialiased font-sans">
      {/* Sidebar Navigation */}
      <Sidebar
        currentTab={currentTab}
        onSelectTab={setCurrentTab}
        health={health}
        principal={principal}
        onLogout={() => void handleLogout()}
      />

      {/* Main Content Area */}
      <div className="flex-1 flex flex-col h-full overflow-hidden min-w-0">
        <Header
          principal={principal}
          onSettings={() => setCurrentTab('settings')}
          onLogout={() => void handleLogout()}
          loggingOut={loggingOut}
          currentTab={currentTab}
          health={health}
          lastScraped={lastScraped}
          isRefreshing={isRefreshing}
          autoRefresh={autoRefresh}
          onToggleAutoRefresh={() => setAutoRefresh(!autoRefresh)}
          onManualRefresh={loadData}
        />

        <main key={currentTab} className="flex-1 overflow-y-auto p-6 min-w-0">
          <div className="content-container max-w-7xl mx-auto space-y-6">
            {!metrics && <p role="status" className="telemetry-notice">{isRefreshing ? 'Loading gateway telemetry…' : 'Telemetry unavailable. Refresh to reconnect; no estimated metrics are displayed.'}</p>}
            {sessionError && <p role="alert" className="text-sm text-rose-400">{sessionError}</p>}
            {currentTab === 'overview' && (
              <OverviewSection
                health={health}
                metrics={metrics}
                timeSeries={timeSeries}
                onNavigate={setCurrentTab}
              />
            )}

            {currentTab === 'backends' && (
              <BackendsSection
                backends={metrics?.backends || []}
                lastScraped={lastScraped}
                apiKey={managementKey}
                onApiKeyChange={() => {}}
              />
            )}

            {currentTab === 'ratelimit' && (
              <div className="space-y-6">
                <RateLimitingSection
                  rateLimitRejections={metrics?.rateLimitRejections || 0}
                  lastObservedRemaining={lastPlaygroundResponse?.rateLimitRemaining || null}
                  lastObservedLimit={lastPlaygroundResponse?.rateLimitLimit || null}
                  lastObservedRetryAfter={lastPlaygroundResponse?.retryAfter || null}
                  lastObservedReset={lastPlaygroundResponse?.rateLimitReset || null}
                  lastObservedDegraded={lastPlaygroundResponse?.rateLimitDegraded}
                  onNavigate={setCurrentTab}
                />
                <TrafficPoliciesSection apiKey={managementKey} onApiKeyChange={() => {}} />
              </div>
            )}

            {currentTab === 'metrics' && (
              <MetricsSection
                metrics={metrics}
                timeSeries={timeSeries}
                lastScraped={lastScraped}
              />
            )}

            {currentTab === 'playground' && (
              <PlaygroundSection
                onResponseReceived={handlePlaygroundResponse}
                lastResponse={lastPlaygroundResponse}
                responseHistory={responseHistory}
                apiKey={apiKey}
                onChangeApiKey={setApiKey}
              />
            )}

            {currentTab === 'inspector' && (
              <InspectorSection
                apiKey={managementKey}
                onApiKeyChange={() => {}}
              />
            )}

            {currentTab === 'keys' && (
              <APIKeysSection apiKey={managementKey} onApiKeyChange={() => {}} />
            )}

            {currentTab === 'audit' && (
              <AuditLogsSection apiKey={managementKey} />
            )}

            {currentTab === 'routes' && (
              <RoutesSection apiKey={managementKey} />
            )}

            {currentTab === 'settings' && (
              <SettingsSection apiKey={managementKey} principal={principal} />
            )}
          </div>
        </main>
      </div>
    </div>
  )
}

export default App
