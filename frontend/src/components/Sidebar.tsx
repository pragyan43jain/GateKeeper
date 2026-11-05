import { Activity, Server, Gauge, BarChart3, Terminal, Shield, ArrowUpRight, FileText, KeyRound, ClipboardList, Route, Settings } from 'lucide-react'
import type { TabType, GatewayHealth } from '../types'
import type { DashboardPrincipal } from '../services/api'

interface SidebarProps {
  currentTab: TabType
  onSelectTab: (tab: TabType) => void
  health: GatewayHealth | null
  principal: DashboardPrincipal
  onLogout: () => void
}

const items = [
  { id: 'overview', label: 'Overview', icon: Activity },
  { id: 'backends', label: 'Upstream nodes', icon: Server },
  { id: 'ratelimit', label: 'Traffic policies', icon: Gauge },
  { id: 'metrics', label: 'Observability', icon: BarChart3 },
  { id: 'playground', label: 'API playground', icon: Terminal },
  { id: 'inspector', label: 'Request inspector', icon: FileText },
  { id: 'keys', label: 'API key manager', icon: KeyRound },
  { id: 'audit', label: 'Audit logs', icon: ClipboardList },
  { id: 'routes', label: 'Route manager', icon: Route },
  { id: 'settings', label: 'Settings & users', icon: Settings },
] as const

export function Sidebar({ currentTab, onSelectTab, health, principal, onLogout }: SidebarProps) {
  return (
    <aside className="console-sidebar">
      <a
        className="brand"
        href="#"
        onClick={(e) => {
          e.preventDefault()
          onSelectTab('overview')
        }}
      >
        <span className="brand-mark">
          <Shield size={22} />
        </span>
        <span>
          gatekeeper<span className="brand-dot">.</span>
        </span>
      </a>
      <div className="workspace">
        <span className="workspace-avatar">G</span>
        <div>
          <strong>Gateway workspace</strong>
          <small>Infrastructure console</small>
        </div>
        <span className="workspace-tag">01</span>
      </div>
      <div className="nav-label">WORKSPACE</div>
      <nav aria-label="Main navigation">
        {items.map(({ id, label, icon: Icon }, index) => (
          <button
            key={id}
            onClick={() => onSelectTab(id)}
            className={`nav-item ${currentTab === id ? 'active' : ''}`}
            aria-current={currentTab === id ? 'page' : undefined}
            title={label}
          >
            <Icon size={18} />
            <span>{label}</span>
            <small>0{index + 1}</small>
          </button>
        ))}
      </nav>
      <div className="sidebar-bottom">
        <div className="mb-3 flex items-center gap-3 rounded-xl border border-zinc-800 bg-zinc-900/70 p-3">
          <span className="grid h-8 w-8 place-items-center rounded-full bg-sky-500/15 text-xs font-semibold text-sky-300">{principal.email.slice(0, 1).toUpperCase()}</span>
          <span className="min-w-0 flex-1"><strong className="block truncate text-xs">{principal.email}</strong><small className="text-zinc-500">{principal.role}</small></span>
          <button onClick={onLogout} className="text-xs text-zinc-400 hover:text-white" title="Sign out">Sign out</button>
        </div>
        <div className="sidebar-note">
          <Shield size={20} />
          <h3>Built for the unexpected.</h3>
          <p>One place to observe, protect, and route every request.</p>
          <button onClick={() => onSelectTab('playground')}>
            Explore your gateway <ArrowUpRight size={15} />
          </button>
        </div>
        <div className="connection">
          <i className={health?.status === 'healthy' ? 'connected' : ''} />
          <span>{health?.status === 'healthy' ? 'Gateway connected' : 'Awaiting connection'}</span>
          <small>v1.0</small>
        </div>
      </div>
    </aside>
  )
}
