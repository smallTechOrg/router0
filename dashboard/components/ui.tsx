'use client';
import { useEffect, useState } from 'react';
import clsx from 'clsx';

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 className="text-2xl font-bold text-white">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-neutral-400">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function ErrorBanner({ message, onRetry }: { message?: string; onRetry?: () => void }) {
  if (!message) return null;
  return (
    <div role="alert" className="mb-4 flex items-start justify-between gap-3 rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300">
      <span className="break-words">{message}</span>
      {onRetry && (
        <button onClick={onRetry} className="shrink-0 text-xs font-medium text-red-200 underline hover:text-white">Retry</button>
      )}
    </div>
  );
}

export function Notice({ message }: { message?: string }) {
  const [shown, setShown] = useState(message);
  useEffect(() => {
    setShown(message);
    if (!message) return;
    const t = setTimeout(() => setShown(''), 4000);
    return () => clearTimeout(t);
  }, [message]);
  if (!shown) return null;
  return (
    <div role="status" className="mb-4 rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-300">
      {shown}
    </div>
  );
}

export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div role="status" aria-live="polite" className="px-6 py-10 text-center text-sm text-neutral-500">{label}</div>
  );
}

export function EmptyState({ title, hint, action }: { title: string; hint?: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="px-6 py-12 text-center">
      <p className="text-sm font-medium text-neutral-300">{title}</p>
      {hint && <p className="mt-1 text-sm text-neutral-500">{hint}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Card({ title, children, className, actions }: { title?: string; children: React.ReactNode; className?: string; actions?: React.ReactNode }) {
  return (
    <section className={clsx('rounded-xl border border-neutral-800 bg-neutral-900', className)}>
      {title && (
        <div className="flex items-center justify-between gap-2 border-b border-neutral-800 px-5 py-3.5">
          <h2 className="text-sm font-semibold text-neutral-200">{title}</h2>
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}

export const inputCls =
  'w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 disabled:opacity-50';
export const btnPrimary =
  'rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-400 disabled:opacity-50 transition-colors';
export const btnGhost =
  'rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-1.5 text-xs font-medium text-neutral-300 hover:text-white hover:bg-neutral-700 focus:outline-none focus:ring-2 focus:ring-indigo-400 disabled:opacity-40 disabled:cursor-not-allowed transition-colors';
export const btnDanger =
  'rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-1.5 text-xs font-medium text-red-300 hover:bg-red-500/20 focus:outline-none focus:ring-2 focus:ring-red-400 disabled:opacity-40 transition-colors';
export const thCls = 'px-4 py-3 text-left text-xs font-medium uppercase text-neutral-400 whitespace-nowrap';
export const tdCls = 'px-4 py-3 text-neutral-300';

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className={btnGhost}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          setTimeout(() => setDone(false), 2000);
        } catch { /* clipboard unavailable */ }
      }}
    >
      {done ? 'Copied' : label}
    </button>
  );
}

export function fmtTime(s?: string | null) {
  if (!s) return '—';
  const d = new Date(s);
  return isNaN(d.getTime()) ? s : d.toLocaleString();
}
