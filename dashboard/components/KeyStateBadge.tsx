import clsx from 'clsx';
import type { ProviderKey } from '@/lib/api';

const base = 'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset';

/** Badge for a provider key: ok / rate-limited until / out of credits / budget reached / auth error. */
export default function KeyStateBadge({ keyInfo }: { keyInfo: Pick<ProviderKey, 'status' | 'state'> }) {
  const { status, state } = keyInfo;
  if (status === 'disabled') {
    return <span className={clsx(base, 'bg-neutral-700/30 text-neutral-400 ring-neutral-600/30')}>disabled</span>;
  }
  if (status === 'error') {
    return <span className={clsx(base, 'bg-red-500/15 text-red-400 ring-red-500/20')}>auth error</span>;
  }
  if (state?.blocked) {
    if (state.reason === 'rate_limited') {
      const s = state.retry_after_s ?? 0;
      return (
        <span className={clsx(base, 'bg-amber-500/15 text-amber-400 ring-amber-500/20')}>
          rate limited{s > 0 ? ` · ${s}s` : ''}
        </span>
      );
    }
    if (state.reason === 'budget_exhausted' || state.reason === 'budget') {
      return <span className={clsx(base, 'bg-orange-500/15 text-orange-400 ring-orange-500/20')}>budget reached</span>;
    }
    if (
      state.reason === 'credits_exhausted' ||
      state.reason === 'insufficient_credits' ||
      state.reason === 'out_of_credits'
    ) {
      return <span className={clsx(base, 'bg-red-500/15 text-red-400 ring-red-500/20')}>out of credits</span>;
    }
    return <span className={clsx(base, 'bg-neutral-500/15 text-neutral-300 ring-neutral-500/20')}>cooling down</span>;
  }
  return <span className={clsx(base, 'bg-emerald-500/15 text-emerald-400 ring-emerald-500/20')}>ok</span>;
}
