import { useEffect, useId, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import {
  Activity,
  Atom,
  Boxes,
  Braces,
  Container,
  Database,
  Gauge,
  Radio,
  Server,
  ShieldCheck,
} from "lucide-react";
import type {
  GatewayHealth,
  GatewayMetricsSummary,
  TimeSeriesPoint,
} from "@/types";
import { TrafficChart } from "@/components/TrafficChart";

type Accent = "indigo" | "cyan" | "emerald" | "fuchsia" | "amber";
const ACCENTS: Record<Accent, string> = {
  indigo: "50,245,155",
  cyan: "50,245,155",
  emerald: "50,245,155",
  fuchsia: "255,48,79",
  amber: "255,101,122",
};
export function GlowCard({
  accent = "emerald",
  className = "",
  children,
}: {
  accent?: Accent;
  className?: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const move = (x: number, y: number) => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    el.style.setProperty("--mx", `${x - r.left}px`);
    el.style.setProperty("--my", `${y - r.top}px`);
  };
  return (
    <div
      ref={ref}
      className={`bento-glow ${className}`}
      style={{ "--glow": ACCENTS[accent] } as CSSProperties}
      onPointerMove={(e) => move(e.clientX, e.clientY)}
    >
      <div className="bento-surface" />
      <div className="bento-grid-light" />
      <div className="bento-content">{children}</div>
    </div>
  );
}
function CardTitle({
  children,
  right,
}: {
  children: ReactNode;
  right?: ReactNode;
}) {
  return (
    <div className="bento-title">
      <h3>{children}</h3>
      {right}
    </div>
  );
}
function LiveBadge({ demo = false }: { demo?: boolean }) {
  return (
    <span className="bento-live">
      <i />
      {demo ? "SIMULATED" : "LATEST SCRAPE"}
    </span>
  );
}
function useAnimatedNumber(target: number) {
  const [value, setValue] = useState(target);
  const current = useRef(target);
  useEffect(() => {
    let frame = 0;
    const from = current.current;
    const start = performance.now();
    const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const tick = (now: number) => {
      const t = reduced ? 1 : Math.min((now - start) / 600, 1);
      current.current = from + (target - from) * (1 - Math.pow(1 - t, 3));
      setValue(current.current);
      if (t < 1) frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [target]);
  return value;
}
const TECHS = [
  {
    name: "React",
    version: "19",
    desc: "Component-driven dashboard and interactive controls.",
    icon: Atom,
  },
  {
    name: "Go",
    version: "1.26",
    desc: "HTTP gateway, reverse proxy and per-backend circuit breakers.",
    icon: Braces,
  },
  {
    name: "TypeScript",
    version: "6",
    desc: "Typed interfaces across the frontend.",
    icon: Boxes,
  },
  {
    name: "Tailwind",
    version: "4",
    desc: "Responsive utilities and shared design tokens.",
    icon: Gauge,
  },
  {
    name: "Redis",
    version: "7",
    desc: "Atomic Redis sliding-window rate limiting with Lua.",
    icon: Database,
  },
  {
    name: "Prometheus",
    version: "",
    desc: "Metrics scraping and historical request rates.",
    icon: Activity,
  },
  {
    name: "Docker",
    version: "",
    desc: "Repeatable builds and service orchestration.",
    icon: Container,
  },
  {
    name: "Caddy",
    version: "2",
    desc: "HTTPS termination and dashboard delivery.",
    icon: ShieldCheck,
  },
];
function StackCard() {
  const [active, setActive] = useState<number | null>(null);
  return (
    <GlowCard accent="fuchsia" className="bento-stack">
      <CardTitle
        right={<span className="bento-muted">08 / CORE TECHNOLOGIES</span>}
      >
        The stack behind every request
      </CardTitle>
      <div className="stack-tools">
        {TECHS.map((tech, i) => (
          <button
            key={tech.name}
            onMouseEnter={() => setActive(i)}
            onMouseLeave={() => setActive(null)}
            onFocus={() => setActive(i)}
            onBlur={() => setActive(null)}
            onClick={() => setActive(i)}
            aria-pressed={active === i}
          >
            <tech.icon size={28} />
            <strong>{tech.name}</strong>
            <small>
              {tech.version ? `v${tech.version}` : "Infrastructure"}
            </small>
          </button>
        ))}
      </div>
      <p className="stack-description" aria-live="polite">
        {active === null
          ? "Explore the tools that power your gateway. Hover, tap, or use your keyboard."
          : TECHS[active].desc}
      </p>
    </GlowCard>
  );
}
const SCRIPT = [
  "$ npm ci",
  "✓ Dependencies installed",
  "$ npm run build",
  "› Compiling components…",
  "✓ Type check passed",
  "✓ Production assets built",
  "$ docker compose up -d",
  "› Starting services…",
  "✓ Gateway ready",
];
function TerminalCard({
  metrics,
  health,
  demo,
}: {
  metrics?: GatewayMetricsSummary | null;
  health?: GatewayHealth | null;
  demo: boolean;
}) {
  const [step, setStep] = useState(1);
  useEffect(() => {
    if (!demo) return;
    const id = setInterval(
      () => setStep((s) => (s >= SCRIPT.length + 3 ? 1 : s + 1)),
      1000,
    );
    return () => clearInterval(id);
  }, [demo]);
  const lines = demo
    ? SCRIPT.slice(0, step)
    : [
        "$ gatekeeper / snapshot",
        `› health: ${health?.status || "checking"}`,
        `› requests: ${metrics?.totalRequests.toLocaleString() ?? "awaiting metrics"}`,
        `› upstreams: ${metrics?.backends.length ?? "—"}`,
        `› rejected: ${metrics?.rateLimitRejections ?? "—"}`,
        ...(metrics?.backends.map(
          (b) =>
            `${b.state === "closed" ? "✓" : "!"} ${b.displayName}: ${b.state}`,
        ) || []),
        `› scraped: ${metrics?.scrapedAt || "pending"}`,
      ];
  return (
    <GlowCard accent="fuchsia" className="bento-terminal">
      <CardTitle
        right={
          <span className="terminal-lights">
            <i />
            <i />
            <i />
          </span>
        }
      >
        {demo ? "deploy.sh · demo" : "gateway / snapshot"}
      </CardTitle>
      <div
        className="terminal-body"
        aria-label={
          demo ? "Simulated deployment log" : "Latest gateway snapshot"
        }
      >
        {lines.map((line, i) => (
          <div
            key={`${i}-${line}`}
            className={
              line.startsWith("!")
                ? "terminal-error"
                : line.startsWith("✓")
                  ? "terminal-ok"
                  : ""
            }
          >
            {line}
          </div>
        ))}
        <span className="terminal-cursor" />
      </div>
      <p className="bento-muted">
        {demo
          ? "Illustrative animation. No deployment is executed."
          : "A snapshot of real metrics, refreshed with the dashboard."}
      </p>
    </GlowCard>
  );
}
function ScoreCard({
  metrics,
  demo,
}: {
  metrics?: GatewayMetricsSummary | null;
  demo: boolean;
}) {
  const total = metrics?.totalRequests || 0;
  const healthy =
    metrics?.backends.filter((b) => b.state === "closed").length || 0;
  const scores = demo
    ? [
        { label: "Perf", value: 98 },
        { label: "A11y", value: 100 },
        { label: "Best", value: 96 },
        { label: "SEO", value: 100 },
      ]
    : [
        {
          label: "HTTP 2xx",
          value: total
            ? (100 *
                Object.entries(metrics!.requestsByCode)
                  .filter(([c]) => c.startsWith("2"))
                  .reduce((n, [, v]) => n + v, 0)) /
              total
            : null,
        },
        {
          label: "Closed circuits",
          value: metrics?.backends.length
            ? (100 * healthy) / metrics.backends.length
            : null,
        },
      ];
  return (
    <GlowCard>
      <CardTitle right={<ShieldCheck size={16} />}>
        {demo ? "Lighthouse · example scores" : "Response quality"}
      </CardTitle>
      <div className="bento-scores">
        {scores.map((s) => (
          <div key={s.label}>
            <svg
              viewBox="0 0 64 64"
              role="img"
              aria-label={`${s.label}: ${s.value === null ? "no data" : s.value.toFixed(1) + " percent"}`}
            >
              <circle
                cx="32"
                cy="32"
                r="26"
                fill="none"
                stroke="#25292a"
                strokeWidth="4"
              />
              <circle
                cx="32"
                cy="32"
                r="26"
                fill="none"
                stroke="#32f59b"
                strokeWidth="4"
                strokeDasharray="163.36"
                strokeDashoffset={163.36 * (1 - (s.value || 0) / 100)}
                transform="rotate(-90 32 32)"
              />
              <text
                x="32"
                y="36"
                textAnchor="middle"
                fill="white"
                fontSize="12"
              >
                {s.value === null ? "—" : Math.round(s.value)}
              </text>
            </svg>
            <small>{s.label}</small>
          </div>
        ))}
      </div>
    </GlowCard>
  );
}
function ThroughputCard({
  points,
  demo,
}: {
  points: TimeSeriesPoint[];
  demo: boolean;
}) {
  const bars = points.slice(-16);
  const last = bars.at(-1)?.totalRate;
  const value = useAnimatedNumber(last || 0);
  const max = Math.max(...bars.map((p) => p.totalRate), 0.001);
  return (
    <GlowCard>
      <CardTitle right={<LiveBadge demo={demo} />}>Requests / second</CardTitle>
      <div className="bento-number">
        {last === undefined ? "—" : value.toFixed(3)}
        <small>req/s</small>
      </div>
      <div className="bento-bars">
        {bars.map((p, i) => (
          <div
            key={i}
            tabIndex={0}
            aria-label={`${p.time}: ${p.totalRate.toFixed(3)} requests per second`}
            title={`${p.time} · ${p.totalRate.toFixed(3)} req/s`}
            style={{ height: `${Math.max((p.totalRate / max) * 100, 2)}%` }}
          />
        ))}
      </div>
    </GlowCard>
  );
}
function LatencyCard({
  metrics,
  demo,
}: {
  metrics?: GatewayMetricsSummary | null;
  demo: boolean;
}) {
  const id = useId();
  const [sample, setSample] = useState(40);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    if (!demo) return;
    const timer = setInterval(
      () =>
        setSample((n) =>
          Math.max(22, Math.min(88, n + (Math.random() - 0.5) * 16)),
        ),
      1200,
    );
    return () => clearInterval(timer);
  }, [demo]);
  const latency = demo ? sample : metrics?.avgLatencyMs;
  const animated = useAnimatedNumber(latency || 0);
  const buckets =
    metrics?.rawSamples.filter(
      (s) =>
        s.name === "gateway_request_duration_seconds_bucket" &&
        s.labels.le !== "+Inf",
    ) || [];
  const totals = new Map<number, number>();
  buckets.forEach((s) => {
    const bound = Number(s.labels.le);
    totals.set(bound, (totals.get(bound) || 0) + s.value);
  });
  const values = demo
    ? Array.from({ length: 40 }, (_, i) => ({
        label: `${Math.round((39 - i) * 1.2)}s ago`,
        value: 40 + Math.sin(i * 0.5) * 9 + sample / 10,
      }))
    : Array.from(totals)
        .sort((a, b) => a[0] - b[0])
        .map(([bound, value]) => ({ label: `≤ ${bound * 1000} ms`, value }));
  const max = Math.max(...values.map((v) => v.value), 1);
  const points = values.map((v, i) => ({
    x: 5 + (i / Math.max(values.length - 1, 1)) * 90,
    y: 90 - (v.value / max) * 75,
  }));
  const path = points.map((p, i) => `${i ? "L" : "M"}${p.x} ${p.y}`).join(" ");
  return (
    <GlowCard className="bento-latency">
      <CardTitle right={<LiveBadge demo={demo} />}>
        {demo ? "Server latency · simulated" : "Gateway response latency"}
      </CardTitle>
      <div className="bento-number">
        {latency === null || latency === undefined ? "—" : animated.toFixed(2)}
        <small>ms {demo ? "" : "average"}</small>
      </div>
      <p className="bento-muted">
        {demo
          ? "Illustrative values, not production measurements."
          : "Cumulative duration histogram · all recorded requests"}
      </p>
      <div
        className="latency-plot"
        onPointerMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          setHover(
            Math.min(
              values.length - 1,
              Math.max(
                0,
                Math.round(
                  ((e.clientX - r.left) / r.width) * (values.length - 1),
                ),
              ),
            ),
          );
        }}
        onPointerLeave={() => setHover(null)}
      >
        <svg
          viewBox="0 0 100 100"
          preserveAspectRatio="none"
          role="img"
          aria-label={
            demo
              ? "Simulated latency chart"
              : "Cumulative requests by latency upper bound"
          }
        >
          <defs>
            <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
              <stop stopColor="#32f59b" stopOpacity=".3" />
              <stop offset="1" stopColor="#32f59b" stopOpacity="0" />
            </linearGradient>
          </defs>
          {[25, 50, 75].map((y) => (
            <line
              key={y}
              x1="0"
              x2="100"
              y1={y}
              y2={y}
              stroke="#ffffff12"
              vectorEffect="non-scaling-stroke"
            />
          ))}
          {values.length > 0 && (
            <>
              <path d={`${path} L95 100 L5 100 Z`} fill={`url(#${id})`} />
              <path
                d={path}
                fill="none"
                stroke="#32f59b"
                strokeWidth="2"
                vectorEffect="non-scaling-stroke"
              />
            </>
          )}
        </svg>
        {!values.length && <span>Waiting for latency observations</span>}
      </div>
      <div className="latency-readout">
        {hover !== null && values[hover]
          ? `${values[hover].label} · ${demo ? values[hover].value.toFixed(1) + " ms" : values[hover].value.toLocaleString() + " requests"}`
          : "Hover to inspect the distribution"}
      </div>
      <div className="bento-node-list">
        {metrics?.backends.map((b) => (
          <div key={b.id}>
            <Server size={14} />
            <span>{b.displayName}</span>
            <strong className={b.state === "closed" ? "" : "terminal-error"}>
              {b.state}
            </strong>
          </div>
        ))}
      </div>
    </GlowCard>
  );
}
export interface TechStackBentoProps {
  metrics?: GatewayMetricsSummary | null;
  health?: GatewayHealth | null;
  timeSeries?: TimeSeriesPoint[] | null;
  demo?: boolean;
}
export default function TechStackBento({
  metrics,
  health,
  timeSeries,
  demo = true,
}: TechStackBentoProps) {
  const points = timeSeries || [];
  return (
    <section className="tech-bento">
      <div className="bento-section-heading">
        <div>
          <div className="eyebrow">
            {demo ? "COMPONENT SHOWCASE · SIMULATED DATA" : "THE ENGINE ROOM"}
          </div>
          <h2>
            Built to perform.
            <br />
            <span>Designed to be understood.</span>
          </h2>
        </div>
        <p>
          {demo
            ? "Interactive component preview. Scores, traffic, and deployment output are illustrative."
            : "From incoming traffic to upstream health. The entire system, brought into focus."}
        </p>
      </div>
      <div className="tech-bento-grid">
        <LatencyCard metrics={metrics} demo={demo} />
        <TerminalCard metrics={metrics} health={health} demo={demo} />
        <ScoreCard metrics={metrics} demo={demo} />
        <ThroughputCard
          points={
            demo
              ? Array.from(
                  { length: 16 },
                  (_, i) =>
                    ({
                      time: `Sample ${i + 1}`,
                      totalRate: 1500 + Math.sin(i * 0.8) * 300,
                    }) as TimeSeriesPoint,
                )
              : points
          }
          demo={demo}
        />
        <StackCard />
      </div>
      {!demo && <TrafficChart points={timeSeries || null} />}
      <footer className="bento-footer">
        <span>
          <Radio size={13} /> GATEKEEPER / INFRASTRUCTURE CONSOLE
        </span>
        <span>Observe. Protect. Route.</span>
      </footer>
    </section>
  );
}
