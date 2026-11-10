import { useEffect, useState, type FormEvent } from 'react'
import { Activity, ArrowRight, Command, Fingerprint, KeyRound, Layers3, LockKeyhole, Shield, Sparkles, Waypoints } from 'lucide-react'
import { API_BASE, dashboardSetupRequired, loginDashboard, registerDashboard, setupDashboard } from '../services/api'
import type { DashboardPrincipal } from '../services/api'

interface AuthScreenProps {
  onAuthenticated: (principal: DashboardPrincipal) => void
}

type AuthMode = 'login' | 'register'
const currentYear = new Date().getFullYear()

function describeAuthError(error: unknown): string {
  const message = error instanceof Error ? error.message : 'Could not reach dashboard authentication.'
  if (!API_BASE && typeof window !== 'undefined' && window.location.hostname.endsWith('vercel.app') && message.includes('404')) {
    return 'The gateway connection is unavailable. Please try again shortly.'
  }
  return message
}

export function AuthScreen({ onAuthenticated }: AuthScreenProps) {
  const [setupRequired, setSetupRequired] = useState(false)
  const [mode, setMode] = useState<AuthMode>('login')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [bootstrapKey, setBootstrapKey] = useState('')

  useEffect(() => {
    dashboardSetupRequired()
      .then((required) => {
        setSetupRequired(required)
        if (required) setMode('register')
      })
      .catch((err: unknown) => setError(describeAuthError(err)))
      .finally(() => setLoading(false))
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if ((setupRequired || mode === 'register') && password !== confirmPassword) {
      setError('The passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const principal = setupRequired
        ? await setupDashboard(bootstrapKey, email, password)
        : mode === 'register'
          ? await registerDashboard(email, password)
          : await loginDashboard(email, password)
      onAuthenticated(principal)
    } catch (err) {
      setError(describeAuthError(err))
    } finally {
      setBusy(false)
    }
  }

  const creating = setupRequired || mode === 'register'

  return (
    <main className="auth-shell min-h-screen overflow-hidden text-zinc-100">
      <div className="auth-grid" aria-hidden="true" />
      <div className="auth-glow auth-glow-one" aria-hidden="true" />
      <div className="auth-glow auth-glow-two" aria-hidden="true" />

      <div className="auth-topbar">
        <a className="auth-brand" href="/" aria-label="GateKeeper home">
          <span className="auth-brand-mark"><Shield size={19} strokeWidth={2.3} /></span>
          <span>gatekeeper<span className="auth-brand-period">.</span></span>
        </a>
        <div className="auth-topbar-note"><span className="auth-secure-dot" /> Secure control plane <span className="auth-divider">/</span> v1.0</div>
      </div>

      <div className="auth-layout">
        <section className="auth-story" aria-labelledby="auth-story-title">
          <div className="auth-kicker"><Sparkles size={13} /> GATEWAY INTELLIGENCE PLATFORM</div>
          <h1 id="auth-story-title">Traffic in control.<br /><span>Confidence in every</span><br /><span className="auth-heading-accent">request.</span></h1>
          <p className="auth-story-copy">One clear command center for protecting APIs, managing upstreams, and understanding every request as it moves through your system.</p>

          <div className="auth-visual" aria-label="GateKeeper traffic control capabilities">
            <div className="auth-visual-top"><span><i /> CONTROL PLANE</span><span className="auth-live-label"><b /> READY TO CONNECT</span></div>
            <div className="auth-flow">
              <div className="auth-flow-node auth-flow-client"><Command size={16} /><span>Client</span></div>
              <div className="auth-flow-line"><i /></div>
              <div className="auth-flow-node auth-flow-gateway"><Shield size={18} /><span>GateKeeper</span></div>
              <div className="auth-flow-branches"><span /><span /></div>
              <div className="auth-flow-backends"><div><Layers3 size={14} /> Upstream A</div><div><Layers3 size={14} /> Upstream B</div></div>
            </div>
            <div className="auth-visual-footer"><span><Activity size={13} /> Policy engine</span><span><Fingerprint size={13} /> Request identity</span><span><Waypoints size={13} /> Health-aware routing</span></div>
          </div>

          <div className="auth-feature-row">
            <div><span className="auth-feature-icon"><LockKeyhole size={15} /></span><span><strong>Secure by design</strong><small>Keys, sessions, and roles</small></span></div>
            <div><span className="auth-feature-icon"><Activity size={15} /></span><span><strong>Operational clarity</strong><small>Traffic, health, and traces</small></span></div>
          </div>
        </section>

        <section className="auth-card" aria-labelledby="auth-form-title">
          <div className="auth-card-orbit" aria-hidden="true"><span /><span /><span /></div>
          <div className="auth-card-heading">
            <span className="auth-card-icon"><Shield size={20} /></span>
            <div><p className="auth-overline">CONTROL CENTER</p><h2>Welcome to GateKeeper</h2></div>
          </div>
          <div className="auth-card-rule" />
          <div className="auth-card-intro">
            <div className="auth-form-kicker">{loading ? 'CONNECTING TO GATEWAY' : setupRequired ? 'FIRST-TIME SETUP' : creating ? 'NEW VIEWER ACCOUNT' : 'YOUR WORKSPACE'}</div>
            <h3 id="auth-form-title">{loading ? 'Checking your workspace' : setupRequired ? 'Create your admin account' : creating ? 'Create your account' : 'Sign in to continue'}</h3>
            <p>{loading ? 'Establishing a secure connection to your API gateway.' : setupRequired ? 'Set up the first administrator to unlock your control plane.' : creating ? 'Create a read-only account to explore the live gateway dashboard.' : 'Manage your APIs from one secure, real-time workspace.'}</p>
          </div>

          {loading ? (
            <div className="auth-loading" role="status"><span className="auth-spinner" /> Checking gateway connection…</div>
          ) : (
            <form className="auth-form" onSubmit={submit}>
              {setupRequired && <label className="auth-label">Bootstrap API key
                <span className="auth-input-wrap"><KeyRound size={16} /><input required type="password" autoComplete="off" placeholder="Enter the gateway bootstrap key" value={bootstrapKey} onChange={(event) => setBootstrapKey(event.target.value)} /></span>
              </label>}
              <label className="auth-label">Email address
                <span className="auth-input-wrap"><span className="auth-input-at">@</span><input required type="email" autoComplete="email" placeholder="you@company.com" value={email} onChange={(event) => setEmail(event.target.value)} /></span>
              </label>
              <label className="auth-label">Password
                <span className="auth-input-wrap"><LockKeyhole size={16} /><input required type="password" minLength={12} maxLength={72} autoComplete={creating ? 'new-password' : 'current-password'} placeholder={creating ? 'At least 12 characters' : 'Enter your password'} value={password} onChange={(event) => setPassword(event.target.value)} /></span>
              </label>
              {creating && <label className="auth-label">Confirm password
                <span className="auth-input-wrap"><LockKeyhole size={16} /><input required type="password" minLength={12} maxLength={72} autoComplete="new-password" placeholder="Re-enter your password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} /></span>
              </label>}

              {error && <div role="alert" className="auth-error"><span>!</span><p>{error}</p></div>}
              {creating && !setupRequired && <p className="auth-role-note"><Shield size={13} /> New accounts have viewer access. An administrator can grant more permissions.</p>}

              <button type="submit" disabled={busy} className="auth-submit">{busy ? <><span className="auth-button-spinner" /> Securing your session…</> : <>{setupRequired ? 'Create administrator' : creating ? 'Create account' : 'Sign in'} <ArrowRight size={16} /></>}</button>
            </form>
          )}

          {!loading && !setupRequired && <div className="auth-switch">{creating ? 'Already have an account?' : 'New to GateKeeper?'} <button type="button" onClick={() => { setMode(creating ? 'login' : 'register'); setError(''); setPassword(''); setConfirmPassword('') }}>{creating ? 'Sign in' : 'Create an account'}</button></div>}
          <div className="auth-card-foot"><span><LockKeyhole size={12} /> Encrypted session</span><span>Access is role-based</span></div>
        </section>
      </div>

      <footer className="auth-footer"><span>© {currentYear} GateKeeper</span><span>Intelligent API gateway management</span><span className="auth-footer-right"><i /> Built for dependable traffic</span></footer>
    </main>
  )
}
