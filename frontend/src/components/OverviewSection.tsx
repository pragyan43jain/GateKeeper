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
import TechStackBento from '@/components/ui/interactive-tech-stack-bento-grid-with-hover-effects'
import { Button } from './ui/button'
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
    totalRequests > 0 ? ((requests200 / totalRequests) * 100).toFixed(1) : '—'

  const avgLatency =
    metrics?.avgLatencyMs !== null && metrics?.avgLatencyMs !== undefined
      ? `${metrics.avgLatencyMs.toFixed(2)} ms`
      : '—'

  // Prepare chart data: either Prometheus matrix points or fallback summary


  return (
    <div className="overview-content space-y-6">
      <section className="overview-hero">
        <div className="hero-copy"><div className="eyebrow"><span/> YOUR INFRASTRUCTURE, IN FOCUS</div>
          <h2>Every request.<br/><em>Under control.</em></h2>
          <p>A clear view of your gateway. Follow the traffic, watch your upstreams, and keep your APIs moving.</p>
          <div className="hero-actions"><Button onClick={() => onNavigate('playground')}>Launch playground <ArrowUpRight size={17}/></Button><button className="hero-secondary" onClick={() => onNavigate('metrics')}>Explore metrics</button></div>
        </div>
        <div className="network-map" aria-label="Gateway routing topology">
          <div className="orbit orbit-one"/><div className="orbit orbit-two"/>
          <div className="network-label">LIVE ROUTING TOPOLOGY</div>
          <div className="network-client"><Terminal size={16}/><span>Incoming requests</span></div>
          <div className="network-line"/>
          <div className="network-core"><Activity size={26}/><span>GATEKEEPER</span><small>{isHealthy ? 'Gateway online' : 'Connecting'}</small></div>
          <div className="network-branches">{(metrics?.backends || []).map(b => <div className="network-node" key={b.id}><Server size={15}/><span>{b.displayName}</span><i className={b.state === 'closed' ? 'connected' : ''}/></div>)}{!metrics?.backends.length && <div className="network-node">Discovering upstreams…</div>}</div>
        </div>
      </section>
      <div className="section-caption"><span>Gateway at a glance</span><small>LIVE TELEMETRY · AUTO-SYNCED</small></div>
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
            <span className="text-zinc-300">{health?.statusCode ?? '—'}</span>
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
              {metrics ? totalRequests.toLocaleString() : '—'}
            </div>
            <div className="text-xs text-zinc-400 mt-1">
              Success rate: <span className="text-emerald-400 font-medium font-mono">{successRate}%</span>
            </div>
          </div>
          <div className="mt-3 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>200 OK: {requests200.toLocaleString()}</span>
            <span>Errors: {totalErrors.toLocaleString()}</span>
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
            <span className="text-zinc-300">Observed latency</span>
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
              {metrics?.rateLimitRejections.toLocaleString() ?? '—'}
            </div>
            <div className="text-xs text-zinc-400 mt-1">
              Redis sliding-window 429s
            </div>
          </div>
          <div className="mt-3 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
            <span>Default: 10 req / 60s</span>
            <span className="text-amber-400">Strict Lua</span>
          </div>
        </div>
      </div>

      <TechStackBento metrics={metrics} health={health} timeSeries={timeSeries} demo={false}/>

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
