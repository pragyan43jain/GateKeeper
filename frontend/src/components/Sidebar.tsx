import React from 'react'
import {
  Activity,
  Server,
  Gauge,
  BarChart3,
  Terminal,
  Shield,
  Radio,
} from 'lucide-react'
import type { TabType, GatewayHealth } from '../types'

interface SidebarProps {
  currentTab: TabType
  onSelectTab: (tab: TabType) => void
  health: GatewayHealth | null
}

export const Sidebar: React.FC<SidebarProps> = ({ currentTab, onSelectTab, health }) => {
  const navItems: { id: TabType; label: string; icon: React.ElementType; badge?: string }[] = [
    { id: 'overview', label: 'Overview', icon: Activity },
    { id: 'backends', label: 'Backends', icon: Server, badge: '2 Nodes' },
    { id: 'ratelimit', label: 'Rate Limiting', icon: Gauge },
    { id: 'metrics', label: 'Metrics', icon: BarChart3 },
    { id: 'playground', label: 'API Playground', icon: Terminal, badge: 'Live' },
  ]

  const isHealthy = health?.status === 'healthy'

  return (
    <aside className="w-64 bg-zinc-900 border-r border-zinc-800 flex flex-col justify-between shrink-0 select-none">
      <div>
        {/* Brand header */}
        <div className="h-16 px-5 border-b border-zinc-800 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-indigo-500/10 border border-indigo-500/30 flex items-center justify-center text-indigo-400">
              <Shield className="w-4 h-4" />
            </div>
            <div>
              <div className="font-semibold text-sm text-zinc-100 tracking-tight flex items-center gap-1.5">
                GateKeeper
                <span className="text-[10px] uppercase font-mono px-1 py-0.2 bg-zinc-800 text-zinc-400 rounded">
                  v1.0
                </span>
              </div>
              <div className="text-[11px] text-zinc-400">API Gateway Console</div>
            </div>
          </div>
        </div>

        {/* System status quick indicator */}
        <div className="p-3 mx-3 my-3 rounded-lg bg-zinc-950/60 border border-zinc-800/80">
          <div className="flex items-center justify-between text-xs">
            <span className="text-zinc-400 flex items-center gap-1.5 font-medium">
              <Radio className={`w-3.5 h-3.5 ${isHealthy ? 'text-emerald-400 animate-pulse' : 'text-rose-400'}`} />
              Gateway Status
            </span>
            <span
              className={`text-[11px] px-1.5 py-0.5 rounded font-mono font-medium ${
                isHealthy
                  ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                  : 'bg-rose-500/10 text-rose-400 border border-rose-500/20'
              }`}
            >
              {health?.status ? health.status.toUpperCase() : 'CHECKING'}
            </span>
          </div>
          <div className="mt-1.5 text-[11px] text-zinc-400 truncate">
            {health?.message || 'Connecting to gateway...'}
          </div>
        </div>

        {/* Navigation links */}
        <nav className="px-3 space-y-1">
          {navItems.map((item) => {
            const Icon = item.icon
            const isActive = currentTab === item.id
            return (
              <button
                key={item.id}
                onClick={() => onSelectTab(item.id)}
                className={`w-full flex items-center justify-between px-3 py-2 rounded-md text-xs font-medium transition-colors ${
                  isActive
                    ? 'bg-zinc-800 text-zinc-100 shadow-sm border border-zinc-700/60'
                    : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/50'
                }`}
              >
                <div className="flex items-center gap-2.5">
                  <Icon className={`w-4 h-4 ${isActive ? 'text-indigo-400' : 'text-zinc-400'}`} />
                  <span>{item.label}</span>
                </div>
                {item.badge && (
                  <span
                    className={`text-[10px] font-mono px-1.5 py-0.2 rounded ${
                      isActive
                        ? 'bg-indigo-500/20 text-indigo-300'
                        : 'bg-zinc-800/80 text-zinc-400'
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
              </button>
            )
          })}
        </nav>
      </div>

      {/* Footer stats */}
      <div className="p-3 border-t border-zinc-800 text-[11px] text-zinc-400 space-y-1 font-mono">
        <div className="flex justify-between">
          <span>PORT</span>
          <span className="text-zinc-300">8082 : 8080</span>
        </div>
        <div className="flex justify-between">
          <span>TARGETS</span>
          <span className="text-zinc-300">backend-1, backend-2</span>
        </div>
        <div className="flex justify-between">
          <span>RATE LIMIT</span>
          <span className="text-zinc-300">10 req / 60s</span>
        </div>
      </div>
    </aside>
  )
}

