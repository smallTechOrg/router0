import { getServerSession } from 'next-auth';
import { authOptions } from '@/lib/auth';

async function getPool(token: string) {
  try {
    const res = await fetch('http://localhost:8080/api/pool', {
      headers: { Authorization: `Bearer ${token}` },
      cache: 'no-store',
    });
    if (!res.ok) return { active: [], common: [] };
    return res.json();
  } catch {
    return { active: [], common: [] };
  }
}

function ScoreBadge({ score }: { score: number }) {
  const color = score >= 0.7 ? 'text-green-400' : score >= 0.4 ? 'text-yellow-400' : 'text-red-400';
  return <span className={`font-mono font-semibold ${color}`}>{score.toFixed(2)}</span>;
}

function ComboTable({ combos, label }: { combos: any[]; label: string }) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden mb-6">
      <div className="px-6 py-4 border-b border-neutral-800 flex items-center gap-2">
        <h2 className="text-sm font-semibold text-neutral-200">{label}</h2>
        <span className="text-xs text-neutral-500">({combos.length})</span>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-neutral-800">
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Provider</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Model</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Score</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Pool</th>
            </tr>
          </thead>
          <tbody>
            {combos.length === 0 ? (
              <tr>
                <td colSpan={4} className="px-6 py-12 text-center text-sm text-neutral-500">
                  No combos yet. Add a provider key to get started.
                </td>
              </tr>
            ) : combos.map((c: any, i: number) => (
              <tr key={i} className="border-b border-neutral-800/50 hover:bg-neutral-800/30 transition-colors">
                <td className="px-6 py-3 text-neutral-200 capitalize">{c.provider}</td>
                <td className="px-6 py-3 text-neutral-400 font-mono text-xs">{c.model}</td>
                <td className="px-6 py-3"><ScoreBadge score={c.score ?? 0.5} /></td>
                <td className="px-6 py-3">
                  <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
                    c.pool === 'active' ? 'bg-green-900/40 text-green-400' : 'bg-neutral-800 text-neutral-400'
                  }`}>
                    {c.pool}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default async function PoolStatusPage() {
  const session = await getServerSession(authOptions) as any;
  const pool = await getPool(session?.accessToken || '');

  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Pool Status</h1>
        <p className="mt-1 text-sm text-neutral-400">Active and common provider pool state.</p>
      </div>
      <ComboTable combos={pool.active || []} label="Active Pool (80% traffic)" />
      <ComboTable combos={pool.common || []} label="Common Pool (20% traffic)" />
      <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-6">
        <p className="text-sm text-neutral-500">Analytics — <span className="text-neutral-400">Coming soon.</span></p>
      </div>
    </div>
  );
}
