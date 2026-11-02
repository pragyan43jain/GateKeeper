"use client";
import * as React from "react";
import { motion, useReducedMotion } from "framer-motion";
import { cn } from "@/lib/utils";
export interface WebShotButtonProps extends Omit<
  React.ButtonHTMLAttributes<HTMLButtonElement>,
  | "color"
  | "onDrag"
  | "onDragStart"
  | "onDragEnd"
  | "onAnimationStart"
  | "onAnimationEnd"
> {
  size?: "md" | "lg";
  color?: string;
  reach?: number;
  threads?: number;
}
const SPRING = {
  type: "spring",
  stiffness: 420,
  damping: 34,
  mass: 0.8,
} as const;
function lightness(c: string) {
  if (c.startsWith("#")) {
    const h =
      c.length === 4
        ? c
            .slice(1)
            .split("")
            .map((x) => x + x)
            .join("")
        : c.slice(1, 7);
    const v = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
    return (0.2126 * v[0] + 0.7152 * v[1] + 0.0722 * v[2]) / 255;
  }
  const n = (c.match(/[\d.]+/g) || []).map(Number);
  return n.length >= 3
    ? (0.2126 * n[0] + 0.7152 * n[1] + 0.0722 * n[2]) / 255
    : 0;
}
export const WebShotButton = React.forwardRef<
  HTMLButtonElement,
  WebShotButtonProps
>(function WebShotButton(
  {
    size = "lg",
    color,
    reach = 140,
    threads = 5,
    className,
    style,
    children,
    disabled,
    ...rest
  },
  ref,
) {
  const reduced = useReducedMotion();
  const button = React.useRef<HTMLButtonElement | null>(null);
  const canvas = React.useRef<HTMLCanvasElement | null>(null);
  const setRefs = React.useCallback(
    (node: HTMLButtonElement | null) => {
      button.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );
  const tone = color === "clear" ? undefined : color || "#E0242F";
  React.useEffect(() => {
    const btn = button.current,
      cv = canvas.current,
      ctx = cv?.getContext("2d");
    if (reduced || disabled || !btn || !cv || !ctx) return;
    const distance = Math.max(0, Math.min(reach, 400)),
      pad = distance + 24,
      n = Math.max(1, Math.min(threads, 20));
    let frame = 0,
      extension = 0,
      w = 0,
      h = 0,
      dpr = 1,
      known = false;
    let px = 0,
      py = 0;
    const tips = Array.from({ length: n }, () => ({ x: 0, y: 0 }));
    const measure = () => {
      w = btn.offsetWidth;
      h = btn.offsetHeight;
      dpr = Math.min(devicePixelRatio || 1, 2);
      cv.width = (w + pad * 2) * dpr;
      cv.height = (h + pad * 2) * dpr;
      Object.assign(cv.style, {
        width: `${w + pad * 2}px`,
        height: `${h + pad * 2}px`,
        left: `${-pad}px`,
        top: `${-pad}px`,
      });
    };
    const tick = () => {
      frame = 0;
      const r = btn.getBoundingClientRect();
      const x = px - r.left,
        y = py - r.top;
      const radius = Math.min(w, h) / 2;
      const cx = Math.min(Math.max(x, radius), w - radius);
      const cy = h / 2;
      const dx = x - cx,
        dy = y - cy;
      const length = Math.hypot(dx, dy) || 1;
      const outside = length - radius;
      const active = known && outside > 1 && outside < distance;
      extension += ((active ? 1 : 0) - extension) * (active ? 0.12 : 0.22);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w + pad * 2, h + pad * 2);
      ctx.strokeStyle = tone || "#a9ffc8";
      ctx.lineWidth = 1;
      ctx.globalAlpha = 0.75 * extension;
      for (let i = 0; i < n; i++) {
        const angle = Math.atan2(dy, dx) + (i - (n - 1) / 2) * 0.13;
        const ax = cx + Math.cos(angle) * (radius - 2),
          ay = cy + Math.sin(angle) * (radius - 2);
        const tip = tips[i];
        const tx = ax + (x - ax) * extension,
          ty = ay + (y - ay) * extension;
        tip.x += (tx - tip.x) * (0.18 + i * 0.025);
        tip.y += (ty - tip.y) * (0.18 + i * 0.025);
        ctx.beginPath();
        ctx.moveTo(pad + ax, pad + ay);
        ctx.quadraticCurveTo(
          pad + (ax + tip.x) / 2,
          pad + (ay + tip.y) / 2 + 14 * extension,
          pad + tip.x,
          pad + tip.y,
        );
        ctx.stroke();
      }
      if (active || extension > 0.003) frame = requestAnimationFrame(tick);
    };
    const kick = () => {
      if (!frame) frame = requestAnimationFrame(tick);
    };
    const move = (e: PointerEvent) => {
      if (e.pointerType === "touch" && e.buttons === 0) return;
      px = e.clientX;
      py = e.clientY;
      known = true;
      const r = btn.getBoundingClientRect();
      if (
        frame ||
        (px > r.left - distance &&
          px < r.right + distance &&
          py > r.top - distance &&
          py < r.bottom + distance)
      )
        kick();
    };
    const end = (e: PointerEvent) => {
      if (e.pointerType === "touch" || !e.relatedTarget) {
        known = false;
        kick();
      }
    };
    measure();
    const observer = new ResizeObserver(() => {
      measure();
      kick();
    });
    observer.observe(btn);
    window.addEventListener("pointermove", move, { passive: true });
    window.addEventListener("pointerdown", move, { passive: true });
    window.addEventListener("pointerup", end, { passive: true });
    window.addEventListener("pointercancel", end, { passive: true });
    document.addEventListener("pointerout", end, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerdown", move);
      window.removeEventListener("pointerup", end);
      window.removeEventListener("pointercancel", end);
      document.removeEventListener("pointerout", end);
    };
  }, [reduced, reach, threads, disabled, tone]);
  return (
    <span className="web-shot-wrap">
      {!reduced && !disabled && (
        <canvas
          ref={canvas}
          aria-hidden
          className="pointer-events-none absolute"
        />
      )}
      <motion.button
        ref={setRefs}
        type="button"
        disabled={disabled}
        whileTap={reduced || disabled ? undefined : { scale: 0.96 }}
        transition={SPRING}
        className={cn(
          "web-shot-button relative inline-flex items-center justify-center gap-2 rounded-full font-semibold disabled:opacity-50 disabled:cursor-not-allowed",
          size === "md" ? "h-11 px-6 text-sm" : "h-14 px-8 text-base",
          className,
        )}
        style={{
          ...(tone
            ? {
                backgroundColor: tone,
                color: lightness(tone) > 0.62 ? "#101510" : "#fff",
              }
            : {}),
          ...style,
        }}
        {...rest}
      >
        <span aria-hidden className="web-shot-glass" />
        <span className="relative inline-flex items-center gap-2">
          {children}
        </span>
      </motion.button>
    </span>
  );
});
export { WebShotButton as Component };
