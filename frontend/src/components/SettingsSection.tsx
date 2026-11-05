import { useCallback, useEffect, useState } from 'react'
import { Plus, RefreshCw, Shield, Trash2 } from 'lucide-react'
import { createDashboardUser, deleteDashboardUser, fetchDashboardUsers, updateDashboardUserRole } from '../services/api'
import type { DashboardPrincipal, DashboardUser } from '../services/api'

interface SettingsSectionProps { apiKey: string; principal: DashboardPrincipal }

export function SettingsSection({ apiKey, principal }: SettingsSectionProps) {
  const [users, setUsers] = useState<DashboardUser[]>([])
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<DashboardPrincipal['role']>('developer')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const refresh = useCallback(async () => {
    setBusy(true); setError('')
    try { setUsers(await fetchDashboardUsers(apiKey)) }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not load dashboard users') }
    finally { setBusy(false) }
  }, [apiKey])
  useEffect(() => {
    let active = true
    fetchDashboardUsers(apiKey).then((result) => { if (active) setUsers(result) }).catch((err: unknown) => {
      if (active) setError(err instanceof Error ? err.message : 'Could not load dashboard users')
    })
    return () => { active = false }
  }, [apiKey])

  if (principal.role !== 'admin') return <section className="rounded-xl border border-zinc-800 bg-zinc-900 p-6 text-sm text-zinc-400">Only an administrator can manage dashboard accounts.</section>

  async function createUser(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setError(''); setMessage('')
    try {
      await createDashboardUser(apiKey, { email, password, role })
      setEmail(''); setPassword(''); setMessage('Dashboard account created. Share the credentials through a secure channel.')
      await refresh()
    } catch (err) { setError(err instanceof Error ? err.message : 'Could not create dashboard account') }
    finally { setBusy(false) }
  }

  async function changeRole(user: DashboardUser, nextRole: DashboardPrincipal['role']) {
    setError(''); setMessage('')
    try { await updateDashboardUserRole(apiKey, user.id, nextRole); setMessage(`Updated ${user.email}.`); await refresh() }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not update role') }
  }

  async function removeUser(user: DashboardUser) {
    if (!window.confirm(`Remove dashboard access for ${user.email}?`)) return
    setError(''); setMessage('')
    try { await deleteDashboardUser(apiKey, user.id); setMessage(`Removed ${user.email}.`); await refresh() }
    catch (err) { setError(err instanceof Error ? err.message : 'Could not remove account') }
  }

  return <div className="space-y-6">
    <section className="rounded-xl border border-zinc-800 bg-zinc-900 p-5">
      <div className="mb-4 flex items-center justify-between"><div><h2 className="flex items-center gap-2 font-semibold"><Shield size={17} className="text-sky-400" />Dashboard accounts</h2><p className="mt-1 text-xs text-zinc-500">Role changes apply to the next management request.</p></div><button onClick={() => void refresh()} disabled={busy} className="rounded-lg border border-zinc-700 p-2 text-zinc-300"><RefreshCw size={15} className={busy ? 'animate-spin' : ''} /></button></div>
      {error && <p role="alert" className="mb-3 text-sm text-rose-400">{error}</p>}{message && <p role="status" className="mb-3 text-sm text-emerald-400">{message}</p>}
      <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead className="text-xs uppercase text-zinc-500"><tr><th className="py-2">Account</th><th>Role</th><th>Created</th><th className="text-right">Action</th></tr></thead><tbody>
        {users.map((user) => <tr key={user.id} className="border-t border-zinc-800 text-zinc-300"><td className="py-3">{user.email}{user.id === principal.id && <span className="ml-2 text-xs text-sky-400">You</span>}</td><td><select aria-label={`Role for ${user.email}`} disabled={user.id === principal.id} value={user.role} onChange={(event) => void changeRole(user, event.target.value as DashboardPrincipal['role'])} className="rounded border border-zinc-700 bg-zinc-950 px-2 py-1 disabled:opacity-50"><option value="admin">Admin</option><option value="developer">Developer</option><option value="viewer">Viewer</option></select></td><td>{new Date(user.createdAt).toLocaleDateString()}</td><td className="text-right"><button disabled={user.id === principal.id} onClick={() => void removeUser(user)} aria-label={`Remove ${user.email}`} className="rounded p-2 text-rose-400 hover:bg-rose-950/30 disabled:opacity-30"><Trash2 size={15} /></button></td></tr>)}
        {!users.length && !busy && <tr><td colSpan={4} className="py-6 text-center text-zinc-500">No dashboard accounts found.</td></tr>}
      </tbody></table></div>
    </section>
    <form onSubmit={createUser} className="rounded-xl border border-zinc-800 bg-zinc-900 p-5">
      <h2 className="font-semibold">Add dashboard account</h2><div className="mt-4 grid gap-3 md:grid-cols-4">
        <input required type="email" placeholder="Email address" value={email} onChange={(event) => setEmail(event.target.value)} className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
        <input required type="password" minLength={12} maxLength={128} autoComplete="new-password" placeholder="Temporary password (12+ chars)" value={password} onChange={(event) => setPassword(event.target.value)} className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm" />
        <select value={role} onChange={(event) => setRole(event.target.value as DashboardPrincipal['role'])} className="rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"><option value="developer">Developer</option><option value="viewer">Viewer</option><option value="admin">Admin</option></select>
        <button disabled={busy} className="inline-flex items-center justify-center gap-2 rounded-lg bg-sky-600 px-3 py-2 text-sm font-medium hover:bg-sky-500 disabled:opacity-50"><Plus size={16} />Create account</button>
      </div><p className="mt-3 text-xs text-zinc-500">Passwords are hashed before storage. Ask the new user to change the temporary password after sign-in.</p>
    </form>
  </div>
}
