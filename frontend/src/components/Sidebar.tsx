import { Activity, Server, Gauge, BarChart3, Terminal, Shield, ArrowUpRight } from 'lucide-react'
import type { TabType, GatewayHealth } from '../types'
interface SidebarProps { currentTab: TabType; onSelectTab: (tab: TabType) => void; health: GatewayHealth | null }
const items = [
  { id: 'overview', label: 'Overview', icon: Activity },
  { id: 'backends', label: 'Upstream nodes', icon: Server },
  { id: 'ratelimit', label: 'Traffic policies', icon: Gauge },
  { id: 'metrics', label: 'Observability', icon: BarChart3 },
  { id: 'playground', label: 'API playground', icon: Terminal },
] as const
export function Sidebar({ currentTab, onSelectTab, health }: SidebarProps) {
 return <aside className="console-sidebar">
  <a className="brand" href="#" onClick={e => { e.preventDefault(); onSelectTab('overview') }}><span className="brand-mark"><Shield size={22}/></span><span>gatekeeper<span className="brand-dot">.</span></span></a>
  <div className="workspace"><span className="workspace-avatar">G</span><div><strong>Gateway workspace</strong><small>Infrastructure console</small></div><span className="workspace-tag">01</span></div>
  <div className="nav-label">WORKSPACE</div>
  <nav aria-label="Main navigation">{items.map(({ id, label, icon: Icon }, index) => <button key={id} onClick={() => onSelectTab(id)} className={`nav-item ${currentTab === id ? 'active' : ''}`} aria-current={currentTab === id ? 'page' : undefined} title={label}><Icon size={18}/><span>{label}</span><small>0{index+1}</small></button>)}</nav>
  <div className="sidebar-bottom"><div className="sidebar-note"><Shield size={20}/><h3>Built for the unexpected.</h3><p>One place to observe, protect, and route every request.</p><button onClick={() => onSelectTab('playground')}>Explore your gateway <ArrowUpRight size={15}/></button></div><div className="connection"><i className={health?.status === 'healthy' ? 'connected' : ''}/><span>{health?.status === 'healthy' ? 'Gateway connected' : 'Awaiting connection'}</span><small>v1.0</small></div></div>
 </aside>
}
