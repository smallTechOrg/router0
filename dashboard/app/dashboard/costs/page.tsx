'use client';
import { useAuth, useLoader } from '@/lib/hooks';
import { getCosts } from '@/lib/api';
import { Card, EmptyState, ErrorBanner, Loading, PageHeader, tdCls, thCls } from '@/components/ui';

// api.md only says: grouped by (provider, canonical model, key label) over the last 30 days.
// The response is parsed defensively: the first array-valued field is treated as the rows.
type Row = Record<string, any>;

function extractRows(d: Record<string, unknown> | null): Row[] {
  if (!d) return [];
  for (const k of ['rows', 'costs', 'items', 'by_key', 'by_provider']) {
    if (Array.isArray(d[k])) return d[k] as Row[];
  }
  const first = Object.values(d).find(Array.isArray);
  return (first as Row[]) ?? [];
}

const num = (v: unknown) => (typeof v === 'number' ? v : Number(v) || 0);

export default function CostsPage() {
  const { token } = useAuth();
  const { data, error, loading, reload } = useLoader((t) => getCosts(t), token);
  const rows = extractRows(data);
  const total = rows.reduce((s, r) => s + num(r.estimated_cost_usd), 0);
  const requests = rows.reduce((s, r) => s + num(r.request_count ?? r.requests), 0);

  return (
    <div className="space-y-6">
      <PageHeader title="Costs" subtitle="Estimated spend over the last 30 days, grouped by provider, model and key." />
      <ErrorBanner message={error} onRetry={() => reload()} />
      {loading && !data ? <Card><Loading /></Card> : (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Card className="p-4"><p className="mb-1 text-xs text-neutral-400">Estimated total</p><p className="text-2xl font-bold text-white">${total.toFixed(4)}</p></Card>
            <Card className="p-4"><p className="mb-1 text-xs text-neutral-400">Requests</p><p className="text-2xl font-bold text-white">{requests.toLocaleString()}</p></Card>
          </div>
          <Card>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <caption className="sr-only">Costs by provider, model and key</caption>
                <thead>
                  <tr className="border-b border-neutral-800">
                    {['Provider', 'Model', 'Key', 'Requests', 'Input tokens', 'Output tokens', 'Est. cost'].map((h) => <th key={h} scope="col" className={thCls}>{h}</th>)}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r, i) => (
                    <tr key={i} className="border-b border-neutral-800/50">
                      <td className={tdCls}>{r.provider ?? '—'}</td>
                      <td className={`${tdCls} font-mono text-xs`}>{r.canonical_model ?? r.model ?? '—'}</td>
                      <td className={tdCls}>{r.key_label ?? '—'}</td>
                      <td className={tdCls}>{num(r.request_count ?? r.requests).toLocaleString()}</td>
                      <td className={tdCls}>{num(r.input_tokens).toLocaleString()}</td>
                      <td className={tdCls}>{num(r.output_tokens).toLocaleString()}</td>
                      <td className={`${tdCls} font-mono`}>${num(r.estimated_cost_usd).toFixed(6)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!error && rows.length === 0 && <EmptyState title="No cost data yet" hint="Costs appear after requests are served." />}
          </Card>
        </>
      )}
    </div>
  );
}
