import React from 'react'
import {
  Server,
  ShieldCheck,
  ShieldAlert,
  Zap,
  RotateCcw,
  CheckCircle2,
  Info,
} from 'lucide-react'
import type { BackendNodeStatus } from '../types'

interface BackendsSectionProps {
  backends: BackendNodeStatus[]
  lastScraped: string
}

export const BackendsSection: React.FC<BackendsSectionProps> = ({ backends, lastScraped }) => {
  return (
    <div className="space-y-6">
      {/* Overview header banner */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <Server className="w-4 h-4 text-indigo-400" />
            Upstream Backend Nodes
          </h2>
          <p className="text-xs text-zinc-400 mt-0.5">
            Round-robin load balancing pool with independent per-backend circuit breakers.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs font-mono">
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Pool Strategy: <strong className="text-indigo-400">Round-Robin</strong>
          </span>
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Active Nodes: <strong className="text-emerald-400">{backends.length}</strong>
          </span>
        </div>
      </div>

      {/* Backend Cards Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {backends.map((backend) => {
          const isClosed = backend.state === 'closed'
          const isHalfOpen = backend.state === 'half-open'
          const isOpen = backend.state === 'open'

          return (
            <div
              key={backend.id}
              className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between"
            >
              <div>
                {/* Header row */}
                <div className="flex items-center justify-between pb-3 border-b border-zinc-800">
                  <div className="flex items-center gap-2.5">
                    <div
                      className={`w-8 h-8 rounded-lg flex items-center justify-center border ${
                        isClosed
                          ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                          : isHalfOpen
                          ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                          : 'bg-rose-500/10 text-rose-400 border-rose-500/20'
                      }`}
                    >
                      <Server className="w-4 h-4" />
                    </div>
                    <div>
                      <div className="text-sm font-semibold text-zinc-100 font-mono">
                        {backend.displayName}
                      </div>
                      <div className="text-[11px] text-zinc-400 font-mono">
                        {backend.target}
                      </div>
                    </div>
                  </div>

                  {/* Status Badge */}
                  <div
                    className={`px-2.5 py-1 rounded-full text-xs font-mono font-medium flex items-center gap-1.5 border ${
                      isClosed
                        ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                        : isHalfOpen
                        ? 'bg-amber-500/10 text-amber-400 border-amber-500/30'
                        : 'bg-rose-500/10 text-rose-400 border-rose-500/30'
                    }`}
                  >
                    <span
                      className={`w-1.5 h-1.5 rounded-full ${
                        isClosed
                          ? 'bg-emerald-400'
                          : isHalfOpen
                          ? 'bg-amber-400 animate-ping'
                          : 'bg-rose-400'
                      }`}
                    />
                    {backend.state.toUpperCase()}
                  </div>
                </div>

                {/* State explanation */}
                <div className="my-4 p-3 rounded-lg bg-zinc-950/80 border border-zinc-800 text-xs">
                  <div className="font-medium text-zinc-300 flex items-center gap-1.5 mb-1">
                    {isClosed ? (
                      <>
                        <ShieldCheck className="w-3.5 h-3.5 text-emerald-400" />
                        Circuit Closed (Healthy Traffic)
                      </>
                    ) : isHalfOpen ? (
                      <>
                        <RotateCcw className="w-3.5 h-3.5 text-amber-400" />
                        Circuit Half-Open (Trial Probe)
                      </>
                    ) : (
                      <>
                        <ShieldAlert className="w-3.5 h-3.5 text-rose-400" />
                        Circuit Open (Traffic Blocked / Failing Over)
                      </>
                    )}
                  </div>
                  <p className="text-[11px] text-zinc-400 leading-relaxed">
                    {isClosed &&
                      'Operating normally. Upstream transport errors are zero or below threshold. Requests are distributed via round-robin.'}
                    {isHalfOpen &&
                      'Cooldown period has elapsed. Gateway is allowing a single probe request to test upstream availability.'}
                    {isOpen &&
                      'Threshold exceeded (5 transport failures). Traffic is automatically skipped and routed to healthy alternatives.'}
                  </p>
                </div>

                {/* Node parameters */}
                <div className="grid grid-cols-2 gap-3 text-xs font-mono">
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-400 block text-[10px]">TOTAL TRIPS</span>
                    <span className="text-zinc-100 font-bold text-sm">{backend.trips}</span>
                  </div>
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-400 block text-[10px]">FAILURE THRESHOLD</span>
                    <span className="text-zinc-100 font-bold text-sm">5 failures</span>
                  </div>
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-400 block text-[10px]">COOLDOWN PERIOD</span>
                    <span className="text-zinc-100 font-bold text-sm">10s</span>
                  </div>
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-400 block text-[10px]">TRANSPORT</span>
                    <span className="text-zinc-100 font-bold text-sm">Dedicated Proxy</span>
                  </div>
                </div>
              </div>

              {/* Card footer */}
              <div className="mt-4 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
                <span>Metric: gateway_backend_circuit_breaker_state</span>
                <span>Value: {isClosed ? '0' : isOpen ? '1' : '2'}</span>
              </div>
            </div>
          )
        })}
      </div>

      {/* Circuit Breaker Architecture & Lifecycle Card */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
        <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2 mb-2">
          <Zap className="w-4 h-4 text-amber-400" />
          Per-Backend Circuit Breaker Lifecycle & Failover Guarantee
        </h3>
        <p className="text-xs text-zinc-400 mb-4 leading-relaxed">
          GateKeeper isolates circuit breakers per upstream instance. Unlike global circuit breakers,
          network issues or crashes affecting <code className="text-zinc-300">backend-1</code> will never
          trip or degrade traffic directed to <code className="text-zinc-300">backend-2</code>.
        </p>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 text-xs">
          <div className="p-3.5 rounded-lg bg-zinc-950 border border-emerald-500/20">
            <div className="font-semibold text-emerald-400 flex items-center gap-1.5 mb-1.5">
              <CheckCircle2 className="w-3.5 h-3.5" />
              1. Closed (Active)
            </div>
            <p className="text-[11px] text-zinc-400 leading-relaxed">
              All requests are dispatched round-robin across healthy nodes. Successful responses reset the consecutive failure counter to zero.
            </p>
          </div>

          <div className="p-3.5 rounded-lg bg-zinc-950 border border-rose-500/20">
            <div className="font-semibold text-rose-400 flex items-center gap-1.5 mb-1.5">
              <ShieldAlert className="w-3.5 h-3.5" />
              2. Open (Failover)
            </div>
            <p className="text-[11px] text-zinc-400 leading-relaxed">
              Triggered after 5 consecutive transport failures. The gateway skips the open node and routes all traffic to healthy peers. If all nodes are open, it returns <strong className="text-rose-300">503 with Retry-After</strong>.
            </p>
          </div>

          <div className="p-3.5 rounded-lg bg-zinc-950 border border-amber-500/20">
            <div className="font-semibold text-amber-400 flex items-center gap-1.5 mb-1.5">
              <RotateCcw className="w-3.5 h-3.5" />
              3. Half-Open (Probe)
            </div>
            <p className="text-[11px] text-zinc-400 leading-relaxed">
              After 10 seconds of cooldown, the gateway tests recovery with a single probe request. If it succeeds, the breaker closes; if it fails, it trips back to Open.
            </p>
          </div>
        </div>

        <div className="mt-4 p-3 rounded-lg bg-zinc-950 border border-zinc-800 text-[11px] text-zinc-400 flex items-start gap-2">
          <Info className="w-4 h-4 text-indigo-400 shrink-0 mt-0.5" />
          <span>
            <strong>Note on Live Health Checks:</strong> The <code className="text-zinc-300 font-mono">/health</code> endpoint reflects Gateway readiness, while per-backend states are exposed in real-time via <code className="text-zinc-300 font-mono">/metrics</code> under <code className="text-zinc-300 font-mono">gateway_backend_circuit_breaker_state</code>. Last scraped: {lastScraped || 'just now'}.
          </span>
        </div>
      </div>
    </div>
  )
}
