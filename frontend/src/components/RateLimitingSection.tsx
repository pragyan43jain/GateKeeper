import React from 'react'
import {
  Gauge,
  Shield,
  AlertOctagon,
  Clock,
  Terminal,
  Database,
  ArrowRight,
} from 'lucide-react'
import type { TabType } from '../types'

interface RateLimitingSectionProps {
  rateLimitRejections: number
  lastObservedRemaining: string | null
  lastObservedLimit: string | null
  lastObservedRetryAfter: string | null
  onNavigate: (tab: TabType) => void
}

export const RateLimitingSection: React.FC<RateLimitingSectionProps> = ({
  rateLimitRejections,
  lastObservedRemaining,
  lastObservedLimit,
  lastObservedRetryAfter,
  onNavigate,
}) => {
  const limitNum = lastObservedLimit ? parseInt(lastObservedLimit, 10) : 10
  const remainingNum = lastObservedRemaining !== null ? parseInt(lastObservedRemaining, 10) : null

  // Calculate percentage used if remaining is known
  const usedPercent =
    remainingNum !== null
      ? Math.max(0, Math.min(100, Math.round(((limitNum - remainingNum) / limitNum) * 100)))
      : 0

  return (
    <div className="space-y-6">
      {/* Top Banner */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <Gauge className="w-4 h-4 text-amber-400" />
            Redis Sliding-Window Rate Limiter
          </h2>
          <p className="text-xs text-zinc-400 mt-0.5">
            Distributed token-bucket / counter enforced by Redis Lua scripts with SHA-256 key isolation.
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs font-mono">
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Quota: <strong className="text-amber-400">10 req / 60s</strong>
          </span>
          <span className="px-2.5 py-1 rounded bg-zinc-950 border border-zinc-800 text-zinc-300">
            Storage: <strong className="text-indigo-400">Redis 7</strong>
          </span>
        </div>
      </div>

      {/* Main Quota Status Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Active Client Quota Meter */}
        <div className="lg:col-span-2 p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div>
            <div className="flex items-center justify-between mb-4">
              <div>
                <h3 className="text-sm font-semibold text-zinc-100">
                  Active Client Quota
                </h3>
                <p className="text-xs text-zinc-400">
                  Observed from live <code className="text-zinc-300 font-mono">X-RateLimit-*</code> headers in API responses.
                </p>
              </div>

              {remainingNum !== null && (
                <span
                  className={`px-2.5 py-1 rounded-full text-xs font-mono font-medium border ${
                    remainingNum > 3
                      ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                      : remainingNum > 0
                      ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                      : 'bg-rose-500/10 text-rose-400 border-rose-500/20'
                  }`}
                >
                  {remainingNum} of {limitNum} left
                </span>
              )}
            </div>

            {remainingNum !== null ? (
              <div className="space-y-4 my-6">
                <div>
                  <div className="flex justify-between text-xs font-mono mb-2">
                    <span className="text-zinc-400">Consumed in 60s Window:</span>
                    <span className="text-zinc-200 font-bold">{limitNum - remainingNum} / {limitNum} ({usedPercent}%)</span>
                  </div>
                  {/* Progress Bar */}
                  <div className="h-3 w-full bg-zinc-950 rounded-full overflow-hidden border border-zinc-800 p-0.5">
                    <div
                      className={`h-full rounded-full transition-all duration-300 ${
                        usedPercent >= 90
                          ? 'bg-rose-500'
                          : usedPercent >= 60
                          ? 'bg-amber-500'
                          : 'bg-emerald-500'
                      }`}
                      style={{ width: `${usedPercent}%` }}
                    />
                  </div>
                </div>

                {lastObservedRetryAfter && (
                  <div className="p-3 rounded-lg bg-rose-500/10 border border-rose-500/20 text-xs text-rose-300 flex items-center justify-between font-mono">
                    <span className="flex items-center gap-1.5">
                      <AlertOctagon className="w-4 h-4 text-rose-400" />
                      Rate Limit Exceeded (HTTP 429)
                    </span>
                    <span>Retry-After: {lastObservedRetryAfter}s</span>
                  </div>
                )}
              </div>
            ) : (
              <div className="my-8 p-6 rounded-lg bg-zinc-950 border border-dashed border-zinc-800 text-center">
                <Gauge className="w-8 h-8 text-zinc-400 mx-auto mb-2" />
                <div className="text-sm font-medium text-zinc-300">
                  No active client quota session observed yet
                </div>
                <p className="text-xs text-zinc-400 mt-1 max-w-md mx-auto">
                  Because GateKeeper rate limits per API key, dispatch an authenticated request in the API Playground to inspect your live remaining tokens and reset headers.
                </p>
                <button
                  onClick={() => onNavigate('playground')}
                  className="mt-4 px-3 py-1.5 rounded-md bg-indigo-600 hover:bg-indigo-500 text-xs font-medium text-white transition-colors inline-flex items-center gap-1.5"
                >
                  <Terminal className="w-3.5 h-3.5" />
                  Test in API Playground
                </button>
              </div>
            )}
          </div>

          <div className="pt-3 border-t border-zinc-800 text-[11px] text-zinc-400 flex items-center justify-between font-mono">
            <span>Response Header: X-RateLimit-Remaining</span>
            <span className="text-zinc-300">{remainingNum !== null ? remainingNum : 'N/A (Awaiting request)'}</span>
          </div>
        </div>

        {/* Total Rejections Telemetry */}
        <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm flex flex-col justify-between">
          <div>
            <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2 mb-1">
              <AlertOctagon className="w-4 h-4 text-amber-400" />
              Rejection Telemetry
            </h3>
            <p className="text-xs text-zinc-400 mb-4">
              Cumulative total of requests blocked by the gateway rate limiter.
            </p>

            <div className="p-4 rounded-lg bg-zinc-950 border border-zinc-800 my-4">
              <span className="text-[10px] text-zinc-400 font-mono block">HTTP 429 REJECTIONS</span>
              <span className="text-3xl font-bold font-mono text-zinc-100 mt-1 block">
                {rateLimitRejections.toLocaleString()}
              </span>
              <span className="text-[11px] text-zinc-400 mt-1 block">
                Metric: gateway_rate_limit_rejections_total
              </span>
            </div>

            <div className="space-y-2 text-xs text-zinc-400 leading-relaxed">
              <div className="flex items-start gap-2">
                <Clock className="w-3.5 h-3.5 text-zinc-400 mt-0.5 shrink-0" />
                <span>Window expiration: 60-second TTL set upon initial request in the window.</span>
              </div>
              <div className="flex items-start gap-2">
                <Database className="w-3.5 h-3.5 text-zinc-400 mt-0.5 shrink-0" />
                <span>Atomicity: Evaluated in a single Redis round-trip via Redis Lua script.</span>
              </div>
            </div>
          </div>

          <div className="pt-3 border-t border-zinc-800 text-xs flex justify-end">
            <button
              onClick={() => onNavigate('playground')}
              className="text-xs text-indigo-400 hover:text-indigo-300 flex items-center gap-1 font-medium transition-colors"
            >
              Trigger Burst Test <ArrowRight className="w-3 h-3" />
            </button>
          </div>
        </div>
      </div>

      {/* Security & Implementation Architecture */}
      <div className="p-5 rounded-xl bg-zinc-900 border border-zinc-800 shadow-sm">
        <h3 className="text-sm font-semibold text-zinc-100 flex items-center gap-2 mb-3">
          <Shield className="w-4 h-4 text-indigo-400" />
          Zero-Leakage Key Identity Hashing
        </h3>
        <p className="text-xs text-zinc-400 leading-relaxed mb-4">
          GateKeeper enforces strict privacy when communicating with Redis. Client API keys are hashed with
          <strong className="text-zinc-200"> SHA-256</strong> before evaluating the rate limit key. Plaintext credentials
          are never stored in Redis keys, values, or debug logs.
        </p>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 text-xs font-mono">
          <div className="p-3 rounded-lg bg-zinc-950 border border-zinc-800">
            <span className="text-zinc-400 text-[10px] block mb-1">STEP 1: AUTHENTICATION</span>
            <span className="text-zinc-200">subtle.ConstantTimeCompare</span>
            <p className="text-[11px] text-zinc-400 mt-1 font-sans">
              Protects against timing attacks by verifying API keys in constant time.
            </p>
          </div>

          <div className="p-3 rounded-lg bg-zinc-950 border border-zinc-800">
            <span className="text-zinc-400 text-[10px] block mb-1">STEP 2: IDENTITY HASH</span>
            <span className="text-indigo-400">sha256.Sum256(key)</span>
            <p className="text-[11px] text-zinc-400 mt-1 font-sans">
              Hex-encoded SHA-256 string forms Redis key: <code className="text-zinc-300">rate_limit:{'{hash}'}</code>.
            </p>
          </div>

          <div className="p-3 rounded-lg bg-zinc-950 border border-zinc-800">
            <span className="text-zinc-400 text-[10px] block mb-1">STEP 3: ATOMIC EVALUATION</span>
            <span className="text-emerald-400">redis.NewScript(...)</span>
            <p className="text-[11px] text-zinc-400 mt-1 font-sans">
              Lua script increments counter, sets TTL, and calculates remaining quota atomically.
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}
