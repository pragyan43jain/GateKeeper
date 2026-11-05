import { useCallback, useEffect, useState } from 'react'
import { AlertCircle, ClipboardList, RefreshCw } from 'lucide-react'
import { fetchAuditLogs } from '../services/api'
import type { AuditLogRecord } from '../types'

interface AuditLogsSectionProps {
  apiKey: string
}

export function AuditLogsSection({ apiKey }: AuditLogsSectionProps) {
  const [events, setEvents] = useState<AuditLogRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    if (!apiKey.trim()) {
      setEvents([])
      setError('Connect an administrator API key in API Key Manager to view audit events.')
      return
    }
    setLoading(true)
    setError('')
    try {
      setEvents(await fetchAuditLogs(apiKey))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unable to load audit events.')
    } finally {
      setLoading(false)
    }
  }, [apiKey])

  // oxlint-disable-next-line react/set-state-in-effect
  useEffect(() => { void load() }, [load])

  return (
    <section className="space-y-5" aria-labelledby="audit-title">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-xs uppercase tracking-[0.2em] text-zinc-500">Administration</p>
          <h2 id="audit-title" className="mt-1 flex items-center gap-2 text-xl font-semibold text-zinc-100">
            <ClipboardList size={20} /> Audit logs
          </h2>
          <p className="mt-1 text-sm text-zinc-400">Recent configuration changes recorded by GateKeeper.</p>
        </div>
        <button type="button" onClick={() => void load()} disabled={loading || !apiKey.trim()} className="inline-flex items-center gap-2 rounded-lg border border-white/10 px-3 py-2 text-sm text-zinc-300 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-50">
          <RefreshCw size={15} className={loading ? 'animate-spin' : ''} /> Refresh
        </button>
      </div>

      {error && <div role="alert" className="flex items-center gap-2 rounded-lg border border-amber-900/60 bg-amber-950/30 p-3 text-sm text-amber-200"><AlertCircle size={16} />{error}</div>}

      <div className="overflow-x-auto rounded-xl border border-white/10 bg-zinc-900/60">
        <table className="w-full min-w-[720px] text-left text-sm">
          <thead className="border-b border-white/10 text-xs uppercase tracking-wide text-zinc-500">
            <tr><th className="px-4 py-3">Time</th><th className="px-4 py-3">Actor</th><th className="px-4 py-3">Action</th><th className="px-4 py-3">Resource</th><th className="px-4 py-3">Result</th></tr>
          </thead>
          <tbody className="divide-y divide-white/5">
            {events.map((event) => (
              <tr key={event.id} className="text-zinc-300">
                <td className="whitespace-nowrap px-4 py-3">{new Date(event.createdAt).toLocaleString()}</td>
                <td className="px-4 py-3">{event.actor}</td>
                <td className="px-4 py-3 font-mono">{event.action}</td>
                <td className="px-4 py-3">{event.resourceType}{event.resourceId ? ` · ${event.resourceId}` : ''}</td>
                <td className="px-4 py-3">{String(event.details.status ?? '—')}</td>
              </tr>
            ))}
            {!loading && !error && events.length === 0 && <tr><td colSpan={5} className="px-4 py-10 text-center text-zinc-500">No administrative changes have been recorded.</td></tr>}
            {loading && events.length === 0 && <tr><td colSpan={5} className="px-4 py-10 text-center text-zinc-500">Loading audit history…</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  )
}
