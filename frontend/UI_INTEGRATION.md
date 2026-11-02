# UI integration

This Vite app already uses React 19, TypeScript 6, and Tailwind 4 through
`@tailwindcss/vite`; no framework migration or provider is needed.

Reusable components live in `src/components/ui` (imported as `@/components/ui`).
Page sections remain in `src/components`; global styles are `src/index.css`.
The UI folder gives shadcn a predictable installation destination and separates
reusable primitives from gateway-specific page composition. `components.json`
and both TypeScript and Vite aliases are configured. Class merging is in
`src/lib/utils.ts` and includes the supplied Layro type-scale names.

The registry command requested was attempted:

```sh
npx shadcn@latest add "https://21st.dev/r/uvain/web-shot-button" --yes
```

The registry requires authentication. The local button adapts the supplied source,
using Framer Motion, clsx, and tailwind-merge. It retains glass styling, pointer
threads, press animation, ref forwarding, disabled state, and reduced-motion support.
The thread drawing implementation is simplified and bounded for performance.

The bento is adapted to GateKeeper, preserving cursor lighting, interactive charts,
terminal styling, score rings, throughput bars, and tool badges. Production usage:

```tsx
<TechStackBento metrics={metrics} health={health} timeSeries={timeSeries} demo={false} />
```

Data stays in App; no additional state provider is required. `ui/demo.tsx` exports
a standalone demonstration with explicitly simulated data. Live cards use actual
latency histogram buckets, request totals, and circuit states. No regional probes,
Lighthouse measurements, or deployment activity are claimed for the real dashboard.
The stack badges reflect this repository, not the sample's Next.js/Postgres stack.
No stock images are needed: the component uses Lucide icons, SVG charts, and canvas.
The layout uses four desktop columns, two tablet columns, and one mobile column.
