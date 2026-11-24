import clsx from 'clsx';

/** Share of a monthly budget at which a key is flagged "near budget". */
export const BUDGET_WARN_RATIO = 0.8;

export const usd = (v: number) => (v >= 1 ? `$${v.toFixed(2)}` : v >= 0.01 ? `$${v.toFixed(4)}` : `$${v.toFixed(6)}`);

/** Month-to-date estimated spend, with a bar against the budget when one is set. */
export default function SpendBar({ spend, budget, className }: { spend: number; budget?: number | null; className?: string }) {
  const hasBudget = typeof budget === 'number';
  const pct = typeof budget === 'number' ? (budget > 0 ? Math.min(100, (spend / budget) * 100) : 100) : 0;
  const color = pct >= 100 ? 'bg-red-500' : pct >= BUDGET_WARN_RATIO * 100 ? 'bg-amber-500' : 'bg-emerald-500';
  return (
    <div className={clsx('min-w-[10rem]', className)}>
      <p className="text-xs text-neutral-400">
        <span className="font-mono text-neutral-200">{usd(spend)}</span>
        {typeof budget === 'number'
          ? <> of <span className="font-mono text-neutral-200">{usd(budget)}</span> this month</>
          : ' this month · no budget'}
        <span className="text-neutral-600"> (estimated)</span>
      </p>
      {hasBudget && (
        <div
          className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-neutral-800"
          role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(pct)}
          aria-label="Month-to-date spend against budget"
        >
          <div className={clsx('h-full rounded-full', color)} style={{ width: `${pct}%` }} />
        </div>
      )}
    </div>
  );
}
