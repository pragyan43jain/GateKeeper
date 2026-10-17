import React, { useState } from 'react'
import {
  Terminal,
  Send,
  Eye,
  EyeOff,
  Copy,
  Check,
  Server,
  Gauge,
  Clock,
  Zap,
  RotateCcw,
} from 'lucide-react'
import { executePlaygroundRequest, normalizePlaygroundUrl } from '../services/api'
import type { PlaygroundResponseData } from '../types'

interface PlaygroundSectionProps {
  onResponseReceived: (res: PlaygroundResponseData) => void
  lastResponse: PlaygroundResponseData | null
  responseHistory: PlaygroundResponseData[]
  apiKey: string
  onChangeApiKey: (key: string) => void
}

export const PlaygroundSection: React.FC<PlaygroundSectionProps> = ({
  onResponseReceived,
  lastResponse,
  responseHistory,
  apiKey,
  onChangeApiKey,
}) => {
  const [method, setMethod] = useState<'GET' | 'POST' | 'PUT' | 'DELETE'>('GET')
  const [url, setUrl] = useState('/api/students')
  const [showKey, setShowKey] = useState(false)
  const [isLoading, setIsLoading] = useState(false)
  const [copied, setCopied] = useState(false)
  const [isBursting, setIsBursting] = useState(false)

  const normalizedTarget = normalizePlaygroundUrl(url)

  const handleSend = async (overrideKey?: string) => {
    setIsLoading(true)
    const effectiveKey = overrideKey !== undefined ? overrideKey : apiKey
    const res = await executePlaygroundRequest(url, method, effectiveKey)
    onResponseReceived(res)
    setIsLoading(false)
  }

  // Rapid test to illustrate round-robin and rate-limit countdown
  const handleBurst = async (count: number = 5) => {
    setIsBursting(true)
    for (let i = 0; i < count; i++) {
      const res = await executePlaygroundRequest(url, method, apiKey)
      onResponseReceived(res)
      // small delay to show visual progression
      await new Promise((resolve) => setTimeout(resolve, 150))
    }
    setIsBursting(false)
  }

  const handleCopyBody = () => {
    if (lastResponse?.body) {
      navigator.clipboard.writeText(lastResponse.body)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <div className="space-y-6">
      {/* Top Banner */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <Terminal className="w-4 h-4 text-indigo-400" />
            Live API Playground
          </h2>
          <p className="text-xs text-zinc-400 mt-0.5">
            Send real authenticated requests through GateKeeper to test load balancing, rate limiting, and failover.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs font-mono">
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Target: <code className="text-indigo-400">{normalizedTarget.effectiveUrl}</code>
          </span>
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Memory Key: <strong className={apiKey ? 'text-emerald-400' : 'text-zinc-500'}>{apiKey ? 'Set' : 'Unset'}</strong>
          </span>
        </div>
      </div>

      {/* Main Grid: Request Form vs Response Inspector */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
        {/* Left Column: Request Builder (5 cols) */}
        <div className="lg:col-span-5 p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between space-y-5">
          <div className="space-y-4">
            <h3 className="text-xs font-semibold text-zinc-300 uppercase tracking-wider font-mono">
              Request Configuration
            </h3>

            {/* Method & URL */}
            <div>
              <label className="block text-xs font-medium text-zinc-400 mb-1.5">
                Endpoint URL
              </label>
              <div className="flex rounded-lg border border-zinc-800 bg-zinc-950 overflow-hidden focus-within:border-indigo-500">
                <select
                  value={method}
                  onChange={(e) => setMethod(e.target.value as any)}
                  className="bg-zinc-900 px-3 py-2 text-xs font-mono font-bold text-zinc-200 border-r border-zinc-800 focus:outline-none"
                >
                  <option value="GET">GET</option>
                  <option value="POST">POST</option>
                  <option value="PUT">PUT</option>
                  <option value="DELETE">DELETE</option>
                </select>
                <input
                  type="text"
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="/api/students"
                  className="w-full bg-transparent px-3 py-2 text-xs font-mono text-zinc-200 focus:outline-none"
                />
              </div>

              {/* URL Normalization & CORS Status Hint */}
              <div className="mt-1.5 font-mono text-[11px]">
                {normalizedTarget.isExternal ? (
                  <div className="p-2 rounded bg-amber-500/10 border border-amber-500/30 text-amber-300">
                    <span className="font-bold">⚠️ External Destination:</span> {normalizedTarget.warning || 'Direct external target. Bypasses Vite gateway proxy and requires CORS enabled on the target.'}
                  </div>
                ) : normalizedTarget.normalized && normalizedTarget.originalUrl !== normalizedTarget.effectiveUrl ? (
                  <div className="p-1.5 rounded bg-indigo-500/10 border border-indigo-500/20 text-indigo-300">
                    <span className="font-semibold">Normalized:</span> <span className="line-through text-zinc-500">{normalizedTarget.originalUrl}</span> → <span className="text-emerald-400 font-bold">{normalizedTarget.effectiveUrl}</span> (routed via Vite dev proxy to prevent browser CORS block)
                  </div>
                ) : (
                  <div className="text-zinc-500 text-[10px]">
                    Proxy route: Vite proxy forwards <code className="text-zinc-400">{normalizedTarget.effectiveUrl}</code> → <code className="text-zinc-400">http://localhost:8082{normalizedTarget.effectiveUrl}</code>
                  </div>
                )}
              </div>
            </div>

            {/* API Key Input (IN MEMORY ONLY) */}
            <div>
              <div className="flex items-center justify-between mb-1.5">
                <label className="text-xs font-medium text-zinc-400">
                  API Key (<code className="text-zinc-300 font-mono">X-API-Key</code>)
                </label>
                <span className="text-[10px] text-amber-400/90 font-mono">
                  React memory only — never stored
                </span>
              </div>
              <div className="relative">
                <input
                  type={showKey ? 'text' : 'password'}
                  value={apiKey}
                  onChange={(e) => onChangeApiKey(e.target.value)}
                  placeholder="Enter GATEKEEPER_API_KEY..."
                  className="w-full px-3 py-2 pr-9 rounded-lg bg-zinc-950 border border-zinc-800 text-xs font-mono text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-indigo-500"
                />
                <button
                  type="button"
                  onClick={() => setShowKey(!showKey)}
                  className="absolute right-2.5 top-2.5 text-zinc-400 hover:text-zinc-200"
                  title={showKey ? 'Hide key' : 'Show key'}
                >
                  {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
              </div>
              <p className="text-[11px] text-zinc-400 mt-1">
                Supplied to gateway via constant-time authentication.
              </p>
            </div>

            {/* Quick Action Helpers */}
            <div className="pt-2">
              <span className="text-[11px] text-zinc-400 block mb-2 font-mono">TEST SCENARIOS</span>
              <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => handleBurst(5)}
                  disabled={isLoading || isBursting}
                  className="px-2.5 py-1.5 rounded-lg bg-zinc-950 hover:bg-zinc-800/80 border border-zinc-800 text-xs font-mono text-zinc-300 transition-colors flex items-center justify-center gap-1.5 disabled:opacity-50"
                  title="Sends 5 requests to observe round-robin alternation"
                >
                  <Zap className="w-3.5 h-3.5 text-indigo-400" />
                  Burst 5 Requests
                </button>
                <button
                  type="button"
                  onClick={() => handleSend('invalid-dev-key')}
                  disabled={isLoading || isBursting}
                  className="px-2.5 py-1.5 rounded-lg bg-zinc-950 hover:bg-zinc-800/80 border border-zinc-800 text-xs font-mono text-zinc-300 transition-colors flex items-center justify-center gap-1.5 disabled:opacity-50"
                  title="Sends invalid key to test 401 response"
                >
                  <RotateCcw className="w-3.5 h-3.5 text-rose-400" />
                  Test 401 Auth
                </button>
              </div>
            </div>
          </div>

          {/* Send Button */}
          <button
            type="button"
            onClick={() => handleSend()}
            disabled={isLoading || isBursting}
            className="w-full py-2.5 px-4 rounded-lg bg-indigo-600 hover:bg-indigo-500 font-semibold text-xs text-white transition-colors flex items-center justify-center gap-2 shadow-sm disabled:opacity-50"
          >
            {isLoading || isBursting ? (
              <>
                <div className="w-3.5 h-3.5 border-2 border-white/20 border-t-white rounded-full animate-spin" />
                <span>Executing Request...</span>
              </>
            ) : (
              <>
                <Send className="w-3.5 h-3.5" />
                <span>Send Request</span>
              </>
            )}
          </button>
        </div>

        {/* Right Column: Response Inspector (7 cols) */}
        <div className="lg:col-span-7 p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between space-y-4">
          <div>
            <div className="flex items-center justify-between pb-3 border-b border-zinc-800">
              <h3 className="text-xs font-semibold text-zinc-300 uppercase tracking-wider font-mono">
                Response Inspector
              </h3>

              {lastResponse && (
                <div className="flex items-center gap-2">
                  <span
                    className={`px-2.5 py-1 rounded text-xs font-mono font-bold border ${
                      lastResponse.status === 200
                        ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                        : lastResponse.status === 401
                        ? 'bg-zinc-800 text-zinc-300 border-zinc-700'
                        : lastResponse.status === 429
                        ? 'bg-amber-500/10 text-amber-400 border-amber-500/30'
                        : 'bg-rose-500/10 text-rose-400 border-rose-500/30'
                    }`}
                  >
                    {lastResponse.status} {lastResponse.statusText}
                  </span>
                  <span className="text-xs font-mono text-zinc-400 flex items-center gap-1">
                    <Clock className="w-3 h-3" />
                    {lastResponse.durationMs}ms
                  </span>
                </div>
              )}
            </div>

            {lastResponse ? (
              <div className="space-y-4 mt-4">
                {/* Gateway Metadata Badges */}
                <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs font-mono">
                  {/* Backend Identity */}
                  <div className="p-2.5 rounded-lg bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 text-[10px] block flex items-center gap-1">
                      <Server className="w-3 h-3 text-indigo-400" />
                      BACKEND ID
                    </span>
                    <span
                      className={`font-bold mt-0.5 block ${
                        lastResponse.backendId === 'backend-1'
                          ? 'text-indigo-400'
                          : lastResponse.backendId === 'backend-2'
                          ? 'text-cyan-400'
                          : 'text-zinc-400'
                      }`}
                    >
                      {lastResponse.backendId || 'None (Gateway)'}
                    </span>
                  </div>

                  {/* Rate Limit Remaining */}
                  <div className="p-2.5 rounded-lg bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 text-[10px] block flex items-center gap-1">
                      <Gauge className="w-3 h-3 text-amber-400" />
                      QUOTA LEFT
                    </span>
                    <span className="font-bold text-zinc-200 mt-0.5 block">
                      {lastResponse.rateLimitRemaining !== undefined
                        ? `${lastResponse.rateLimitRemaining} / ${lastResponse.rateLimitLimit || '10'}`
                        : 'N/A'}
                    </span>
                  </div>

                  {/* Retry-After Header */}
                  <div className="p-2.5 rounded-lg bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 text-[10px] block">RETRY-AFTER</span>
                    <span className="font-bold text-amber-400 mt-0.5 block">
                      {lastResponse.retryAfter ? `${lastResponse.retryAfter}s` : '0s'}
                    </span>
                  </div>

                  {/* Gateway Header */}
                  <div className="p-2.5 rounded-lg bg-zinc-950 border border-zinc-800">
                    <span className="text-zinc-500 text-[10px] block">X-GATEWAY</span>
                    <span className="font-bold text-zinc-200 mt-0.5 block">
                      {lastResponse.headers['x-gateway'] || 'Gatekeeper'}
                    </span>
                  </div>
                </div>

                {/* Response Payload Viewer */}
                <div>
                  <div className="flex items-center justify-between text-xs text-zinc-400 mb-1.5 font-mono">
                    <span>RESPONSE BODY</span>
                    <button
                      type="button"
                      onClick={handleCopyBody}
                      className="text-zinc-400 hover:text-zinc-200 flex items-center gap-1 text-[11px]"
                    >
                      {copied ? (
                        <>
                          <Check className="w-3 h-3 text-emerald-400" />
                          <span className="text-emerald-400">Copied</span>
                        </>
                      ) : (
                        <>
                          <Copy className="w-3 h-3" />
                          <span>Copy</span>
                        </>
                      )}
                    </button>
                  </div>
                  <pre className="p-3.5 rounded-lg bg-zinc-950 border border-zinc-800 font-mono text-xs text-zinc-200 overflow-x-auto max-h-60 leading-relaxed">
                    {lastResponse.body}
                  </pre>
                </div>
              </div>
            ) : (
              <div className="py-16 text-center text-zinc-400">
                <Terminal className="w-8 h-8 mx-auto mb-2 text-zinc-400" />
                <div className="text-xs font-mono">Awaiting request dispatch</div>
                <p className="text-[11px] text-zinc-400 mt-1">
                  Configure an API key and click "Send Request" to inspect the live response.
                </p>
              </div>
            )}
          </div>

          {/* Response History Table (last 5) */}
          {responseHistory.length > 0 && (
            <div className="pt-3 border-t border-zinc-800">
              <span className="text-[10px] font-mono text-zinc-400 block mb-2 uppercase">
                Recent Dispatches (Round-Robin History)
              </span>
              <div className="space-y-1 max-h-32 overflow-y-auto font-mono text-[11px]">
                {responseHistory.slice(0, 5).map((item, idx) => (
                  <div
                    key={idx}
                    className="p-1.5 rounded bg-zinc-950/70 border border-zinc-800/80 flex items-center justify-between text-zinc-400"
                  >
                    <div className="flex items-center gap-2">
                      <span
                        className={`font-bold ${
                          item.status === 200
                            ? 'text-emerald-400'
                            : item.status === 401
                            ? 'text-zinc-400'
                            : item.status === 429
                            ? 'text-amber-400'
                            : 'text-rose-400'
                        }`}
                      >
                        {item.status}
                      </span>
                      <span>{item.timestamp}</span>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-zinc-300 font-semibold">
                        {item.backendId ? `Served by: ${item.backendId}` : 'Gateway error'}
                      </span>
                      <span className="text-zinc-400">{item.durationMs}ms</span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

