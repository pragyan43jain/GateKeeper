import React, { useState, useEffect, useCallback, useRef } from 'react'
import {
  FileText,
  Search,
  RefreshCw,
  Copy,
  Check,
  Shield,
  Key,
  AlertTriangle,
  Activity,
  ChevronDown,
  ChevronUp,
} from 'lucide-react'
import type { RequestLogEntry } from '../types'
import { fetchRequestLogs } from '../services/api'

interface InspectorSectionProps {
  apiKey: string
  onApiKeyChange: (key: string) => void
}

export const InspectorSection: React.FC<InspectorSectionProps> = ({ apiKey, onApiKeyChange }) => {
  const [logs, setLogs] = useState<RequestLogEntry[]>([])
  const [isLoading, setIsLoading] = useState<boolean>(false)
  const [error, setError] = useState<string | null>(null)
  const [autoRefresh, setAutoRefresh] = useState<boolean>(true)
  const [methodFilter, setMethodFilter] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<string>('')
  const [searchQuery, setSearchQuery] = useState<string>('')
  const [limit, setLimit] = useState<number>(50)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const [localKeyInput, setLocalKeyInput] = useState<string>('')

  const isMounted = useRef(true)
  useEffect(() => {
    isMounted.current = true
    return () => {
      isMounted.current = false
    }
  }, [])

  const loadLogs = useCallback(async () => {
    if (!apiKey) {
      setLogs([])
      setError('Please provide a valid API key to inspect live gateway logs.')
      return
    }

    setIsLoading(true)
    setError(null)
    try {
      const statusNum = statusFilter ? parseInt(statusFilter, 10) : undefined
      const data = await fetchRequestLogs(apiKey, {
        limit,
        status: statusNum && !isNaN(statusNum) ? statusNum : undefined,
        method: methodFilter || undefined,
        search: searchQuery || undefined,
      })
      if (isMounted.current) {
        setLogs(data)
      }
    } catch (err: unknown) {
      if (isMounted.current) {
        const msg = err instanceof Error ? err.message : 'Failed to fetch logs'
        setError(msg)
      }
    } finally {
      if (isMounted.current) {
        setIsLoading(false)
      }
    }
  }, [apiKey, limit, statusFilter, methodFilter, searchQuery])

  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect
    loadLogs()
  }, [loadLogs])

  // Periodic auto-refresh
  useEffect(() => {
    if (!autoRefresh || !apiKey) return
    const timer = setInterval(() => {
      loadLogs()
    }, 3000)
    return () => clearInterval(timer)
  }, [autoRefresh, apiKey, loadLogs])

  const handleCopy = (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    navigator.clipboard.writeText(id)
    setCopiedId(id)
    setTimeout(() => setCopiedId(null), 2000)
  }

  const getStatusBadge = (status: number) => {
    if (status >= 200 && status < 300) {
      return 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
    }
    if (status >= 300 && status < 400) {
      return 'bg-cyan-500/10 text-cyan-400 border-cyan-500/30'
    }
    if (status >= 400 && status < 500) {
      return 'bg-amber-500/10 text-amber-400 border-amber-500/30'
    }
    return 'bg-rose-500/10 text-rose-400 border-rose-500/30'
  }

  const getMethodBadge = (method: string) => {
    switch (method.toUpperCase()) {
      case 'GET':
        return 'bg-blue-500/10 text-blue-400 border-blue-500/30'
      case 'POST':
        return 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
      case 'PUT':
      case 'PATCH':
        return 'bg-amber-500/10 text-amber-400 border-amber-500/30'
      case 'DELETE':
        return 'bg-rose-500/10 text-rose-400 border-rose-500/30'
      default:
        return 'bg-zinc-800 text-zinc-300 border-zinc-700'
    }
  }

  const formatTimestamp = (ts: string) => {
    try {
      const d = new Date(ts)
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
    } catch {
      return ts
    }
  }

  return (
    <div className="space-y-6">
      {/* Overview & Key Bar */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <FileText className="w-4 h-4 text-indigo-400" />
            Live Request Inspector
          </h2>
          <p className="text-xs text-zinc-400 mt-0.5">
            Real-time trace logs with end-to-end Request IDs, latency metrics, and failure attribution.
          </p>
        </div>

        <div className="flex items-center gap-2">
          {!apiKey ? (
            <div className="flex items-center gap-2 w-full md:w-auto">
              <div className="relative flex-1 md:w-64">
                <Key className="w-3.5 h-3.5 text-zinc-500 absolute left-3 top-1/2 -translate-y-1/2" />
                <input
                  type="password"
                  placeholder="Enter API Key to Inspect..."
                  value={localKeyInput}
                  onChange={(e) => setLocalKeyInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') onApiKeyChange(localKeyInput.trim())
                  }}
                  className="w-full pl-8 pr-3 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 text-xs font-mono text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-indigo-500"
                />
              </div>
              <button
                onClick={() => onApiKeyChange(localKeyInput.trim())}
                className="px-3 py-1.5 rounded-lg bg-indigo-600 hover:bg-indigo-500 text-xs font-medium text-white transition-colors"
              >
                Authenticate
              </button>
            </div>
          ) : (
            <div className="flex items-center gap-2">
              <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-xs text-zinc-300 font-mono flex items-center gap-1.5">
                <span className="w-2 h-2 rounded-full bg-emerald-400 animate-pulse" />
                Authenticated Session
              </span>
              <button
                onClick={() => onApiKeyChange('')}
                className="px-2 py-1 text-[11px] text-zinc-400 hover:text-zinc-200 transition-colors"
              >
                Change Key
              </button>
            </div>
          )}
        </div>
      </div>

      {/* Filter and Control Toolbar */}
      <div className="p-4 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-wrap items-center justify-between gap-3 text-xs">
        <div className="flex flex-wrap items-center gap-2.5 flex-1 min-w-[280px]">
          {/* Search box */}
          <div className="relative flex-1 min-w-[180px] max-w-sm">
            <Search className="w-3.5 h-3.5 text-zinc-500 absolute left-3 top-1/2 -translate-y-1/2" />
            <input
              type="text"
              placeholder="Search Request ID, path, backend..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-full pl-8 pr-3 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 text-zinc-200 font-mono placeholder-zinc-500 focus:outline-none focus:border-indigo-500"
            />
          </div>

          {/* Method filter */}
          <select
            value={methodFilter}
            onChange={(e) => setMethodFilter(e.target.value)}
            className="px-2.5 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 text-zinc-300 font-mono focus:outline-none focus:border-indigo-500"
          >
            <option value="">All Methods</option>
            <option value="GET">GET</option>
            <option value="POST">POST</option>
            <option value="PUT">PUT</option>
            <option value="DELETE">DELETE</option>
            <option value="HEAD">HEAD</option>
          </select>

          {/* Status filter */}
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="px-2.5 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 text-zinc-300 font-mono focus:outline-none focus:border-indigo-500"
          >
            <option value="">All Statuses</option>
            <option value="200">200 OK</option>
            <option value="401">401 Unauthorized</option>
            <option value="429">429 Too Many Requests</option>
            <option value="502">502 Bad Gateway</option>
            <option value="503">503 Service Unavailable</option>
          </select>

          {/* Limit selector */}
          <select
            value={limit}
            onChange={(e) => setLimit(Number(e.target.value))}
            className="px-2.5 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 text-zinc-300 font-mono focus:outline-none focus:border-indigo-500"
          >
            <option value={25}>25 entries</option>
            <option value={50}>50 entries</option>
            <option value={100}>100 entries</option>
          </select>
        </div>

        {/* Action Controls */}
        <div className="flex items-center gap-2">
          <label className="flex items-center gap-1.5 text-zinc-400 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
              className="rounded bg-zinc-950 border-zinc-800 text-indigo-600 focus:ring-0"
            />
            <span>Live poll (3s)</span>
          </label>

          <button
            onClick={loadLogs}
            disabled={isLoading || !apiKey}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-zinc-800 hover:bg-zinc-700 disabled:opacity-50 text-zinc-200 transition-colors font-mono text-xs"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${isLoading ? 'animate-spin text-indigo-400' : ''}`} />
            Refresh
          </button>
        </div>
      </div>

      {/* Error state */}
      {error && (
        <div className="p-4 rounded-xl bg-rose-500/10 border border-rose-500/20 text-rose-300 text-xs flex items-center justify-between">
          <div className="flex items-center gap-2">
            <AlertTriangle className="w-4 h-4 text-rose-400 flex-shrink-0" />
            <span>{error}</span>
          </div>
          <button
            onClick={loadLogs}
            className="px-2.5 py-1 rounded bg-rose-500/20 hover:bg-rose-500/30 text-rose-200 text-xs transition-colors"
          >
            Retry
          </button>
        </div>
      )}

      {/* Logs Table */}
      <div className="rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs font-mono border-collapse">
            <thead>
              <tr className="border-b border-zinc-800 bg-zinc-950/60 text-zinc-400 uppercase tracking-wider text-[11px]">
                <th className="py-3 px-4">Time</th>
                <th className="py-3 px-4">Method</th>
                <th className="py-3 px-4">Endpoint</th>
                <th className="py-3 px-4">Status</th>
                <th className="py-3 px-4">Latency</th>
                <th className="py-3 px-4">Backend</th>
                <th className="py-3 px-4">Request ID</th>
                <th className="py-3 px-4 text-right">Details</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/60">
              {logs.length === 0 ? (
                <tr>
                  <td colSpan={8} className="py-12 text-center text-zinc-500">
                    {isLoading ? (
                      <div className="flex flex-col items-center justify-center gap-2">
                        <RefreshCw className="w-5 h-5 animate-spin text-indigo-400" />
                        <span>Fetching live request logs...</span>
                      </div>
                    ) : !apiKey ? (
                      <div className="flex flex-col items-center justify-center gap-2 max-w-sm mx-auto">
                        <Key className="w-6 h-6 text-zinc-400" />
                        <span className="font-semibold text-zinc-300">API Key Required</span>
                        <p className="text-xs text-zinc-500 text-center font-sans">
                          Enter your administrator API key in the toolbar above to stream live requests.
                        </p>
                      </div>
                    ) : (
                      <div className="flex flex-col items-center justify-center gap-2">
                        <Activity className="w-5 h-5 text-zinc-600" />
                        <span>No requests match the current filters. Dispatch requests in the API Playground to see them here.</span>
                      </div>
                    )}
                  </td>
                </tr>
              ) : (
                logs.map((log) => {
                  const isExpanded = expandedId === log.id
                  const isFailed = log.status >= 400

                  return (
                    <React.Fragment key={log.id}>
                      <tr
                        onClick={() => setExpandedId(isExpanded ? null : log.id)}
                        className={`hover:bg-zinc-800/40 cursor-pointer transition-colors ${
                          isExpanded ? 'bg-zinc-800/30' : ''
                        }`}
                      >
                        <td className="py-3 px-4 text-zinc-400 whitespace-nowrap">
                          {formatTimestamp(log.timestamp)}
                        </td>
                        <td className="py-3 px-4">
                          <span
                            className={`px-2 py-0.5 rounded text-[11px] font-semibold border ${getMethodBadge(
                              log.method
                            )}`}
                          >
                            {log.method}
                          </span>
                        </td>
                        <td className="py-3 px-4 text-zinc-200 font-medium whitespace-nowrap">
                          {log.path}
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap">
                          <span
                            className={`px-2 py-0.5 rounded text-[11px] font-semibold border ${getStatusBadge(
                              log.status
                            )}`}
                          >
                            {log.status}
                          </span>
                        </td>
                        <td className="py-3 px-4 text-zinc-300 whitespace-nowrap">
                          <span
                            className={
                              log.durationMs > 100
                                ? 'text-rose-400 font-semibold'
                                : log.durationMs > 20
                                ? 'text-amber-400'
                                : 'text-emerald-400'
                            }
                          >
                            {log.durationMs.toFixed(1)}ms
                          </span>
                        </td>
                        <td className="py-3 px-4 whitespace-nowrap">
                          <span className="px-2 py-0.5 rounded bg-zinc-950 border border-zinc-800 text-zinc-300 text-[11px]">
                            {log.backend}
                          </span>
                        </td>
                        <td className="py-3 px-4 text-zinc-400">
                          <div className="flex items-center gap-1.5">
                            <span className="truncate max-w-[120px]" title={log.id}>
                              {log.id}
                            </span>
                            <button
                              onClick={(e) => handleCopy(log.id, e)}
                              className="p-1 hover:text-zinc-200 hover:bg-zinc-800 rounded transition-colors"
                              title="Copy Request ID"
                            >
                              {copiedId === log.id ? (
                                <Check className="w-3 h-3 text-emerald-400" />
                              ) : (
                                <Copy className="w-3 h-3" />
                              )}
                            </button>
                          </div>
                        </td>
                        <td className="py-3 px-4 text-right">
                          <button className="text-zinc-500 hover:text-zinc-300 p-1">
                            {isExpanded ? (
                              <ChevronUp className="w-4 h-4" />
                            ) : (
                              <ChevronDown className="w-4 h-4" />
                            )}
                          </button>
                        </td>
                      </tr>

                      {/* Expandable row detail view */}
                      {isExpanded && (
                        <tr className="bg-zinc-950/80 border-b border-zinc-800">
                          <td colSpan={8} className="p-4">
                            <div className="space-y-3 font-sans text-xs">
                              <div className="flex items-center justify-between pb-2 border-b border-zinc-800/80">
                                <div className="font-semibold text-zinc-200 flex items-center gap-2">
                                  <Shield className="w-4 h-4 text-indigo-400" />
                                  Request Trace Details
                                </div>
                                <span className="font-mono text-[11px] text-zinc-400">
                                  Timestamp: {log.timestamp}
                                </span>
                              </div>

                              <div className="grid grid-cols-1 md:grid-cols-3 gap-3 font-mono text-xs">
                                <div className="p-3 rounded-lg bg-zinc-900 border border-zinc-800">
                                  <span className="text-[10px] text-zinc-500 uppercase block mb-1">
                                    Full Request ID
                                  </span>
                                  <div className="flex items-center justify-between">
                                    <span className="text-zinc-200 select-all">{log.id}</span>
                                    <button
                                      onClick={(e) => handleCopy(log.id, e)}
                                      className="p-1 text-zinc-400 hover:text-zinc-200"
                                      title="Copy ID"
                                    >
                                      {copiedId === log.id ? (
                                        <Check className="w-3.5 h-3.5 text-emerald-400" />
                                      ) : (
                                        <Copy className="w-3.5 h-3.5" />
                                      )}
                                    </button>
                                  </div>
                                </div>

                                <div className="p-3 rounded-lg bg-zinc-900 border border-zinc-800">
                                  <span className="text-[10px] text-zinc-500 uppercase block mb-1">
                                    Target / Route
                                  </span>
                                  <span className="text-zinc-200">
                                    {log.method} {log.path}
                                  </span>
                                </div>

                                <div className="p-3 rounded-lg bg-zinc-900 border border-zinc-800">
                                  <span className="text-[10px] text-zinc-500 uppercase block mb-1">
                                    Selected Node
                                  </span>
                                  <span className="text-zinc-200">{log.backend}</span>
                                </div>
                              </div>

                              {isFailed && log.failureCategory && (
                                <div className="p-3 rounded-lg bg-rose-500/10 border border-rose-500/20 text-rose-300 font-sans">
                                  <strong className="block text-xs uppercase tracking-wider mb-0.5">
                                    Failure Classification: {log.failureCategory}
                                  </strong>
                                  <span className="text-xs text-rose-300/80">
                                    {log.failureCategory === 'auth_failed' &&
                                      'Request lacked a valid X-API-Key header. GateKeeper rejected access before proxy forwarding.'}
                                    {log.failureCategory === 'rate_limited' &&
                                      'Client exceeded the Redis sliding-window rate limit threshold. Returned 429 with Retry-After header.'}
                                    {log.failureCategory === 'bad_gateway' &&
                                      'Upstream backend failed or connection timed out beyond the configured BACKEND_TIMEOUT.'}
                                    {log.failureCategory === 'service_unavailable' &&
                                      'All backend nodes in the pool are unhealthy or tripped into circuit-breaker open state.'}
                                    {log.failureCategory === 'request_too_large' &&
                                      'Request body exceeded MAX_REQUEST_BODY_BYTES.'}
                                  </span>
                                </div>
                              )}

                              <div className="pt-2 text-[11px] text-zinc-500 flex items-center gap-1.5 font-sans">
                                <Shield className="w-3.5 h-3.5 text-emerald-500/70" />
                                <span>
                                  <strong>Privacy Guaranteed:</strong> API keys, authorization tokens, and request payloads are never logged or stored in memory.
                                </span>
                              </div>
                            </div>
                          </td>
                        </tr>
                      )}
                    </React.Fragment>
                  )
                })
              )}
            </tbody>
          </table>
        </div>

        {/* Table footer count */}
        <div className="p-3 bg-zinc-950/60 border-t border-zinc-800 flex items-center justify-between text-xs text-zinc-500 font-mono">
          <span>Showing {logs.length} of max {limit} buffered traces</span>
          <span>Bounded Circular Storage: 500 max</span>
        </div>
      </div>
    </div>
  )
}
