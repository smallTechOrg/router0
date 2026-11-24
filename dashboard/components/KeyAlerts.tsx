import Link from 'next/link';
import type { ProviderKey } from '@/lib/api';
import { providerLabelFor } from '@/lib/api';
import KeyStateBadge from '@/components/KeyStateBadge';
import { BUDGET_WARN_RATIO, usd } from '@/components/SpendBar';

export type KeyAlert = { key: ProviderKey; message: string; severity: 'error' | 'warning' };

/**
 * Keys an owner should look at: auth errors, keys blocked by credits or budget,
 * rate-limited keys, and keys at 80 % or more of their monthly budget.
 * Transient cooldowns (seconds long) are not alerts.
 */
export function keyAlerts(keys: ProviderKey[]): KeyAlert[] {
  const out: KeyAlert[] = [];
  for (const k of keys) {
    if (k.status === 'disabled') continue;
    const budget = k.monthly_budget_usd;
    const spend = k.month_spend_usd ?? 0;
    if (k.status === 'error') {
      out.push({ key: k, severity: 'error', message: 'The provider rejected this key. Fix it, then run Test on the keys page.' });
    } else if (k.state?.blocked && k.state.reason === 'credits_exhausted') {
      out.push({ key: k, severity: 'error', message: 'Out of credits: skipped until the provider has balance again.' });
    } else if (k.state?.blocked && k.state.reason === 'budget_exhausted') {
      out.push({ key: k, severity: 'error', message: `Monthly budget reached (${usd(spend)} of ${usd(budget ?? 0)}): skipped until the 1st (UTC) or until the budget is raised.` });
    } else if (k.state?.blocked && k.state.reason === 'rate_limited') {
      out.push({ key: k, severity: 'warning', message: `Rate limited${k.state.retry_after_s ? ` for ${k.state.retry_after_s}s` : ''}.` });
    } else if (typeof budget === 'number' && budget > 0 && spend >= budget * BUDGET_WARN_RATIO) {
      out.push({ key: k, severity: 'warning', message: `${Math.floor((spend / budget) * 100)}% of the monthly budget used (${usd(spend)} of ${usd(budget)}).` });
    }
  }
  const rank = { error: 0, warning: 1 };
  return out.sort((a, b) => rank[a.severity] - rank[b.severity]);
}

export default function KeyAlerts({ keys }: { keys: ProviderKey[] }) {
  const alerts = keyAlerts(keys);
  if (alerts.length === 0) return null;
  return (
    <section aria-labelledby="key-alerts" className="rounded-xl border border-amber-500/30 bg-amber-500/5">
      <div className="flex items-center justify-between border-b border-amber-500/20 px-5 py-3">
        <h2 id="key-alerts" className="text-sm font-semibold text-amber-200">Keys that need attention ({alerts.length})</h2>
        <Link href="/dashboard/keys" className="text-sm text-indigo-400 hover:text-indigo-300">Provider keys →</Link>
      </div>
      <ul className="divide-y divide-neutral-800">
        {alerts.map(({ key, message }) => (
          <li key={key.id} className="flex flex-wrap items-center gap-3 px-5 py-3">
            <div className="w-40 min-w-0">
              <p className="truncate text-sm font-medium text-neutral-100">{key.label}</p>
              <p className="text-xs text-neutral-500">{providerLabelFor(key.provider)}</p>
            </div>
            <p className="min-w-0 flex-1 text-sm text-neutral-300">{message}</p>
            <KeyStateBadge keyInfo={key} />
          </li>
        ))}
      </ul>
    </section>
  );
}
