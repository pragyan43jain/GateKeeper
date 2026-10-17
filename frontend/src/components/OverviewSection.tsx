import React from 'react'
import {
  Activity,
  CheckCircle2,
  Clock,
  Server,
  Terminal,
  ShieldAlert,
  ArrowUpRight,
} from 'lucide-react'
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import type {
  GatewayHealth,
  GatewayMetricsSummary,
  TimeSeriesPoint,
  TabType,
} from '../types'

interface OverviewSectionProps {
  health: GatewayHealth | null
  metrics: GatewayMetricsSummary | null
  timeSeries: TimeSeriesPoint[] | null
  onNavigate: (tab: TabType) => void
}

export const OverviewSection: React.FC<OverviewSectionProps> = ({
  health,
  metrics,
  timeSeries,
  onNavigate,
}) => {
  const isHealthy = health?.status === 'healthy'

  const totalRequests = metrics?.totalRequests ?? 0
  const requests200 = metrics?.requestsByCode['200'] ?? 0
  const requests401 = metrics?.requestsByCode['401'] ?? 0
  const requests429 = metrics?.requestsByCode['429'] ?? 0
  const requests5xx = Object.entries(metrics?.requestsByCode ?? {}).reduce(
    (acc, [code, val]) => (code.startsWith('5') ? acc + val : acc),
    0
  )
  const totalErrors = requests401 + requests429 + requests5xx

  const successRate =
    totalRequests > 0 ? ((requests200 / totalRequests) * 100).toFixed(1) : '100.0'

  const avgLatency =
    metrics?.avgLatencyMs !== null && metrics?.avgLatencyMs !== undefined
      ? `${metrics.avgLatencyMs.toFixed(2)} ms`
      : '0.00 ms'

  // Prepare chart data: either Prometheus matrix points or fallback summary
  const chartData = timeSeries && timeSeries.length > 0 ? timeSeries : []

  return (
    <div className="space-y-6">
      {/* KPI Cards Grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Gateway Health */}
        <div className="p-4 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div className="flex items-center justify-between text-zinc-400 text-xs font-medium">
            <span>Gateway Health</span>
            <Activity className={`w-4 h-4 ${isHealthy ? 'text-emerald-400' : 'text-rose-400'}`} />
          </div>
          <div className="mt-3">
            <div className="text-xl font-bold text-zinc-100 flex items-center gap-2">
              <span
                className={`w-2.5 h-2.5 rounded-full ${
                  isHealthy ? 'bg-emerald-400 shadow-[0_0_8px_rgba(52,211,153,0.5)]' : 'bg-rose-400'
                }`}
              />
              {isHealthy ? 'Operational' : 'Degraded'}
            </div>
            <div className="text-xs text-zinc-400 mt-1 truncate">
              {health?.message || 'Checking status...'}
            </div>
          </div>
          <div className="mt-3 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>Route: /health</span>
            <span className="text-zinc-300">200 OK</span>
          </div>
        </div>

        {/* Total Requests */}
        <div className="p-4 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div className="flex items-center justify-between text-zinc-400 text-xs font-medium">
            <span>Total Requests</span>
            <CheckCircle2 className="w-4 h-4 text-indigo-400" />
          </div>
          <div className="mt-3">
            <div className="text-2xl font-bold text-zinc-100 font-mono">
              {totalRequests.toLocaleString()}
            </div>
            <div className="text-xs text-zinc-400 mt-1">
              Success rate: <span className="text-emerald-400 font-medium font-mono">{successRate}%</span>
            </div>
          </div>
          <div className="mt-3 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>200 OK: {requests200.toLocaleString()}</span>
            <span>Errors (401/429/5xx): {totalErrors.toLocaleString()}</span>
          </div>
        </div>

        {/* Average Latency */}
        <div className="p-4 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div className="flex items-center justify-between text-zinc-400 text-xs font-medium">
            <span>Average Latency</span>
            <Clock className="w-4 h-4 text-cyan-400" />
          </div>
          <div className="mt-3">
            <div className="text-2xl font-bold text-zinc-100 font-mono">
              {avgLatency}
            </div>
            <div className="text-xs text-zinc-400 mt-1">
              Gateway duration histogram
            </div>
          </div>
          <div className="mt-3 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>Count: {metrics?.totalDurationCount.toLocaleString() ?? 0}</span>
            <span className="text-zinc-300">&lt; 10ms typical</span>
          </div>
        </div>

        {/* Rate Limit Rejections */}
        <div className="p-4 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div className="flex items-center justify-between text-zinc-400 text-xs font-medium">
            <span>Rate Limit Violations</span>
            <ShieldAlert className="w-4 h-4 text-amber-400" />
          </div>
          <div className="mt-3">
            <div className="text-2xl font-bold text-zinc-100 font-mono">
              {(metrics?.rateLimitRejections ?? 0).toLocaleString()}
            </div>
            <div className="text-xs text-zinc-400 mt-1">
              Redis sliding-window 429s
            </div>
          </div>
          <div className="mt-3 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>Limit: 10 req / 60s</span>
            <span className="text-amber-400">Strict Lua</span>
          </div>
        </div>
      </div>

      {/* Main Traffic Chart */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 mb-4">
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
                Request Rate & Traffic (req/s)
              </h2>
              {chartData.length > 0 && (
                <span
                  className={`text-[10px] font-mono px-2 py-0.5 rounded border ${
                    chartData[0]?.isFallback
                      ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                      : 'bg-indigo-500/10 text-indigo-400 border-indigo-500/20'
                  }`}
                >
                  {chartData[0]?.isFallback
                    ? 'Fallback: Client Polling Rate'
                    : 'Source: Prometheus rate[1m]'}
                </span>
              )}
            </div>
            <p className="text-xs text-zinc-400 mt-0.5">
              Live request rate in requests per second.
            </p>
          </div>
          <div className="flex items-center gap-4 text-xs font-mono">
            <span className="flex items-center gap-1.5 text-zinc-300">
              <span className="w-2.5 h-2.5 rounded-sm bg-indigo-500" />
              200 OK (req/s)
            </span>
            <span className="flex items-center gap-1.5 text-zinc-300">
              <span className="w-2.5 h-2.5 rounded-sm bg-rose-500" />
              Errors: 401 / 429 / 5xx (req/s)
            </span>
          </div>
        </div>

        {chartData.length > 0 ? (
          <div className="h-64 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={chartData} margin={{ top: 10, right: 10, left: -20, bottom: 0 }}>
                <defs>
                  <linearGradient id="color200" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#6366f1" stopOpacity={0.3} />
                    <stop offset="95%" stopColor="#6366f1" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="colorErr" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#f43f5e" stopOpacity={0.3} />
                    <stop offset="95%" stopColor="#f43f5e" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="#27272a" vertical={false} />
                <XAxis
                  dataKey="time"
                  stroke="#71717a"
                  fontSize={11}
                  tickLine={false}
                  axisLine={{ stroke: '#27272a' }}
                />
                <YAxis
                  stroke="#71717a"
                  fontSize={11}
                  tickLine={false}
                  axisLine={{ stroke: '#27272a' }}
                />
                <Tooltip
                  contentStyle={{
                    backgroundColor: '#18181b',
                    borderColor: '#27272a',
                    borderRadius: '8px',
                    color: '#f4f4f5',
                    fontSize: '12px',
                    fontFamily: 'monospace',
                  }}
                  formatter={(val: any, name: any) => [`${val} req/s`, name]}
                />
                <Area
                  type="monotone"
                  dataKey="requests200"
                  name="200 OK"
                  stroke="#6366f1"
                  strokeWidth={2}
                  fillOpacity={1}
                  fill="url(#color200)"
                />
                <Area
                  type="monotone"
                  dataKey="requestsError"
                  name="Errors (401 / 429 / 5xx)"
                  stroke="#f43f5e"
                  strokeWidth={2}
                  fillOpacity={1}
                  fill="url(#colorErr)"
                />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        ) : (
          <div className="h-64 w-full flex flex-col items-center justify-center border border-dashed border-zinc-800 rounded-lg text-center p-6">
            <Activity className="w-8 h-8 text-zinc-400 mb-2" />
            <div className="text-sm font-medium text-zinc-300">
              Awaiting Prometheus time-series accumulation
            </div>
            <p className="text-xs text-zinc-400 mt-1 max-w-sm">
              Current total processed: {totalRequests.toLocaleString()} requests. Run requests in the
              API Playground to stream real-time throughput.
            </p>
            <button
              onClick={() => onNavigate('playground')}
              className="mt-4 px-3 py-1.5 rounded-md bg-indigo-600 hover:bg-indigo-500 text-xs font-medium text-white transition-colors flex items-center gap-1.5"
            >
              <Terminal className="w-3.5 h-3.5" />
              Open API Playground
            </button>
          </div>
        )}
      </div>

      {/* Two Column Section: Upstream Nodes & Quick Navigation */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Upstream Backend Status Summary */}
        <div className="lg:col-span-2 p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
          <div className="flex items-center justify-between mb-4">
            <div>
              <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
                <Server className="w-4 h-4 text-indigo-400" />
                Upstream Backend Cluster
              </h2>
              <p className="text-xs text-zinc-400">
                Round-robin pool with isolated per-backend circuit breakers.
              </p>
            </div>
            <button
              onClick={() => onNavigate('backends')}
              className="text-xs text-indigo-400 hover:text-indigo-300 flex items-center gap-1 font-medium transition-colors"
            >
              View Backends <ArrowUpRight className="w-3 h-3" />
            </button>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {metrics?.backends.map((b) => {
              const isClosed = b.state === 'closed'
              const isHalf = b.state === 'half-open'

              return (
                <div
                  key={b.id}
                  className="p-3.5 rounded-lg bg-zinc-950 border border-zinc-800 flex flex-col justify-between"
                >
                  <div className="flex items-center justify-between">
                    <span className="font-mono text-xs font-bold text-zinc-200">{b.displayName}</span>
                    <span
                      className={`text-[10px] uppercase font-mono px-2 py-0.5 rounded font-semibold border ${
                        isClosed
                          ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                          : isHalf
                          ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                          : 'bg-rose-500/10 text-rose-400 border-rose-500/20'
                      }`}
                    >
                      {b.state}
                    </span>
                  </div>
                  <div className="mt-2 text-[11px] text-zinc-400 font-mono truncate">
                    {b.target}
                  </div>
                  <div className="mt-3 pt-2.5 border-t border-zinc-800/80 flex items-center justify-between text-[11px] font-mono">
                    <span className="text-zinc-400">Trips Total:</span>
                    <span className="text-zinc-200 font-semibold">{b.trips}</span>
                  </div>
                </div>
              )
            })}
          </div>
        </div>

        {/* Status Code Breakdown Card */}
        <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div>
            <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2 mb-1">
              Response Codes
            </h2>
            <p className="text-xs text-zinc-400 mb-4">
              Distribution of HTTP status codes across all requests.
            </p>

            <div className="space-y-2.5">
              <div className="flex items-center justify-between text-xs font-mono">
                <span className="text-emerald-400 flex items-center gap-1.5">
                  <span className="w-2 h-2 rounded-full bg-emerald-400" />
                  200 OK
                </span>
                <span className="text-zinc-200 font-bold">{requests200.toLocaleString()}</span>
              </div>
              <div className="flex items-center justify-between text-xs font-mono">
                <span className="text-zinc-400 flex items-center gap-1.5">
                  <span className="w-2 h-2 rounded-full bg-zinc-400" />
                  401 Unauthorized
                </span>
                <span className="text-zinc-200 font-bold">{requests401.toLocaleString()}</span>
              </div>
              <div className="flex items-center justify-between text-xs font-mono">
                <span className="text-amber-400 flex items-center gap-1.5">
                  <span className="w-2 h-2 rounded-full bg-amber-400" />
                  429 Too Many Requests
                </span>
                <span className="text-zinc-200 font-bold">{requests429.toLocaleString()}</span>
              </div>
              <div className="flex items-center justify-between text-xs font-mono">
                <span className="text-rose-400 flex items-center gap-1.5">
                  <span className="w-2 h-2 rounded-full bg-rose-400" />
                  502 / 503 Errors
                </span>
                <span className="text-zinc-200 font-bold">{requests5xx.toLocaleString()}</span>
              </div>
            </div>
          </div>

          <div className="mt-4 pt-3 border-t border-zinc-800 text-xs text-zinc-400 flex items-center justify-between">
            <span>Failover Policy:</span>
            <span className="text-indigo-400 font-medium font-mono">Circuit-Aware</span>
          </div>
        </div>
      </div>
    </div>
  )
}
