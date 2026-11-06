import { useCallback, useEffect, useState } from 'react'
import { AlertCircle, Plus, RefreshCw, Route, ShieldCheck, Trash2 } from 'lucide-react'
import {
  createManagedRoute,
  deleteManagedRoute,
  fetchManagedBackends,
  fetchManagedRoutes,
  updateManagedRoute,
} from '../services/api'
import type { ManagedBackend, ManagedRoute } from '../types'

interface RoutesSectionProps { apiKey: string }

export function RoutesSection({ apiKey }: RoutesSectionProps) {
  const [routes, setRoutes] = useState<ManagedRoute[]>([])
  const [backends, setBackends] = useState<ManagedBackend[]>([])
  const [name, setName] = useState('')
  const [pathPrefix, setPathPrefix] = useState('/api/')
  const [methods, setMethods] = useState('GET')
  const [requestsPerWindow, setRequestsPerWindow] = useState('')
  const [windowSeconds, setWindowSeconds] = useState('')
  const [burst, setBurst] = useState('0')
  const [backendId, setBackendId] = useState('')
  const [authRequired, setAuthRequired] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    if (!apiKey.trim()) {
      setRoutes([])
      setBackends([])
      return
    }
    try {
      const [nextRoutes, nextBackends] = await Promise.all([
        fetchManagedRoutes(apiKey),
        fetchManagedBackends(apiKey),
      ])
      setRoutes(nextRoutes)
      setBackends(nextBackends)
      setBackendId((current) => current || nextBackends.find((backend) => backend.enabled)?.configId || '')
      setError('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Unable to load route configuration.')
    }
  }, [apiKey])

  // oxlint-disable-next-line react/set-state-in-effect
  useEffect(() => { void refresh() }, [refresh])

  const addRoute = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!apiKey.trim()) return
    if (Boolean(requestsPerWindow) !== Boolean(windowSeconds)) {
      setError('Set both route rate limit fields, or leave both blank.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await createManagedRoute(apiKey, {
        name,
        pathPrefix,
        methods: methods.split(',').map((method) => method.trim().toUpperCase()).filter(Boolean),
        backendId,
        authRequired,
        enabled: true,
        requestsPerWindow: requestsPerWindow ? Number(requestsPerWindow) : 0,
        windowSeconds: windowSeconds ? Number(windowSeconds) : 0,
        burst: Number(burst || 0),
      })
      setName('')
      setRequestsPerWindow('')
      setWindowSeconds('')
      setBurst('0')
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Unable to create route.')
    } finally {
      setBusy(false)
    }
  }

  const changeRoute = async (route: ManagedRoute, change: Partial<ManagedRoute>) => {
    setBusy(true)
    setError('')
    try {
      await updateManagedRoute(apiKey, { ...route, ...change })
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Unable to update route.')
    } finally {
      setBusy(false)
    }
  }

  const removeRoute = async (route: ManagedRoute) => {
    if (!window.confirm(`Delete route ${route.name} (${route.pathPrefix})?`)) return
    setBusy(true)
    setError('')
    try {
      await deleteManagedRoute(apiKey, route.id)
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Unable to delete route.')
    } finally {
      setBusy(false)
    }
  }

  const availableBackends = backends.filter((backend) => backend.enabled)

  return (
    <section className="space-y-5" aria-labelledby="routes-title">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="text-xs uppercase tracking-[0.2em] text-zinc-500">Traffic configuration</p>
          <h2 id="routes-title" className="mt-1 flex items-center gap-2 text-xl font-semibold text-zinc-100"><Route size={20} />Route manager</h2>
          <p className="mt-1 max-w-2xl text-sm text-zinc-400">Enabled routes use longest-prefix matching, enforce their method allowlist, and dispatch to the selected healthy backend.</p>
        </div>
        <button type="button" onClick={() => void refresh()} disabled={busy || !apiKey.trim()} className="inline-flex items-center gap-2 rounded-lg border border-white/10 px-3 py-2 text-sm text-zinc-300 hover:bg-white/5 disabled:opacity-50"><RefreshCw size={15} />Refresh</button>
      </div>

      {!apiKey.trim() && <p className="rounded-lg border border-zinc-800 bg-zinc-900 p-4 text-sm text-zinc-400">Connect the administrator key from API Key Manager to manage routes.</p>}
      {error && <div role="alert" className="flex items-center gap-2 rounded-lg border border-rose-900/60 bg-rose-950/30 p-3 text-sm text-rose-200"><AlertCircle size={16} />{error}</div>}

      {apiKey.trim() && <>
        <form onSubmit={addRoute} className="grid grid-cols-1 gap-3 rounded-xl border border-zinc-800 bg-zinc-900 p-4 md:grid-cols-2 xl:grid-cols-8">
          <label className="space-y-1 text-xs text-zinc-400">Route name<input required maxLength={100} value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-100" /></label>
          <label className="space-y-1 text-xs text-zinc-400">Path prefix<input required value={pathPrefix} onChange={(event) => setPathPrefix(event.target.value)} className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm text-zinc-100" /></label>
          <label className="space-y-1 text-xs text-zinc-400">Methods<input required value={methods} onChange={(event) => setMethods(event.target.value)} placeholder="GET, POST" className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm text-zinc-100" /></label>
          <label className="space-y-1 text-xs text-zinc-400">Backend<select required value={backendId} onChange={(event) => setBackendId(event.target.value)} className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-100"><option value="">Select enabled backend</option>{availableBackends.map((backend) => <option key={backend.configId} value={backend.configId}>{backend.name} · {backend.url}</option>)}</select></label>
          <label className="space-y-1 text-xs text-zinc-400">Requests / window<input type="number" min={1} max={1000000} value={requestsPerWindow} onChange={(event) => setRequestsPerWindow(event.target.value)} placeholder="No route limit" className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-100" /></label>
          <label className="space-y-1 text-xs text-zinc-400">Window seconds<input type="number" min={1} max={86400} value={windowSeconds} onChange={(event) => setWindowSeconds(event.target.value)} placeholder="60" className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-100" /></label>
          <label className="space-y-1 text-xs text-zinc-400">Burst allowance<input type="number" min={0} max={1000000} value={burst} onChange={(event) => setBurst(event.target.value)} className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-100" /></label>
          <label className="flex items-center gap-2 self-end rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-300"><input type="checkbox" checked={authRequired} onChange={(event) => setAuthRequired(event.target.checked)} />Require API key</label>
          <button type="submit" disabled={busy || !backendId} className="inline-flex items-center justify-center gap-2 self-end rounded-lg bg-indigo-500 px-3 py-2 text-sm font-medium text-white disabled:opacity-50"><Plus size={15} />Create route</button>
        </form>

        <div className="overflow-x-auto rounded-xl border border-zinc-800 bg-zinc-900">
          <table className="w-full min-w-[850px] text-left text-sm">
            <thead className="border-b border-zinc-800 text-xs uppercase tracking-wide text-zinc-500"><tr><th className="px-4 py-3">Route</th><th className="px-4 py-3">Methods</th><th className="px-4 py-3">Backend</th><th className="px-4 py-3">Rate limit</th><th className="px-4 py-3">Authentication</th><th className="px-4 py-3">State</th><th className="px-4 py-3">Actions</th></tr></thead>
            <tbody className="divide-y divide-zinc-800/80">
              {routes.map((route) => <tr key={route.id} className="text-zinc-300">
                <td className="px-4 py-3"><strong className="block font-medium text-zinc-100">{route.name}</strong><code className="text-xs text-zinc-500">{route.pathPrefix}</code></td>
                <td className="px-4 py-3 font-mono text-xs">{route.methods.join(', ')}</td>
                <td className="px-4 py-3">{route.backendName}</td>
                <td className="px-4 py-3">{route.requestsPerWindow ? `${route.requestsPerWindow} + ${route.burst ?? 0} burst / ${route.windowSeconds}s` : 'Default'}</td>
                <td className="px-4 py-3">{route.authRequired ? <span className="inline-flex items-center gap-1 text-emerald-300"><ShieldCheck size={14} />API key</span> : <span className="text-amber-300">Public · IP limited</span>}</td>
                <td className="px-4 py-3"><span className={route.enabled ? 'text-emerald-300' : 'text-zinc-500'}>{route.enabled ? 'Enabled' : 'Disabled'}</span></td>
                <td className="px-4 py-3"><div className="flex items-center gap-2"><button type="button" disabled={busy} onClick={() => void changeRoute(route, { enabled: !route.enabled })} className="rounded-md border border-zinc-700 px-2.5 py-1.5 text-xs hover:bg-zinc-800 disabled:opacity-50">{route.enabled ? 'Disable' : 'Enable'}</button><button type="button" disabled={busy} aria-label={`Delete ${route.name}`} onClick={() => void removeRoute(route)} className="rounded-md border border-rose-900/50 p-2 text-rose-300 hover:bg-rose-950/40 disabled:opacity-50"><Trash2 size={14} /></button></div></td>
              </tr>)}
              {!routes.length && <tr><td colSpan={7} className="px-4 py-10 text-center text-zinc-500">No route records configured.</td></tr>}
            </tbody>
          </table>
        </div>
      </>}
    </section>
  )
}
