import clsx from 'clsx';
import type { TrailStep } from '@/lib/api';

/** Horizontal chain of attempts: groq/groq-a ✕ rate_limit 429 (95 ms) → groq/groq-b ✓ (717 ms). */
export default function FailoverTrail({ trail }: { trail: TrailStep[] | null | undefined }) {
  if (!trail || trail.length === 0) {
    return <p className="text-xs text-neutral-500">No attempts recorded for this request.</p>;
  }
  const steps = [...trail].sort((a, b) => a.order - b.order);
  return (
    <ol className="flex flex-wrap items-center gap-2 text-xs" aria-label="Failover trail">
      {steps.map((s, i) => (
        <li key={`${s.order}-${i}`} className="flex items-center gap-2">
          {i > 0 && <span aria-hidden className="text-neutral-600">→</span>}
          <span
            className={clsx(
              'rounded-md border px-2 py-1 font-mono',
              s.outcome === 'success' && 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
              s.outcome === 'error' && 'border-red-500/30 bg-red-500/10 text-red-300',
              s.outcome === 'skipped' && 'border-neutral-600 bg-neutral-800 text-neutral-400',
            )}
          >
            {s.provider}/{s.key_label}{' '}
            {s.outcome === 'success' && <span aria-label="success">✓</span>}
            {s.outcome === 'error' && (
              <span>
                <span aria-label="failed">✕</span> {s.class}
                {s.http_status ? ` ${s.http_status}` : ''}
              </span>
            )}
            {s.outcome === 'skipped' && (
              <span>
                <span aria-hidden>⤼</span> skipped: {s.class}
                {s.retry_after_s ? ` ${s.retry_after_s}s` : ''}
              </span>
            )}
            {s.latency_ms != null && <span className="text-neutral-400"> ({s.latency_ms} ms)</span>}
          </span>
        </li>
      ))}
    </ol>
  );
}
