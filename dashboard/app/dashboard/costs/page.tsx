import { getServerSession } from 'next-auth';
import { authOptions } from '@/lib/auth';

async function getCosts(token: string) {
  try {
    const res = await fetch('http://localhost:8080/api/costs', {
      headers: { Authorization: `Bearer ${token}` },
      cache: 'no-store',
    });
    if (!res.ok) return { month: '', by_provider: [], total_estimated_cost_usd: 0 };
    return res.json();
  } catch { return { month: '', by_provider: [], total_estimated_cost_usd: 0 }; }
}

export default async function CostsPage() {
  const session = await getServerSession(authOptions) as any;
  const costs = await getCosts(session?.accessToken || '');

  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Cost Tracker</h1>
        <p className="mt-1 text-sm text-neutral-400">Estimated spend for {costs.month || 'current month'}.</p>
      </div>

      {/* Summary card */}
      <div className="grid grid-cols-3 gap-4 mb-6">
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-4">
          <p className="text-xs text-neutral-400 mb-1">Total this month</p>
          <p className="text-2xl font-bold text-white">${Number(costs.total_estimated_cost_usd).toFixed(4)}</p>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-4">
          <p className="text-xs text-neutral-400 mb-1">Providers active</p>
          <p className="text-2xl font-bold text-white">{costs.by_provider?.length || 0}</p>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-4">
          <p className="text-xs text-neutral-400 mb-1">Total requests</p>
          <p className="text-2xl font-bold text-white">
            {(costs.by_provider || []).reduce((s: number, p: any) => s + (p.request_count || 0), 0)}
          </p>
        </div>
      </div>

      {/* By provider table */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden mb-6">
        <div className="px-6 py-4 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">By Provider</h2>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-neutral-800">
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Provider</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Requests</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Input tokens</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Output tokens</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Estimated cost</th>
            </tr>
          </thead>
          <tbody>
            {(costs.by_provider || []).length === 0 ? (
              <tr><td colSpan={5} className="px-6 py-12 text-center text-sm text-neutral-500">No data yet.</td></tr>
            ) : (costs.by_provider || []).map((p: any) => (
              <tr key={p.provider} className="border-b border-neutral-800/50">
                <td className="px-6 py-3 text-neutral-200 capitalize">{p.provider}</td>
                <td className="px-6 py-3 text-neutral-400">{p.request_count}</td>
                <td className="px-6 py-3 text-neutral-400">{p.input_tokens?.toLocaleString()}</td>
                <td className="px-6 py-3 text-neutral-400">{p.output_tokens?.toLocaleString()}</td>
                <td className="px-6 py-3 text-neutral-200 font-mono">${Number(p.estimated_cost_usd).toFixed(6)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Chart stub */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-8 text-center">
        <p className="text-sm text-neutral-500">Spend chart — <span className="text-neutral-400">Coming soon in Phase 2.</span></p>
      </div>
    </div>
  );
}
