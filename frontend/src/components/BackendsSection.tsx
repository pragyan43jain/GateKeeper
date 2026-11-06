import React, { useCallback, useEffect, useState } from 'react'
import {
  Server,
  ShieldCheck,
  ShieldAlert,
  RotateCcw,
  CheckCircle2,
  AlertTriangle,
  HeartPulse,
  Clock,
  Info,
  Plus,
  Trash2,
  Wrench,
  History,
} from 'lucide-react'
import type { BackendHealthEvent, BackendNodeStatus, ManagedBackend } from '../types'
import { LoadBalancingConfigSection } from './LoadBalancingConfigSection'
import { createManagedBackend, deleteManagedBackend, fetchBackendHealthHistory, fetchManagedBackends, updateManagedBackend } from '../services/api'

interface BackendsSectionProps {
  backends: BackendNodeStatus[]
  lastScraped: string
  apiKey: string
  onApiKeyChange: (key: string) => void
}

export const BackendsSection: React.FC<BackendsSectionProps> = ({ backends, lastScraped, apiKey, onApiKeyChange }) => {
  const configuredStrategy = backends.find((backend) => backend.strategy)?.strategy
  const strategyLabel = configuredStrategy?.replaceAll('_', ' ') || 'configured strategy'
  const [managedBackends, setManagedBackends] = useState<ManagedBackend[]>([])
  const [draft, setDraft] = useState({ name: '', url: '', weight: '1', healthPath: '/health' })
  const [managementError, setManagementError] = useState('')
  const [managementBusy, setManagementBusy] = useState(false)
  const [historyBackend, setHistoryBackend] = useState('')
  const [healthHistory, setHealthHistory] = useState<BackendHealthEvent[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const refreshManaged = useCallback(async () => {
    if (!apiKey.trim()) {
      setManagedBackends([])
      return
    }
    try {
      setManagedBackends(await fetchManagedBackends(apiKey))
      setManagementError('')
    } catch (error) {
      setManagementError(error instanceof Error ? error.message : 'Unable to load backend configuration.')
    }
  }, [apiKey])
  // oxlint-disable-next-line react/set-state-in-effect
  useEffect(() => { void refreshManaged() }, [refreshManaged])

  const addBackend = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!apiKey.trim()) return
    setManagementBusy(true)
    setManagementError('')
    try {
      await createManagedBackend(apiKey, {
        name: draft.name,
        url: draft.url,
        weight: Number(draft.weight),
        enabled: true,
        maintenance: false,
        healthPath: draft.healthPath,
      })
      setDraft({ name: '', url: '', weight: '1', healthPath: '/health' })
      await refreshManaged()
    } catch (error) {
      setManagementError(error instanceof Error ? error.message : 'Unable to register backend.')
    } finally {
      setManagementBusy(false)
    }
  }

  const saveBackend = async (backend: ManagedBackend, changes: Partial<ManagedBackend>) => {
    setManagementBusy(true)
    setManagementError('')
    try {
      await updateManagedBackend(apiKey, { ...backend, ...changes })
      await refreshManaged()
    } catch (error) {
      setManagementError(error instanceof Error ? error.message : 'Unable to update backend.')
    } finally {
      setManagementBusy(false)
    }
  }

  const removeBackend = async (backend: ManagedBackend) => {
    if (!window.confirm(`Remove ${backend.name}? Routes may prevent removing an assigned backend.`)) return
    setManagementBusy(true)
    setManagementError('')
    try {
      await deleteManagedBackend(apiKey, backend.configId)
      await refreshManaged()
    } catch (error) {
      setManagementError(error instanceof Error ? error.message : 'Unable to remove backend.')
    } finally {
      setManagementBusy(false)
    }
  }
  const toggleHistory = async (backend: ManagedBackend) => {
    if (historyBackend === backend.configId) {
      setHistoryBackend('')
      return
    }
    setHistoryBackend(backend.configId)
    setHistoryLoading(true)
    try {
      setHealthHistory(await fetchBackendHealthHistory(apiKey, backend.configId))
    } catch (error) {
      setManagementError(error instanceof Error ? error.message : 'Unable to load backend health history.')
      setHealthHistory([])
    } finally {
      setHistoryLoading(false)
    }
  }
  return (
    <div className="space-y-6">
      <LoadBalancingConfigSection apiKey={apiKey} onApiKeyChange={onApiKeyChange} />
      <section className="space-y-4 rounded-xl border border-zinc-800 bg-zinc-900 p-5">
        <div className="flex items-center gap-2 text-sm font-semibold text-zinc-100"><Wrench size={16} className="text-indigo-400" />Backend configuration</div>
        {!apiKey.trim() && <p className="text-xs text-zinc-400">Connect the administrator key above to add, remove, or maintain upstream nodes.</p>}
        {managementError && <p role="alert" className="rounded-lg border border-rose-500/20 bg-rose-500/10 p-3 text-xs text-rose-300">{managementError}</p>}
        {apiKey.trim() && <>
          <form onSubmit={addBackend} className="grid grid-cols-1 gap-2 md:grid-cols-[1fr_2fr_100px_1fr_auto]">
            <input aria-label="Backend name" required maxLength={100} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} placeholder="Name" className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
            <input aria-label="Backend URL" required type="url" value={draft.url} onChange={(e) => setDraft({ ...draft, url: e.target.value })} placeholder="https://api.example.com" className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
            <input aria-label="Backend weight" required type="number" min={1} max={1000} value={draft.weight} onChange={(e) => setDraft({ ...draft, weight: e.target.value })} className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
            <input aria-label="Health check path" required value={draft.healthPath} onChange={(e) => setDraft({ ...draft, healthPath: e.target.value })} placeholder="/health" className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
            <button type="submit" disabled={managementBusy} className="inline-flex items-center justify-center gap-2 rounded-lg bg-indigo-500 px-3 py-2 text-sm font-medium text-white disabled:opacity-50"><Plus size={15} />Add</button>
          </form>
          <div className="divide-y divide-zinc-800">
            {managedBackends.map((backend) => (
              <div key={backend.configId} className="py-3">
                <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-[220px]">
                  <p className="text-sm font-medium text-zinc-200">{backend.name} <span className="ml-2 text-xs text-zinc-500">weight {backend.weight}</span></p>
                  <p className="break-all font-mono text-xs text-zinc-500">{backend.url}{backend.healthPath}</p>
                  <p className="mt-1 text-[11px] uppercase tracking-wide text-zinc-500">{backend.enabled ? backend.maintenance ? 'Maintenance' : backend.healthStatus || 'Starting checks' : 'Disabled'}</p>
                </div>
                <div className="flex items-center gap-2">
                  <button type="button" disabled={historyLoading} onClick={() => void toggleHistory(backend)} className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-700 px-3 py-2 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"><History size={14} />{historyBackend === backend.configId ? 'Hide history' : 'Health history'}</button>
                  <button type="button" disabled={managementBusy || !apiKey.trim()} onClick={() => void saveBackend(backend, { maintenance: !backend.maintenance })} className="rounded-lg border border-zinc-700 px-3 py-2 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50">
                    {backend.maintenance ? 'Resume traffic' : 'Maintenance'}
                  </button>
                  <button type="button" disabled={managementBusy || !apiKey.trim()} onClick={() => void removeBackend(backend)} aria-label={`Remove ${backend.name}`} className="rounded-lg border border-rose-900/50 p-2 text-rose-300 hover:bg-rose-950/40 disabled:opacity-50"><Trash2 size={15} /></button>
                </div>
                </div>
                {historyBackend === backend.configId && <div className="mt-3 rounded-lg border border-zinc-800 bg-zinc-950/70 p-3">
                  <h4 className="mb-2 text-xs font-medium uppercase tracking-wide text-zinc-400">Recent health state changes</h4>
                  {historyLoading ? <p className="text-xs text-zinc-500">Loading history…</p> : healthHistory.length ? <ul className="space-y-2">{healthHistory.map((event) => <li key={event.id} className="flex flex-wrap items-start justify-between gap-2 text-xs"><span className="font-mono text-zinc-300">{event.previousState} → <strong className={event.state === 'healthy' ? 'text-emerald-300' : event.state === 'recovering' ? 'text-amber-300' : 'text-rose-300'}>{event.state}</strong>{event.reason && <span className="ml-2 text-zinc-500">{event.reason}</span>}</span><time className="text-zinc-500">{new Date(event.checkedAt).toLocaleString()}</time></li>)}</ul> : <p className="text-xs text-zinc-500">No health state changes recorded yet.</p>}
                </div>}
              </div>
            ))}
            {!managedBackends.length && <p className="py-4 text-sm text-zinc-500">No persisted backend records are available.</p>}
          </div>
        </>}
      </section>
      {/* Overview header banner */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <Server className="w-4 h-4 text-indigo-400" />
            Upstream Backend Nodes & Health Management
          </h2>
          <p className="text-xs text-zinc-400 mt-0.5">
            Health-aware load balancing pool with automated probes and per-upstream circuit breakers.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs font-mono">
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Strategy: <strong className="text-indigo-400">{configuredStrategy?.replaceAll('_', ' ') || 'Unavailable'}</strong>
          </span>
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Pool Nodes: <strong className="text-emerald-400">{backends.length}</strong>
          </span>
        </div>
      </div>

      {/* Backend Cards Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {backends.map((backend) => {
          const isClosed = backend.state === 'closed'
          const isHalfOpen = backend.state === 'half-open'
          const isOpen = backend.state === 'open'

          const healthState = backend.healthStatus || 'healthy'
          const isHealthHealthy = healthState === 'healthy'
          const isHealthRecovering = healthState === 'recovering'
          const isHealthUnhealthy = healthState === 'unhealthy'

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
                        isHealthHealthy && isClosed
                          ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                          : isHealthRecovering || isHalfOpen
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

                  {/* Dual Badges: Health & Circuit Breaker */}
                  <div className="flex items-center gap-2">
                    {/* Active Health Status Badge */}
                    <div
                      className={`px-2.5 py-1 rounded-full text-xs font-mono font-medium flex items-center gap-1.5 border ${
                        isHealthHealthy
                          ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                          : isHealthRecovering
                          ? 'bg-amber-500/10 text-amber-400 border-amber-500/30'
                          : 'bg-rose-500/10 text-rose-400 border-rose-500/30'
                      }`}
                    >
                      <span
                        className={`w-1.5 h-1.5 rounded-full ${
                          isHealthHealthy
                            ? 'bg-emerald-400'
                            : isHealthRecovering
                            ? 'bg-amber-400 animate-ping'
                            : 'bg-rose-400 animate-pulse'
                        }`}
                      />
                      <span>HEALTH: {healthState.toUpperCase()}</span>
                    </div>

                    {/* Circuit Breaker Status Badge */}
                    <div
                      className={`px-2.5 py-1 rounded-full text-xs font-mono font-medium flex items-center gap-1.5 border ${
                        isClosed
                          ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                          : isHalfOpen
                          ? 'bg-amber-500/10 text-amber-400 border-amber-500/30'
                          : 'bg-rose-500/10 text-rose-400 border-rose-500/30'
                      }`}
                    >
                      <span>CB: {backend.state.toUpperCase()}</span>
                    </div>
                  </div>
                </div>

                {/* State explanation banner */}
                <div className="my-4 p-3 rounded-lg bg-zinc-950/80 border border-zinc-800 text-xs">
                  <div className="font-medium text-zinc-300 flex items-center justify-between mb-1">
                    <div className="flex items-center gap-1.5">
                      {isHealthHealthy && isClosed ? (
                        <>
                          <ShieldCheck className="w-3.5 h-3.5 text-emerald-400" />
                          <span>Routing Active: Healthy & In-Service</span>
                        </>
                      ) : isHealthRecovering || isHalfOpen ? (
                        <>
                          <RotateCcw className="w-3.5 h-3.5 text-amber-400" />
                          <span>Recovery In-Progress: Probing Health</span>
                        </>
                      ) : (
                        <>
                          <ShieldAlert className="w-3.5 h-3.5 text-rose-400" />
                          <span>Traffic Excluded: Failing Over</span>
                        </>
                      )}
                    </div>

                    {backend.lastCheckTime && (
                      <span className="text-[10px] text-zinc-500 flex items-center gap-1">
                        <Clock className="w-3 h-3" />
                        Checked: {new Date(backend.lastCheckTime).toLocaleTimeString()}
                      </span>
                    )}
                  </div>

                  <p className="text-[11px] text-zinc-400 leading-relaxed">
                    {isHealthHealthy && isClosed &&
                      `Active background health checks passing. Consecutive failures at zero. Distributing client traffic via ${strategyLabel}.`}
                    {isHealthRecovering &&
                      `Node is in recovery trial (${backend.consecutiveSuccesses || 1} / 2 required successful checks). Traffic will automatically resume once recovery threshold is met.`}
                    {isHealthUnhealthy &&
                      `Health check failure threshold met (${backend.consecutiveFailures || 3} consecutive probe failures). Gateway excluded this node from traffic distribution.`}
                    {!isHealthUnhealthy && isOpen &&
                      'Circuit breaker opened due to upstream transport errors. Gateway has temporarily skipped this node.'}
                  </p>

                  {/* Failure reason callout if present */}
                  {backend.lastError && isHealthUnhealthy && (
                    <div className="mt-2 p-2 rounded bg-rose-500/10 border border-rose-500/20 text-rose-300 text-[11px] flex items-center gap-1.5 font-mono">
                      <AlertTriangle className="w-3.5 h-3.5 shrink-0 text-rose-400" />
                      <span>Failure Diagnostic: {backend.lastError}</span>
                    </div>
                  )}
                </div>

                {/* Node parameters */}
                <div className="grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-6 gap-2.5 text-xs font-mono">
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 block text-[10px]">HEALTH STATE</span>
                    <span
                      className={`font-bold text-xs ${
                        isHealthHealthy
                          ? 'text-emerald-400'
                          : isHealthRecovering
                          ? 'text-amber-400'
                          : 'text-rose-400'
                      }`}
                    >
                      {healthState.toUpperCase()}
                    </span>
                  </div>

                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 block text-[10px]">PROBE FAILURES</span>
                    <span className="text-zinc-100 font-bold text-xs">
                      {backend.consecutiveFailures || 0} / 3
                    </span>
                  </div>

                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 block text-[10px]">RECOVERY CHECKS</span>
                    <span className="text-zinc-100 font-bold text-xs">
                      {backend.consecutiveSuccesses || 0} / 2
                    </span>
                  </div>

                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 block text-[10px]">BREAKER TRIPS</span>
                    <span className="text-zinc-100 font-bold text-xs">{backend.trips}</span>
                  </div>
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 block text-[10px]">WEIGHT</span>
                    <span className="text-zinc-100 font-bold text-xs">{backend.weight ?? '—'}</span>
                  </div>
                  <div className="p-2.5 rounded bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 block text-[10px]">IN FLIGHT</span>
                    <span className="text-zinc-100 font-bold text-xs">{backend.activeRequests ?? '—'}</span>
                  </div>
                </div>
              </div>

              {/* Card footer */}
              <div className="mt-4 pt-3 border-t border-zinc-800/80 flex items-center justify-between text-[11px] text-zinc-400 font-mono">
                <span>Telemetry: /metrics & /api/backends</span>
                <span>Last Telemetry Scrape: {lastScraped || 'just now'}</span>
              </div>
            </div>
          )
        })}
      </div>

      {/* Health Management Architecture & Lifecycle Card */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
        <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2 mb-2">
          <HeartPulse className="w-4 h-4 text-rose-400" />
          Proactive Backend Health Management & Automatic Failover
        </h3>
        <p className="text-xs text-zinc-400 mb-4 leading-relaxed">
          GateKeeper combines continuous out-of-band active health checking with per-backend circuit breakers.
          If an upstream node crashes or degrades, active probes detect the failure and take the node out of
          rotation before client requests encounter failures.
        </p>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 text-xs">
          <div className="p-3.5 rounded-lg bg-zinc-950 border border-emerald-500/20">
            <div className="font-semibold text-emerald-400 flex items-center gap-1.5 mb-1.5">
              <CheckCircle2 className="w-3.5 h-3.5" />
              1. Healthy (In-Service)
            </div>
            <p className="text-[11px] text-zinc-400 leading-relaxed">
              Periodic probes (every 5s) verify the backend's <code className="text-zinc-300">/health</code> endpoint. Requests use the configured health-aware strategy.
            </p>
          </div>

          <div className="p-3.5 rounded-lg bg-zinc-950 border border-rose-500/20">
            <div className="font-semibold text-rose-400 flex items-center gap-1.5 mb-1.5">
              <ShieldAlert className="w-3.5 h-3.5" />
              2. Unhealthy (Excluded)
            </div>
            <p className="text-[11px] text-zinc-400 leading-relaxed">
              After 3 consecutive probe failures, the backend is marked Unhealthy and excluded from traffic distribution without penalizing healthy peers.
            </p>
          </div>

          <div className="p-3.5 rounded-lg bg-zinc-950 border border-amber-500/20">
            <div className="font-semibold text-amber-400 flex items-center gap-1.5 mb-1.5">
              <RotateCcw className="w-3.5 h-3.5" />
              3. Recovering (Trial)
            </div>
            <p className="text-[11px] text-zinc-400 leading-relaxed">
              When an unhealthy node returns 200 OK, it enters Recovering state. Once it achieves 2 consecutive successful checks, it is restored to traffic.
            </p>
          </div>
        </div>

        <div className="mt-4 p-3 rounded-lg bg-zinc-950 border border-zinc-800 text-[11px] text-zinc-400 flex items-start gap-2">
          <Info className="w-4 h-4 text-indigo-400 shrink-0 mt-0.5" />
          <span>
            <strong>Failover Guarantee:</strong> If all backends in the pool become unhealthy or circuit-broken, GateKeeper returns a controlled <code className="text-zinc-300 font-mono">503 Service Unavailable</code> with a <code className="text-zinc-300 font-mono">Retry-After: 5</code> header.
          </span>
        </div>
      </div>
    </div>
  )
}
