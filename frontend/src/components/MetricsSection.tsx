import React, { useState, useMemo } from 'react'
import {
  BarChart3,
  Search,
  Layers,
  Clock,
  Activity,
  Filter,
} from 'lucide-react'
import {
  AreaChart,
  Area,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import type { GatewayMetricsSummary, TimeSeriesPoint } from '../types'

interface MetricsSectionProps {
  metrics: GatewayMetricsSummary | null
  timeSeries: TimeSeriesPoint[] | null
  lastScraped: string
}

export const MetricsSection: React.FC<MetricsSectionProps> = ({
  metrics,
  timeSeries,
  lastScraped,
}) => {
  const [searchTerm, setSearchTerm] = useState('')
  const [selectedType, setSelectedType] = useState<string>('all')

  // Filter raw samples for explorer
  const filteredSamples = useMemo(() => {
    if (!metrics?.rawSamples) return []
    return metrics.rawSamples.filter((sample) => {
      const matchesSearch =
        sample.name.toLowerCase().includes(searchTerm.toLowerCase()) ||
        Object.entries(sample.labels).some(
          ([k, v]) =>
            k.toLowerCase().includes(searchTerm.toLowerCase()) ||
            v.toLowerCase().includes(searchTerm.toLowerCase())
        )
      const matchesType =
        selectedType === 'all' || (sample.type && sample.type.toLowerCase() === selectedType.toLowerCase())
      return matchesSearch && matchesType
    })
  }, [metrics, searchTerm, selectedType])

  // Extract and aggregate latency bucket distribution across all response codes
  const latencyBuckets = useMemo(() => {
    if (!metrics?.rawSamples) return []

    // Helper to format bucket upper bound label
    const formatBucketLabel = (rawLe: string): string => {
      if (rawLe === '+Inf') return '>10s (+Inf)'
      const val = parseFloat(rawLe)
      if (val < 1) {
        return `≤${Math.round(val * 1000)}ms`
      }
      return `≤${val}s`
    }

    // Map by rawLe to aggregate counts across response-code and method labels
    const bucketMap = new Map<string, { numLe: number; count: number; label: string }>()

    for (const s of metrics.rawSamples) {
      if (s.name === 'gateway_request_duration_seconds_bucket' && s.labels.le) {
        const rawLe = s.labels.le
        const numLe = rawLe === '+Inf' ? Infinity : parseFloat(rawLe)
        if (isNaN(numLe)) continue

        const existing = bucketMap.get(rawLe)
        if (existing) {
          existing.count += s.value
        } else {
          bucketMap.set(rawLe, {
            numLe,
            count: s.value,
            label: formatBucketLabel(rawLe),
          })
        }
      }
    }

    // Sort buckets in ascending order of duration bound
    const sorted = Array.from(bucketMap.entries())
      .map(([rawLe, data]) => ({
        key: `bucket-${rawLe}`,
        rawLe,
        le: data.label,
        count: Math.round(data.count),
        numLe: data.numLe,
      }))
      .sort((a, b) => a.numLe - b.numLe)

    return sorted
  }, [metrics])

  return (
    <div className="space-y-6">
      {/* Top Banner */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <BarChart3 className="w-4 h-4 text-indigo-400" />
            Prometheus Telemetry & Metrics Engine
          </h2>
          <p className="text-xs text-zinc-400 mt-0.5">
            Real-time scraping from GateKeeper's <code className="text-zinc-300 font-mono">/metrics</code> endpoint and Prometheus server.
          </p>
        </div>
        <div className="text-xs font-mono text-zinc-400">
          Last scrape: <span className="text-zinc-200">{lastScraped || 'just now'}</span>
        </div>
      </div>

      {/* Time-Series Traffic Chart */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 mb-4">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
                <Activity className="w-4 h-4 text-indigo-400" />
                Request Rate Time-Series (req/s)
              </h3>
              {timeSeries && timeSeries.length > 0 && (
                <span
                  className={`text-[10px] font-mono px-2 py-0.5 rounded border ${
                    timeSeries[0]?.isFallback
                      ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                      : 'bg-indigo-500/10 text-indigo-400 border-indigo-500/20'
                  }`}
                >
                  {timeSeries[0]?.isFallback
                    ? 'Fallback: Client Polling Rate'
                    : 'Source: Prometheus rate[1m]'}
                </span>
              )}
            </div>
            <p className="text-xs text-zinc-400 mt-0.5">
              Live per-second request rate measured across response status codes (req/s).
            </p>
          </div>
          <div className="flex items-center gap-4 text-xs font-mono">
            <span className="flex items-center gap-1.5 text-zinc-300">
              <span className="w-2.5 h-2.5 rounded-sm bg-indigo-500" />
              200 OK (req/s)
            </span>
            <span className="flex items-center gap-1.5 text-zinc-300">
              <span className="w-2.5 h-2.5 rounded-sm bg-amber-500" />
              429 Limited (req/s)
            </span>
            <span className="flex items-center gap-1.5 text-zinc-300">
              <span className="w-2.5 h-2.5 rounded-sm bg-rose-500" />
              5xx Error (req/s)
            </span>
          </div>
        </div>

        {timeSeries && timeSeries.length > 0 ? (
          <div className="h-60 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={timeSeries} margin={{ top: 10, right: 10, left: -15, bottom: 0 }}>
                <defs>
                  <linearGradient id="metric200" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#6366f1" stopOpacity={0.3} />
                    <stop offset="95%" stopColor="#6366f1" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="metric429" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#f59e0b" stopOpacity={0.3} />
                    <stop offset="95%" stopColor="#f59e0b" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="metric5xx" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#f43f5e" stopOpacity={0.3} />
                    <stop offset="95%" stopColor="#f43f5e" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="#27272a" vertical={false} />
                <XAxis dataKey="time" stroke="#71717a" fontSize={10} tickLine={false} />
                <YAxis stroke="#71717a" fontSize={10} tickLine={false} />
                <Tooltip
                  contentStyle={{
                    backgroundColor: '#18181b',
                    borderColor: '#27272a',
                    borderRadius: '8px',
                    color: '#f4f4f5',
                    fontSize: '11px',
                    fontFamily: 'monospace',
                  }}
                  formatter={(val: any, name: any) => [`${val} req/s`, name]}
                />
                <Area type="monotone" dataKey="requests200" name="200 OK" stroke="#6366f1" strokeWidth={2} fillOpacity={1} fill="url(#metric200)" />
                <Area type="monotone" dataKey="requests429" name="429 Too Many Requests" stroke="#f59e0b" strokeWidth={2} fillOpacity={1} fill="url(#metric429)" />
                <Area type="monotone" dataKey="requests5xx" name="5xx Server Errors" stroke="#f43f5e" strokeWidth={2} fillOpacity={1} fill="url(#metric5xx)" />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        ) : (
          <div className="h-48 flex items-center justify-center text-xs text-zinc-400">
            Awaiting Prometheus time-series matrix points
          </div>
        )}
      </div>

      {/* Latency Distribution & Histogram Chart */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
          <div className="flex items-center justify-between mb-4">
            <div>
              <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
                <Clock className="w-4 h-4 text-cyan-400" />
                Latency Histogram Distribution
              </h3>
              <p className="text-xs text-zinc-400">
                Cumulative request latency distribution from <code className="text-zinc-300 font-mono">gateway_request_duration_seconds</code>.
              </p>
            </div>
            <div className="text-xs font-mono text-cyan-400 font-medium">
              Avg: {metrics?.avgLatencyMs ? `${metrics.avgLatencyMs.toFixed(2)} ms` : 'N/A'}
            </div>
          </div>

          {latencyBuckets.length > 0 ? (
            <div className="h-56 w-full">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={latencyBuckets} margin={{ top: 10, right: 10, left: -15, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#27272a" vertical={false} />
                  <XAxis dataKey="le" stroke="#71717a" fontSize={10} tickLine={false} />
                  <YAxis stroke="#71717a" fontSize={10} tickLine={false} />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#18181b',
                      borderColor: '#27272a',
                      borderRadius: '8px',
                      color: '#f4f4f5',
                      fontSize: '11px',
                      fontFamily: 'monospace',
                    }}
                    formatter={(val: any) => [`${val.toLocaleString()} requests`, 'Cumulative Count (≤ Bound)']}
                  />
                  <Bar dataKey="count" name="Cumulative Requests" fill="#06b6d4" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          ) : (
            <div className="h-56 flex items-center justify-center text-xs text-zinc-400">
              No latency histogram buckets captured yet
            </div>
          )}
        </div>

        {/* Status Codes Distribution Chart */}
        <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div>
            <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2 mb-1">
              <Activity className="w-4 h-4 text-indigo-400" />
              Status Code Breakdown
            </h3>
            <p className="text-xs text-zinc-400 mb-4">
              Real-time counters from <code className="text-zinc-300 font-mono">gateway_requests_total</code>.
            </p>

            <div className="space-y-3 font-mono text-xs">
              {metrics && Object.keys(metrics.requestsByCode).length > 0 ? (
                Object.entries(metrics.requestsByCode).map(([code, count]) => {
                  const is200 = code === '200'
                  const is401 = code === '401'
                  const is429 = code === '429'
                  const is5xx = code.startsWith('5')

                  const colorClass = is200
                    ? 'text-emerald-400 bg-emerald-500/10 border-emerald-500/20'
                    : is429
                    ? 'text-amber-400 bg-amber-500/10 border-amber-500/20'
                    : is401
                    ? 'text-zinc-400 bg-zinc-800 border-zinc-700'
                    : 'text-rose-400 bg-rose-500/10 border-rose-500/20'

                  return (
                    <div
                      key={code}
                      className="p-3 rounded-lg bg-zinc-950 border border-zinc-800 flex items-center justify-between"
                    >
                      <div className="flex items-center gap-2">
                        <span className={`px-2 py-0.5 rounded text-[11px] font-bold border ${colorClass}`}>
                          {code}
                        </span>
                        <span className="text-zinc-300">
                          {is200
                            ? 'OK (Forwarded to Backend)'
                            : is401
                            ? 'Unauthorized (Key Missing/Invalid)'
                            : is429
                            ? 'Too Many Requests (Rate Limited)'
                            : is5xx
                            ? 'Server / Proxy Error'
                            : 'Other Status'}
                        </span>
                      </div>
                      <span className="text-zinc-100 font-bold">{count.toLocaleString()}</span>
                    </div>
                  )
                })
              ) : (
                <div className="p-4 rounded-lg bg-zinc-950 border border-zinc-800 text-center text-zinc-400">
                  Awaiting request activity
                </div>
              )}
            </div>
          </div>

          <div className="mt-4 pt-3 border-t border-zinc-800 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>Total Requests Scraped:</span>
            <span className="text-zinc-200 font-bold">{metrics?.totalRequests.toLocaleString() ?? 0}</span>
          </div>
        </div>
      </div>

      {/* Raw Prometheus Samples Explorer */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 mb-4">
          <div>
            <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
              <Layers className="w-4 h-4 text-indigo-400" />
              Live Prometheus Metric Explorer
            </h3>
            <p className="text-xs text-zinc-400">
              Inspect exact metric names, label dimensions, and numeric values as scraped by Prometheus.
            </p>
          </div>

          <div className="flex items-center gap-2">
            {/* Filter by Type */}
            <div className="flex items-center gap-1.5 bg-zinc-950 border border-zinc-800 rounded-lg px-2 py-1 text-xs">
              <Filter className="w-3.5 h-3.5 text-zinc-400" />
              <select
                value={selectedType}
                onChange={(e) => setSelectedType(e.target.value)}
                className="bg-transparent text-zinc-300 focus:outline-none text-xs"
              >
                <option value="all">All Types</option>
                <option value="counter">Counter</option>
                <option value="gauge">Gauge</option>
                <option value="histogram">Histogram</option>
              </select>
            </div>

            {/* Search Input */}
            <div className="relative">
              <Search className="w-3.5 h-3.5 text-zinc-400 absolute left-2.5 top-2.5" />
              <input
                type="text"
                placeholder="Search metrics or labels..."
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
                className="pl-8 pr-3 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-indigo-500 w-48 sm:w-64"
              />
            </div>
          </div>
        </div>

        {/* Metrics Table */}
        <div className="overflow-x-auto max-h-96 rounded-lg border border-zinc-800 bg-zinc-950">
          <table className="w-full text-left text-xs font-mono">
            <thead className="bg-zinc-900/90 text-zinc-400 border-b border-zinc-800 sticky top-0">
              <tr>
                <th className="px-4 py-2.5">Metric Name</th>
                <th className="px-4 py-2.5">Type</th>
                <th className="px-4 py-2.5">Labels</th>
                <th className="px-4 py-2.5 text-right">Value</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/60">
              {filteredSamples.length > 0 ? (
                filteredSamples.map((sample, idx) => (
                  <tr key={`${sample.name}-${idx}`} className="hover:bg-zinc-900/50 transition-colors">
                    <td className="px-4 py-2 text-indigo-300 font-medium truncate max-w-xs" title={sample.help}>
                      {sample.name}
                    </td>
                    <td className="px-4 py-2 text-zinc-400">
                      <span className="px-1.5 py-0.5 rounded text-[10px] bg-zinc-900 border border-zinc-800">
                        {sample.type || 'gauge'}
                      </span>
                    </td>
                    <td className="px-4 py-2 text-zinc-400 truncate max-w-sm">
                      {Object.keys(sample.labels).length > 0 ? (
                        <div className="flex flex-wrap gap-1">
                          {Object.entries(sample.labels).map(([k, v]) => (
                            <span
                              key={k}
                              className="px-1.5 py-0.2 rounded bg-zinc-900 border border-zinc-800/80 text-[10px] text-zinc-300"
                            >
                              <strong className="text-zinc-500">{k}=</strong>"{v}"
                            </span>
                          ))}
                        </div>
                      ) : (
                        <span className="text-zinc-600">—</span>
                      )}
                    </td>
                    <td className="px-4 py-2 text-right text-emerald-400 font-bold">
                      {sample.value}
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={4} className="px-4 py-8 text-center text-zinc-400">
                    No metrics match your search filter
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
