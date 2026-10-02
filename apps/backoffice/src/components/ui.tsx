import Link from "next/link";
import type { ComponentProps, ReactNode } from "react";

// Small building blocks shared by every page. Plain Tailwind, no UI kit.

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-ink-soft">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function Card({ title, children, className = "", actions }: { title?: ReactNode; children: ReactNode; className?: string; actions?: ReactNode }) {
  return (
    <section className={`rounded-xl border border-line bg-white ${className}`}>
      {title && (
        <header className="flex items-center justify-between gap-2 border-b border-line px-4 py-3">
          <h2 className="text-sm font-semibold">{title}</h2>
          {actions}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

type Tone = "neutral" | "good" | "warn" | "bad" | "info";
const tones: Record<Tone, string> = {
  neutral: "bg-chalk text-ink-soft ring-line",
  good: "bg-curry-50 text-curry-700 ring-curry-700/20",
  warn: "bg-marigold-50 text-marigold-700 ring-marigold-400/40",
  bad: "bg-chilli-50 text-chilli-700 ring-chilli-700/20",
  info: "bg-neel-50 text-neel-700 ring-neel-700/15",
};

export function Badge({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${tones[tone]}`}>
      {children}
    </span>
  );
}

export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: ReactNode }) {
  return (
    <div className="rounded-xl border border-line bg-white px-4 py-3">
      <div className="text-xs font-medium uppercase tracking-wide text-ink-faint">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      {hint && <div className="mt-0.5 text-xs text-ink-soft">{hint}</div>}
    </div>
  );
}

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="rounded-xl border border-dashed border-line bg-white px-6 py-10 text-center">
      <p className="font-medium">{title}</p>
      {children && <div className="mt-1 text-sm text-ink-soft">{children}</div>}
    </div>
  );
}

const buttons = {
  primary: "bg-neel-700 text-white hover:bg-neel-800 disabled:bg-neel-200",
  secondary: "bg-white text-ink ring-1 ring-inset ring-line hover:bg-chalk disabled:text-ink-faint",
  danger: "bg-chilli-700 text-white hover:bg-chilli-700/90 disabled:opacity-50",
  ghost: "text-neel-700 hover:bg-neel-50",
};
export type ButtonVariant = keyof typeof buttons;

export function buttonClass(variant: ButtonVariant = "primary", size: "sm" | "md" = "md") {
  const s = size === "sm" ? "px-2.5 py-1 text-xs" : "px-3.5 py-2 text-sm";
  return `inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg font-medium transition-colors ${s} ${buttons[variant]}`;
}

export function Button({ variant = "primary", size = "md", className = "", ...rest }: ComponentProps<"button"> & { variant?: ButtonVariant; size?: "sm" | "md" }) {
  return <button className={`${buttonClass(variant, size)} ${className}`} {...rest} />;
}

export function LinkButton({ variant = "secondary", size = "md", className = "", ...rest }: ComponentProps<typeof Link> & { variant?: ButtonVariant; size?: "sm" | "md" }) {
  return <Link className={`${buttonClass(variant, size)} ${className}`} {...rest} />;
}

export const inputClass =
  "block w-full rounded-lg border border-line bg-white px-3 py-2 text-sm placeholder:text-ink-faint focus:border-neel-600 focus:outline-none focus:ring-2 focus:ring-neel-600/20";

export function Field({ label, hint, children, className = "" }: { label: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={`block ${className}`}>
      <span className="mb-1 block text-xs font-medium text-ink-soft">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-xs text-ink-faint">{hint}</span>}
    </label>
  );
}

export function Input(props: ComponentProps<"input">) {
  return <input {...props} className={`${inputClass} ${props.className ?? ""}`} />;
}

export function Select(props: ComponentProps<"select">) {
  return <select {...props} className={`${inputClass} ${props.className ?? ""}`} />;
}

export function Textarea(props: ComponentProps<"textarea">) {
  return <textarea {...props} className={`${inputClass} ${props.className ?? ""}`} />;
}

export function Table({ children }: { children: ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-line bg-white">
      <table className="w-full text-left text-sm tabular-nums">{children}</table>
    </div>
  );
}

export const th = "border-b border-line bg-chalk px-4 py-2 text-xs font-medium uppercase tracking-wide text-ink-faint";
export const td = "border-b border-line/70 px-4 py-2.5 align-top";
