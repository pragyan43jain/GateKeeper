import React from 'react'
import { RefreshCw, CheckCircle2, AlertCircle, Clock } from 'lucide-react'
import type { TabType, GatewayHealth } from '../types'

interface HeaderProps {
  currentTab: TabType
  health: GatewayHealth | null
  lastScraped: string
  isRefreshing: boolean
  autoRefresh: boolean
  onToggleAutoRefresh: () => void
  onManualRefresh: () => void
}

const tabMeta: Record<TabType, { title: string; subtitle: string }> = {
  overview: {
    title: 'Gateway Overview',
    subtitle: 'High-level telemetry, uptime, traffic distribution, and live system health.',
  },
  backends: {
    title: 'Upstream Backends & Circuit Breakers',
    subtitle: 'Per-backend node health, round-robin load distribution, and failure isolation states.',
  },
  ratelimit: {
    title: 'Redis Rate Limiting',
    subtitle: 'Sliding-window policy inspection, active quota consumption, and 429 rejection tracking.',
  },
  metrics: {
    title: 'Prometheus Metrics & Telemetry',
    subtitle: 'Time-series traffic graphs, status code breakdowns, and raw Prometheus scraped samples.',
  },
  playground: {
    title: 'Interactive API Playground',
    subtitle: 'Dispatch live authenticated HTTP requests to test failover, round-robin, and rate limits.',
  },
}

export const Header: React.FC<HeaderProps> = ({
  currentTab,
  health,
  lastScraped,
  isRefreshing,
  autoRefresh,
  onToggleAutoRefresh,
  onManualRefresh,
}) => {
  const meta = tabMeta[currentTab]
  const isHealthy = health?.status === 'healthy'

  return (
    <header className="h-16 px-6 bg-zinc-900/60 border-b border-zinc-800 flex items-center justify-between shrink-0 backdrop-blur-sm sticky top-0 z-10">
      <div>
        <h1 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
          {meta.title}
        </h1>
        <p className="text-xs text-zinc-400 truncate max-w-xl">
          {meta.subtitle}
        </p>
      </div>

      <div className="flex items-center gap-3">
        {/* Gateway Health Indicator */}
        <div className="hidden sm:flex items-center gap-1.5 px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-xs font-mono">
          {isHealthy ? (
            <>
              <CheckCircle2 className="w-3.5 h-3.5 text-emerald-400" />
              <span className="text-zinc-300">Gateway: 200 OK</span>
            </>
          ) : (
            <>
              <AlertCircle className="w-3.5 h-3.5 text-rose-400" />
              <span className="text-rose-300">Gateway: Disconnected</span>
            </>
          )}
        </div>

        {/* Last updated */}
        {lastScraped && (
          <div className="hidden md:flex items-center gap-1 text-[11px] text-zinc-400 font-mono">
            <Clock className="w-3 h-3 text-zinc-400" />
            <span>{lastScraped}</span>
          </div>
        )}

        {/* Auto refresh toggle */}
        <button
          onClick={onToggleAutoRefresh}
          className={`px-2.5 py-1 rounded text-xs font-medium border transition-colors ${
            autoRefresh
              ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
              : 'bg-zinc-800 text-zinc-400 border-zinc-700 hover:text-zinc-200'
          }`}
          title={autoRefresh ? 'Auto-refreshing every 5 seconds' : 'Auto-refresh paused'}
        >
          {autoRefresh ? 'Live (5s)' : 'Paused'}
        </button>

        {/* Manual Refresh button */}
        <button
          onClick={onManualRefresh}
          disabled={isRefreshing}
          className="p-1.5 rounded bg-zinc-800 border border-zinc-700 text-zinc-300 hover:text-zinc-100 hover:bg-zinc-700/80 transition-colors disabled:opacity-50"
          title="Refresh telemetry"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${isRefreshing ? 'animate-spin text-indigo-400' : ''}`} />
        </button>
      </div>
    </header>
  )
}

