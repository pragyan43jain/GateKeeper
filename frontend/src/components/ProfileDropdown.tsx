import { useEffect, useRef, useState } from 'react'
import { ChevronDown, LogOut, Settings, Shield } from 'lucide-react'
import type { DashboardPrincipal } from '../services/api'

export function ProfileDropdown({ principal, onSettings, onLogout, busy }: {
  principal: DashboardPrincipal; onSettings: () => void; onLogout: () => void; busy: boolean
}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false)
    }
    document.addEventListener('pointerdown', close)
    return () => document.removeEventListener('pointerdown', close)
  }, [open])
  return <div className="profile-dropdown" ref={root} onBlur={event => {
    if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false)
  }} onKeyDown={event => {
    if (event.key === 'Escape') { setOpen(false); trigger.current?.focus() }
  }}>
    <button ref={trigger} className="profile-trigger" aria-expanded={open} aria-controls="profile-panel" aria-label="Account menu" onClick={() => setOpen(!open)}>
      <span className="profile-avatar">{principal.email.slice(0, 2).toUpperCase()}</span><ChevronDown size={13} />
    </button>
    {open && <div id="profile-panel" className="profile-panel" aria-label="Your account">
      <strong>{principal.email.split('@')[0]}</strong><p>{principal.email}</p>
      <span className="profile-role"><Shield size={12} />{principal.role}</span>
      <hr />
      <button onClick={() => { onSettings(); setOpen(false) }}><Settings size={15} />Account settings</button>
      <button disabled={busy} onClick={onLogout}><LogOut size={15} />{busy ? 'Signing out…' : 'Sign out'}</button>
    </div>}
  </div>
}
