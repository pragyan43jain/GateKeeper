import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { AlertTriangle, Check, Copy, KeyRound, Plus, RefreshCw, Shield, X } from 'lucide-react'
import type { APIKeyRecord, ApplicationRecord } from '../types'
import { createAPIKey, createApplication, fetchAPIKeys, fetchApplications, revokeAPIKey } from '../services/api'

interface APIKeysSectionProps {
  apiKey: string
  onApiKeyChange: (key: string) => void
}

export function APIKeysSection({ apiKey, onApiKeyChange }: APIKeysSectionProps) {
  const [applications, setApplications] = useState<ApplicationRecord[]>([])
  const [keys, setKeys] = useState<APIKeyRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [applicationName, setApplicationName] = useState('')
  const [applicationDescription, setApplicationDescription] = useState('')
  const [keyName, setKeyName] = useState('')
  const [applicationId, setApplicationId] = useState('')
  const [permission, setPermission] = useState<'read' | 'write'>('read')
  const [requestsPerWindow, setRequestsPerWindow] = useState(100)
  const [windowSeconds, setWindowSeconds] = useState(60)
  const [expiresAt, setExpiresAt] = useState('')
  const [oneTimeSecret, setOneTimeSecret] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [adminKeyInput, setAdminKeyInput] = useState('')
  const [checkedAt, setCheckedAt] = useState(0)

  const load = useCallback(async () => {
    if (!apiKey) {
      setApplications([])
      setKeys([])
      return
    }
    setLoading(true)
    setError(null)
    try {
      const [apps, apiKeys] = await Promise.all([fetchApplications(apiKey), fetchAPIKeys(apiKey)])
      setApplications(apps)
      setKeys(apiKeys)
      setCheckedAt(Date.now())
      if (apps.length > 0) setApplicationId((current) => current || apps[0].id)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not load API key management data')
    } finally {
      setLoading(false)
    }
  }, [apiKey])

  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect
    load()
  }, [load])

  const handleCreateApplication = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setError(null)
    setNotice(null)
    try {
      const app = await createApplication(apiKey, { name: applicationName.trim(), description: applicationDescription.trim() })
      setApplications((current) => [app, ...current])
      setApplicationId(app.id)
      setApplicationName('')
      setApplicationDescription('')
      setNotice(`Application “${app.name}” created.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not create application')
    }
  }

  const handleCreateKey = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setError(null)
    setNotice(null)
    try {
      const created = await createAPIKey(apiKey, {
        applicationId,
        name: keyName.trim(),
        permissions: [permission],
        requestsPerWindow,
        windowSeconds,
        ...(expiresAt ? { expiresAt: new Date(expiresAt).toISOString() } : {}),
      })
      setOneTimeSecret(created.secret || null)
      setKeyName('')
      setExpiresAt('')
      await load()
      setNotice('API key created. Copy the secret now; it will not be shown again.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not create API key')
    }
  }

  const handleRevoke = async (key: APIKeyRecord) => {
    if (!window.confirm(`Revoke “${key.name}”? Requests using this key will be rejected immediately.`)) return
    setError(null)
    try {
      await revokeAPIKey(apiKey, key.id)
      setNotice(`API key “${key.name}” revoked.`)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not revoke API key')
    }
  }

  const copySecret = async () => {
    if (!oneTimeSecret) return
    try {
      await navigator.clipboard.writeText(oneTimeSecret)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1800)
    } catch {
      setError('Clipboard access failed. Select and copy the secret manually.')
    }
  }

  if (!apiKey) {
    return (
      <section className="rounded-xl border border-zinc-800 bg-zinc-900 p-6">
        <div className="mb-4 flex items-center gap-3">
          <KeyRound className="h-5 w-5 text-indigo-400" />
          <div>
            <h2 className="font-semibold text-zinc-100">API Key Manager</h2>
            <p className="text-xs text-zinc-400">Connect with the management key configured as GATEKEEPER_API_KEY.</p>
          </div>
        </div>
        <div className="flex max-w-xl gap-2">
          <input type="password" value={adminKeyInput} onChange={(event) => setAdminKeyInput(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') onApiKeyChange(adminKeyInput.trim()) }} placeholder="Administrator API key" className="min-w-0 flex-1 rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" />
          <button type="button" onClick={() => onApiKeyChange(adminKeyInput.trim())} className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-500">Connect</button>
        </div>
      </section>
    )
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-lg font-semibold text-zinc-100"><KeyRound className="h-5 w-5 text-indigo-400" />API Key Manager</h2>
          <p className="mt-1 text-sm text-zinc-400">Issue scoped consumer keys, set quotas, and revoke access.</p>
        </div>
        <button onClick={load} disabled={loading} className="inline-flex items-center gap-2 rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm text-zinc-300 hover:bg-zinc-800 disabled:opacity-50">
          <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />Refresh
        </button>
      </header>

      <div className="flex flex-wrap items-center gap-3 rounded-xl border border-zinc-800 bg-zinc-900 p-4">
        <Shield className="h-4 w-4 text-emerald-400" />
        <span className="text-sm text-zinc-300">Management access enabled</span>
        <span className="rounded bg-zinc-950 px-2 py-1 font-mono text-xs text-zinc-500">key retained in memory only</span>
        {apiKey !== '__session__' && <button onClick={() => { onApiKeyChange(''); setOneTimeSecret(null) }} className="ml-auto text-xs text-zinc-400 hover:text-white">Disconnect</button>}
      </div>

      {error && <div role="alert" className="flex items-center gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 p-3 text-sm text-rose-300"><AlertTriangle className="h-4 w-4 shrink-0" />{error}</div>}
      {notice && <div role="status" className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3 text-sm text-emerald-300">{notice}</div>}

      {oneTimeSecret && (
        <div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-4">
          <div className="mb-2 flex items-start justify-between gap-3">
            <div><h3 className="font-medium text-amber-200">Copy this secret now</h3><p className="text-xs text-amber-200/70">GateKeeper stores only its hash. This value will not appear again.</p></div>
            <button onClick={() => setOneTimeSecret(null)} aria-label="Dismiss secret" className="text-amber-200/70 hover:text-white"><X size={17} /></button>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 break-all rounded-lg bg-zinc-950 p-3 text-sm text-zinc-100">{oneTimeSecret}</code>
            <button onClick={copySecret} className="inline-flex items-center gap-2 rounded-lg bg-amber-400 px-3 py-2 text-sm font-medium text-zinc-950">{copied ? <Check size={16} /> : <Copy size={16} />}{copied ? 'Copied' : 'Copy'}</button>
          </div>
        </div>
      )}

      <div className="grid gap-6 xl:grid-cols-2">
        <form onSubmit={handleCreateApplication} className="space-y-3 rounded-xl border border-zinc-800 bg-zinc-900 p-5">
          <div><h3 className="font-semibold text-zinc-100">Applications</h3><p className="text-xs text-zinc-500">Group keys by the consuming application.</p></div>
          <input required maxLength={100} value={applicationName} onChange={(event) => setApplicationName(event.target.value)} placeholder="Application name" className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" />
          <input maxLength={500} value={applicationDescription} onChange={(event) => setApplicationDescription(event.target.value)} placeholder="Description (optional)" className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" />
          <button disabled={loading} className="inline-flex items-center gap-2 rounded-lg border border-zinc-700 px-3 py-2 text-sm text-zinc-200 hover:bg-zinc-800 disabled:opacity-50"><Plus size={16} />Create application</button>
          {applications.length > 0 && <ul className="space-y-1 border-t border-zinc-800 pt-3 text-sm text-zinc-300">{applications.map((app) => <li key={app.id} className="flex justify-between"><span>{app.name}</span><span className="text-xs text-zinc-600">{new Date(app.createdAt).toLocaleDateString()}</span></li>)}</ul>}
        </form>

        <form onSubmit={handleCreateKey} className="space-y-3 rounded-xl border border-zinc-800 bg-zinc-900 p-5">
          <div><h3 className="font-semibold text-zinc-100">Issue API key</h3><p className="text-xs text-zinc-500">The secret is displayed once after creation.</p></div>
          <select required value={applicationId} onChange={(event) => setApplicationId(event.target.value)} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200">
            <option value="">Choose an application</option>{applications.map((app) => <option key={app.id} value={app.id}>{app.name}</option>)}
          </select>
          <input required maxLength={100} value={keyName} onChange={(event) => setKeyName(event.target.value)} placeholder="Key name" className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" />
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="space-y-1 text-xs text-zinc-400">Permission<select value={permission} onChange={(event) => setPermission(event.target.value as 'read' | 'write')} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200"><option value="read">Read only</option><option value="write">Read and write</option></select></label>
            <label className="space-y-1 text-xs text-zinc-400">Requests per window<input required type="number" min={1} max={1000000} value={requestsPerWindow} onChange={(event) => setRequestsPerWindow(Number(event.target.value))} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" /></label>
            <label className="space-y-1 text-xs text-zinc-400">Window (seconds)<input required type="number" min={1} max={86400} value={windowSeconds} onChange={(event) => setWindowSeconds(Number(event.target.value))} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" /></label>
            <label className="space-y-1 text-xs text-zinc-400">Expires (optional)<input type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} className="w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm text-zinc-200" /></label>
          </div>
          <button disabled={loading || applications.length === 0} className="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-3 py-2 text-sm font-medium text-white hover:bg-indigo-500 disabled:opacity-50"><Plus size={16} />Generate key</button>
          {applications.length === 0 && <p className="text-xs text-amber-300">Create an application before issuing a key.</p>}
        </form>
      </div>

      <section className="overflow-hidden rounded-xl border border-zinc-800 bg-zinc-900">
        <div className="border-b border-zinc-800 p-5"><h3 className="font-semibold text-zinc-100">Consumer keys</h3><p className="text-xs text-zinc-500">Secrets are never returned by the list endpoint.</p></div>
        <div className="overflow-x-auto"><table className="w-full min-w-[950px] text-left text-sm">
          <thead className="bg-zinc-950/70 text-xs uppercase tracking-wide text-zinc-500"><tr><th className="px-4 py-3">Name</th><th className="px-4 py-3">Application</th><th className="px-4 py-3">Key hint</th><th className="px-4 py-3">Policy</th><th className="px-4 py-3">Usage</th><th className="px-4 py-3">Last used</th><th className="px-4 py-3">Status</th><th className="px-4 py-3 text-right">Action</th></tr></thead>
          <tbody className="divide-y divide-zinc-800/70">
            {keys.length === 0 ? <tr><td colSpan={8} className="px-4 py-10 text-center text-zinc-500">{loading ? 'Loading API keys…' : 'No API keys have been created.'}</td></tr> : keys.map((key) => {
              const expired = Boolean(key.expiresAt && checkedAt > 0 && Date.parse(key.expiresAt) <= checkedAt)
              const inactive = Boolean(key.revokedAt) || expired
              return <tr key={key.id} className="text-zinc-300"><td className="px-4 py-3"><div className="font-medium text-zinc-100">{key.name}</div><div className="text-xs text-zinc-500">{key.permissions.join(', ')}</div></td><td className="px-4 py-3">{key.applicationName}</td><td className="px-4 py-3 font-mono text-xs">{key.prefix}…</td><td className="px-4 py-3">{key.requestsPerWindow} / {key.windowSeconds}s</td><td className="px-4 py-3 font-mono">{key.usageCount.toLocaleString()}</td><td className="px-4 py-3 text-xs">{key.lastUsedAt ? new Date(key.lastUsedAt).toLocaleString() : 'Never'}</td><td className="px-4 py-3"><span className={`rounded-full px-2 py-1 text-xs ${inactive ? 'bg-zinc-800 text-zinc-500' : 'bg-emerald-500/10 text-emerald-400'}`}>{key.revokedAt ? 'Revoked' : expired ? 'Expired' : 'Active'}</span></td><td className="px-4 py-3 text-right"><button disabled={inactive} onClick={() => handleRevoke(key)} className="rounded-md border border-rose-500/30 px-2.5 py-1 text-xs text-rose-300 hover:bg-rose-500/10 disabled:opacity-40">Revoke</button></td></tr>
            })}
          </tbody>
        </table></div>
      </section>
    </div>
  )
}
