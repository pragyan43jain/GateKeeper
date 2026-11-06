import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { AlertTriangle, Gauge, RefreshCw, Save } from 'lucide-react'
import type { APIKeyRecord } from '../types'
import { fetchAPIKeys, updateAPIKeyPolicy } from '../services/api'

interface TrafficPoliciesSectionProps {
  apiKey: string
  onApiKeyChange: (key: string) => void
}

export function TrafficPoliciesSection({ apiKey, onApiKeyChange }: TrafficPoliciesSectionProps) {
  const [keys, setKeys] = useState<APIKeyRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [editing, setEditing] = useState<APIKeyRecord | null>(null)
  const [limit, setLimit] = useState(100)
  const [windowSeconds, setWindowSeconds] = useState(60)
  const [burst, setBurst] = useState(0)
  const [permission, setPermission] = useState<'read' | 'write'>('read')
  const [managementKey, setManagementKey] = useState('')

  const load = useCallback(async () => {
    if (!apiKey) {
      setKeys([])
      return
    }
    setLoading(true)
    setError(null)
    try {
      setKeys(await fetchAPIKeys(apiKey))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not load traffic policies')
    } finally {
      setLoading(false)
    }
  }, [apiKey])

  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect
    load()
  }, [load])

  const beginEdit = (key: APIKeyRecord) => {
    setEditing(key)
    setLimit(key.requestsPerWindow)
    setWindowSeconds(key.windowSeconds)
    setBurst(key.burst ?? 0)
    setPermission(key.permissions.includes('write') ? 'write' : 'read')
    setError(null)
    setNotice(null)
  }

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!editing) return
    setError(null)
    setNotice(null)
    try {
      await updateAPIKeyPolicy(apiKey, editing.id, {
        permissions: [permission],
        requestsPerWindow: limit,
        windowSeconds,
        burst,
      })
      setNotice(`Traffic policy for “${editing.name}” updated. New quotas apply to its next request.`)
      setEditing(null)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not update traffic policy')
    }
  }

  return (
    <section className="space-y-4 rounded-xl border border-zinc-800 bg-zinc-900 p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold text-zinc-100"><Gauge className="h-4 w-4 text-amber-400" />Per-key traffic policies</h2>
          <p className="mt-1 text-xs text-zinc-400">Update request quotas and permissions. Changes are read from PostgreSQL on the next gateway request.</p>
        </div>
        <button onClick={load} disabled={!apiKey || loading} className="inline-flex items-center gap-2 rounded-lg border border-zinc-700 px-3 py-2 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"><RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />Refresh</button>
      </div>

      {!apiKey && <div className="flex flex-wrap gap-2"><input type="password" value={managementKey} onChange={(event) => setManagementKey(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') onApiKeyChange(managementKey.trim()) }} placeholder="Management key (GATEKEEPER_API_KEY)" className="min-w-0 flex-1 rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-xs text-zinc-200" /><button onClick={() => onApiKeyChange(managementKey.trim())} className="rounded-lg bg-indigo-600 px-3 py-2 text-xs font-medium text-white">Connect</button></div>}
      {error && <div role="alert" className="flex items-center gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-rose-300"><AlertTriangle className="h-4 w-4 shrink-0" />{error}</div>}
      {notice && <div role="status" className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3 text-xs text-emerald-300">{notice}</div>}

      <div className="overflow-x-auto rounded-lg border border-zinc-800">
        <table className="w-full min-w-[760px] text-left text-xs">
          <thead className="bg-zinc-950/70 uppercase tracking-wide text-zinc-500"><tr><th className="px-3 py-3">Consumer</th><th className="px-3 py-3">Application</th><th className="px-3 py-3">Requests / window</th><th className="px-3 py-3">Permissions</th><th className="px-3 py-3">Usage</th><th className="px-3 py-3 text-right">Policy</th></tr></thead>
          <tbody className="divide-y divide-zinc-800/70">
            {keys.length === 0 ? <tr><td colSpan={6} className="px-3 py-8 text-center text-zinc-500">{!apiKey ? 'Connect with the management key to view policies.' : loading ? 'Loading policies…' : 'No API keys have policies yet.'}</td></tr> : keys.map((key) => <tr key={key.id} className="text-zinc-300"><td className="px-3 py-3"><span className="font-medium text-zinc-100">{key.name}</span><span className="ml-2 font-mono text-zinc-500">{key.prefix}…</span></td><td className="px-3 py-3">{key.applicationName}</td><td className="px-3 py-3 font-mono">{key.requestsPerWindow} + {key.burst ?? 0} / {key.windowSeconds}s</td><td className="px-3 py-3">{key.permissions.join(', ')}</td><td className="px-3 py-3 font-mono">{key.usageCount.toLocaleString()}</td><td className="px-3 py-3 text-right"><button disabled={!apiKey || Boolean(key.revokedAt)} onClick={() => beginEdit(key)} className="rounded-md border border-zinc-700 px-2.5 py-1.5 text-zinc-200 hover:bg-zinc-800 disabled:opacity-40">Edit policy</button></td></tr>)}
          </tbody>
        </table>
      </div>

      {editing && <form onSubmit={save} className="grid gap-3 rounded-lg border border-indigo-500/30 bg-zinc-950/70 p-4 md:grid-cols-4 md:items-end">
        <div className="md:col-span-4"><h3 className="text-sm font-medium text-zinc-100">Edit {editing.name}</h3><p className="text-xs text-zinc-500">Existing expiry is retained. Read and write grants both permissions.</p></div>
        <label className="space-y-1 text-xs text-zinc-400">Requests<input required type="number" min={1} max={1000000} value={limit} onChange={(event) => setLimit(Number(event.target.value))} className="w-full rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100" /></label>
        <label className="space-y-1 text-xs text-zinc-400">Window (seconds)<input required type="number" min={1} max={86400} value={windowSeconds} onChange={(event) => setWindowSeconds(Number(event.target.value))} className="w-full rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100" /></label>
        <label className="space-y-1 text-xs text-zinc-400">Burst allowance<input required type="number" min={0} max={1000000} value={burst} onChange={(event) => setBurst(Number(event.target.value))} className="w-full rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100" /></label>
        <label className="space-y-1 text-xs text-zinc-400">Access<select value={permission} onChange={(event) => setPermission(event.target.value as 'read' | 'write')} className="w-full rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-100"><option value="read">Read only</option><option value="write">Read + write</option></select></label>
        <div className="flex gap-2"><button disabled={loading} className="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-2 text-xs font-medium text-white disabled:opacity-50"><Save size={14} />Save</button><button type="button" onClick={() => setEditing(null)} className="rounded-lg border border-zinc-700 px-3 py-2 text-xs text-zinc-300">Cancel</button></div>
      </form>}
    </section>
  )
}
