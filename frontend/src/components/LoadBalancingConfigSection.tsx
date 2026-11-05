import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { AlertTriangle, GitMerge, RefreshCw, Save } from 'lucide-react'
import type { LoadBalancingSettings, LoadBalancingStrategy } from '../types'
import { fetchLoadBalancingSettings, updateLoadBalancingSettings } from '../services/api'

interface LoadBalancingConfigSectionProps {
  apiKey: string
  onApiKeyChange: (key: string) => void
}

const strategyLabels: Record<LoadBalancingStrategy, string> = {
  round_robin: 'Round robin',
  weighted_round_robin: 'Weighted round robin',
  least_connections: 'Least in-flight requests',
  random: 'Random',
}

export function LoadBalancingConfigSection({ apiKey, onApiKeyChange }: LoadBalancingConfigSectionProps) {
  const [settings, setSettings] = useState<LoadBalancingSettings | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [managementKey, setManagementKey] = useState('')

  const load = useCallback(async () => {
    if (!apiKey) {
      setSettings(null)
      return
    }
    setLoading(true)
    setError(null)
    try {
      setSettings(await fetchLoadBalancingSettings(apiKey))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not load balancing settings')
    } finally {
      setLoading(false)
    }
  }, [apiKey])

  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect
    load()
  }, [load])

  const changeWeight = (name: string, value: number) => {
    setSettings((current) => current ? { ...current, weights: { ...current.weights, [name]: value } } : current)
  }

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!settings) return
    setSaving(true)
    setError(null)
    setNotice(null)
    try {
      const saved = await updateLoadBalancingSettings(apiKey, settings)
      setSettings(saved)
      setNotice('Load balancing settings saved and applied to new requests.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not save load balancing settings')
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="space-y-4 rounded-xl border border-zinc-800 bg-zinc-900 p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold text-zinc-100"><GitMerge className="h-4 w-4 text-indigo-400" />Load balancing configuration</h2>
          <p className="mt-1 text-xs text-zinc-400">Persisted settings apply to new backend selections. Health and circuit-breaker checks remain active.</p>
        </div>
        <button onClick={load} disabled={!apiKey || loading} className="inline-flex items-center gap-2 rounded-lg border border-zinc-700 px-3 py-2 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"><RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />Refresh</button>
      </div>

      {!apiKey && <div className="flex flex-wrap gap-2"><input type="password" value={managementKey} onChange={(event) => setManagementKey(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') onApiKeyChange(managementKey.trim()) }} placeholder="Management key (GATEKEEPER_API_KEY)" className="min-w-0 flex-1 rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-xs text-zinc-200" /><button onClick={() => onApiKeyChange(managementKey.trim())} className="rounded-lg bg-indigo-600 px-3 py-2 text-xs font-medium text-white">Connect</button></div>}
      {error && <div role="alert" className="flex items-center gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-rose-300"><AlertTriangle className="h-4 w-4 shrink-0" />{error}</div>}
      {notice && <div role="status" className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3 text-xs text-emerald-300">{notice}</div>}

      {settings && <form onSubmit={save} className="grid gap-4 lg:grid-cols-[minmax(220px,1fr)_2fr_auto] lg:items-end">
        <label className="space-y-1 text-xs text-zinc-400">Algorithm<select value={settings.strategy} onChange={(event) => setSettings({ ...settings, strategy: event.target.value as LoadBalancingStrategy })} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-100">{Object.entries(strategyLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{Object.entries(settings.weights).map(([name, weight]) => <label key={name} className="space-y-1 text-xs text-zinc-400">{name} weight<input type="number" min={1} max={1000} value={weight} onChange={(event) => changeWeight(name, Number(event.target.value))} disabled={settings.strategy !== 'weighted_round_robin'} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm text-zinc-100 disabled:opacity-50" /></label>)}</div>
        <button disabled={!apiKey || loading || saving} className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-2 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50"><Save size={14} />{saving ? 'Saving…' : 'Save settings'}</button>
      </form>}
      {!apiKey && <p className="text-xs text-zinc-500">Connect with the management key to view and update settings.</p>}
      {apiKey && !settings && !loading && !error && <p className="text-xs text-zinc-500">No balancing settings are available.</p>}
    </section>
  )
}
