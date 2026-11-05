import { useEffect, useState } from 'react'
import { KeyRound, Shield } from 'lucide-react'
import { dashboardSetupRequired, loginDashboard, setupDashboard } from '../services/api'
import type { DashboardPrincipal } from '../services/api'

interface AuthScreenProps {
  onAuthenticated: (principal: DashboardPrincipal) => void
}

export function AuthScreen({ onAuthenticated }: AuthScreenProps) {
  const [setupRequired, setSetupRequired] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [bootstrapKey, setBootstrapKey] = useState('')

  useEffect(() => {
    dashboardSetupRequired().then(setSetupRequired).catch((err: unknown) => {
      setError(err instanceof Error ? err.message : 'Could not reach dashboard authentication')
    }).finally(() => setLoading(false))
  }, [])

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      const principal = setupRequired
        ? await setupDashboard(bootstrapKey, email, password)
        : await loginDashboard(email, password)
      onAuthenticated(principal)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Authentication failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-zinc-950 px-5 text-zinc-100">
      <section className="w-full max-w-md rounded-2xl border border-zinc-800 bg-zinc-900/80 p-8 shadow-2xl">
        <div className="mb-7 flex items-center gap-3">
          <span className="rounded-xl bg-sky-500/10 p-3 text-sky-400"><Shield size={22} /></span>
          <div><h1 className="text-xl font-semibold">GateKeeper</h1><p className="text-sm text-zinc-400">Control Center</p></div>
        </div>
        <h2 className="text-lg font-medium">{loading ? 'Checking dashboard…' : setupRequired ? 'Set up administrator' : 'Sign in'}</h2>
        <p className="mt-1 text-sm text-zinc-400">{setupRequired ? 'Create the first dashboard account using your gateway bootstrap key.' : 'Sign in with your dashboard account to manage gateway configuration.'}</p>
        {!loading && <form className="mt-6 space-y-4" onSubmit={submit}>
          {setupRequired && <label className="block text-sm text-zinc-300">Bootstrap API key
            <span className="mt-1 flex items-center rounded-lg border border-zinc-700 bg-zinc-950 px-3"><KeyRound size={16} className="text-zinc-500" /><input required type="password" autoComplete="off" className="w-full bg-transparent px-3 py-3 outline-none" value={bootstrapKey} onChange={(event) => setBootstrapKey(event.target.value)} /></span>
          </label>}
          <label className="block text-sm text-zinc-300">Email<input required type="email" autoComplete="username" className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-3 outline-none focus:border-sky-500" value={email} onChange={(event) => setEmail(event.target.value)} /></label>
          <label className="block text-sm text-zinc-300">Password<input required type="password" minLength={12} autoComplete={setupRequired ? 'new-password' : 'current-password'} className="mt-1 w-full rounded-lg border border-zinc-700 bg-zinc-950 px-3 py-3 outline-none focus:border-sky-500" value={password} onChange={(event) => setPassword(event.target.value)} /></label>
          {error && <p role="alert" className="text-sm text-rose-400">{error}</p>}
          <button disabled={busy} className="w-full rounded-lg bg-sky-500 px-4 py-3 font-medium text-white transition hover:bg-sky-400 disabled:opacity-50">{busy ? 'Please wait…' : setupRequired ? 'Create administrator' : 'Sign in'}</button>
        </form>}
      </section>
    </main>
  )
}
