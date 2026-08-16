'use client';
import { useEffect, useState } from 'react';
import { useSession } from 'next-auth/react';

type Combo = { provider: string; model: string; provider_key_id: string; score: number; pool: string };

type ModelStat = {
  provider: string;
  model: string;
  total_reqs: number;
  reqs_1h: number;
  reqs_24h: number;
  avg_latency_ms: number | null;
  avg_latency_1h: number | null;
  errors: number;
  total_tokens: number;
  last_seen: string;
};

type PerfResult = {
  provider: string;
  model: string;
  ok: boolean;
  latency_ms: number;
  response?: string;
  error?: string;
};

const PROVIDER_COLORS: Record<string, string> = {
  groq: 'text-orange-400',
  nvidia: 'text-green-400',
  openrouter: 'text-purple-400',
  google: 'text-blue-400',
  together: 'text-pink-400',
};

function ScoreBadge({ score }: { score: number }) {
  const color = score >= 0.7 ? 'text-green-400' : score >= 0.4 ? 'text-yellow-400' : score === 0 ? 'text-red-500' : 'text-neutral-400';
  return <span className={`font-mono font-semibold ${color}`}>{score.toFixed(2)}</span>;
}

function ComboTable({ combos, label, loading }: { combos: Combo[]; label: string; loading: boolean }) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
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
            {loading ? (
              <tr><td colSpan={4} className="px-6 py-8 text-center text-neutral-500">Loading…</td></tr>
            ) : combos.length === 0 ? (
              <tr><td colSpan={4} className="px-6 py-8 text-center text-neutral-500">No combos. Add a provider key to get started.</td></tr>
            ) : combos.map((c, i) => (
              <tr key={i} className="border-b border-neutral-800/50 hover:bg-neutral-800/30 transition-colors">
                <td className={`px-6 py-3 font-medium capitalize ${PROVIDER_COLORS[c.provider] || 'text-neutral-200'}`}>{c.provider}</td>
                <td className="px-6 py-3 text-neutral-400 font-mono text-xs">{c.model}</td>
                <td className="px-6 py-3"><ScoreBadge score={c.score ?? 0.5} /></td>
                <td className="px-6 py-3">
                  <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
                    c.pool === 'pro' ? 'bg-amber-900/40 text-amber-400' : c.pool === 'flash' ? 'bg-indigo-900/40 text-indigo-400' : 'bg-neutral-800 text-neutral-400'
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

function PerfPanel({ results, loading }: { results: PerfResult[] | null; loading: boolean }) {
  if (loading) return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6 text-center text-neutral-500 text-sm animate-pulse">
      Running perf test — hitting every provider directly…
    </div>
  );
  if (!results) return null;

  const pass = results.filter(r => r.ok).length;
  const okResults = results.filter(r => r.ok);
  const avgLatency = okResults.length ? Math.round(okResults.reduce((s, r) => s + r.latency_ms, 0) / okResults.length) : 0;

  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
      <div className="flex items-center justify-between px-6 py-4 border-b border-neutral-800">
        <div className="text-sm font-semibold text-white">Perf Test Results</div>
        <div className="flex gap-4 text-xs text-neutral-400">
          <span><span className="text-green-400 font-medium">{pass}</span>/{results.length} passed</span>
          {pass > 0 && <span>avg <span className="text-neutral-200 font-medium">{avgLatency}ms</span></span>}
        </div>
      </div>
      <div className="divide-y divide-neutral-800/50">
        {[...results].sort((a, b) => a.provider.localeCompare(b.provider)).map((r, i) => (
          <div key={i} className="flex items-center gap-3 px-6 py-3">
            <span className={`text-sm font-bold w-4 ${r.ok ? 'text-green-400' : 'text-red-400'}`}>
              {r.ok ? '✓' : '✗'}
            </span>
            <span className={`w-20 text-xs font-medium capitalize ${PROVIDER_COLORS[r.provider] || 'text-neutral-400'}`}>
              {r.provider}
            </span>
            <span className="font-mono text-xs text-neutral-400 flex-1 truncate">{r.model}</span>
            <span className={`tabular-nums text-xs w-16 text-right font-medium ${
              r.latency_ms < 1000 ? 'text-green-400' : r.latency_ms < 3000 ? 'text-yellow-400' : 'text-red-400'
            }`}>{r.latency_ms}ms</span>
            <span className="text-xs text-neutral-500 w-48 truncate text-right">
              {r.ok ? r.response : r.error}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function AnalyticsPanel({ stats }: { stats: ModelStat[] }) {
  if (!stats.length) return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6 text-center text-neutral-500 text-sm">
      No request data yet. Send some requests to see analytics.
    </div>
  );
  const totalReqs = stats.reduce((s, r) => s + r.total_reqs, 0);
  const totalTokens = stats.reduce((s, r) => s + r.total_tokens, 0);
  const totalErrors = stats.reduce((s, r) => s + r.errors, 0);
  const reqs1h = stats.reduce((s, r) => s + r.reqs_1h, 0);
  return (
    <div className="space-y-4">
      {/* Summary tiles */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {[
          { label: 'Total Requests', value: totalReqs.toLocaleString() },
          { label: 'Last 1h', value: reqs1h.toLocaleString() },
          { label: 'Total Tokens', value: totalTokens >= 1000 ? `${(totalTokens/1000).toFixed(1)}K` : String(totalTokens) },
          { label: 'Error Rate', value: totalReqs > 0 ? `${((totalErrors/totalReqs)*100).toFixed(1)}%` : '—', danger: totalErrors > 0 },
        ].map(t => (
          <div key={t.label} className="rounded-lg border border-neutral-800 bg-neutral-900 px-4 py-3">
            <div className="text-xs text-neutral-500">{t.label}</div>
            <div className={`text-xl font-bold mt-0.5 ${(t as any).danger ? 'text-red-400' : 'text-white'}`}>{t.value}</div>
          </div>
        ))}
      </div>
      {/* Per-model breakdown */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="px-6 py-3 border-b border-neutral-800">
          <h3 className="text-sm font-semibold text-neutral-200">Per-model activity</h3>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800 text-xs text-neutral-500 uppercase">
                <th className="px-4 py-2 text-left">Model</th>
                <th className="px-4 py-2 text-right">Total</th>
                <th className="px-4 py-2 text-right">1h</th>
                <th className="px-4 py-2 text-right">24h</th>
                <th className="px-4 py-2 text-right">Avg ms</th>
                <th className="px-4 py-2 text-right">1h avg</th>
                <th className="px-4 py-2 text-right">Errors</th>
                <th className="px-4 py-2 text-right">Last seen</th>
              </tr>
            </thead>
            <tbody>
              {stats.sort((a, b) => b.total_reqs - a.total_reqs).map(s => (
                <tr key={s.provider + ':' + s.model} className="border-b border-neutral-800/50 hover:bg-neutral-800/20">
                  <td className="px-4 py-2">
                    <span className={`text-[10px] font-medium rounded-full px-1.5 py-0.5 mr-1.5 ${PROVIDER_COLORS[s.provider] ? PROVIDER_COLORS[s.provider].replace('text-', 'text-').replace('400', '400') : 'text-neutral-400'} bg-neutral-800`}>{s.provider}</span>
                    <span className="font-mono text-xs text-neutral-300">{s.model}</span>
                  </td>
                  <td className="px-4 py-2 text-right text-xs text-neutral-300">{s.total_reqs}</td>
                  <td className="px-4 py-2 text-right text-xs text-neutral-400">{s.reqs_1h || '—'}</td>
                  <td className="px-4 py-2 text-right text-xs text-neutral-400">{s.reqs_24h || '—'}</td>
                  <td className="px-4 py-2 text-right text-xs tabular-nums">
                    <span className={s.avg_latency_ms != null && s.avg_latency_ms < 1000 ? 'text-green-400' : s.avg_latency_ms != null && s.avg_latency_ms < 5000 ? 'text-yellow-400' : 'text-red-400'}>
                      {s.avg_latency_ms != null ? `${s.avg_latency_ms}ms` : '—'}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-right text-xs tabular-nums text-neutral-400">
                    {s.avg_latency_1h != null ? `${s.avg_latency_1h}ms` : '—'}
                  </td>
                  <td className="px-4 py-2 text-right text-xs">
                    {s.errors > 0 ? <span className="text-red-400">{s.errors}</span> : <span className="text-neutral-600">0</span>}
                  </td>
                  <td className="px-4 py-2 text-right text-[10px] text-neutral-600">
                    {new Date(s.last_seen).toLocaleTimeString()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

export default function PoolStatusPage() {
  const { data: session } = useSession() as any;
  const isAdmin = session?.user?.role === 'admin';

  const [pool, setPool] = useState<{ pro: Combo[]; flash: Combo[]; flash_lite: Combo[]; model_stats: ModelStat[] }>({ pro: [], flash: [], flash_lite: [], model_stats: [] });
  const [poolLoading, setPoolLoading] = useState(true);

  const [actionLoading, setActionLoading] = useState<string | null>(null);
  const [actionMsg, setActionMsg] = useState<string | null>(null);
  const [perfResults, setPerfResults] = useState<PerfResult[] | null>(null);
  const [perfLoading, setPerfLoading] = useState(false);

  async function loadPool() {
    if (!session?.accessToken) return;
    setPoolLoading(true);
    try {
      const res = await fetch('/api/pool', { headers: { Authorization: `Bearer ${session.accessToken}` } });
      if (res.ok) setPool(await res.json());
    } finally {
      setPoolLoading(false);
    }
  }

  useEffect(() => { loadPool(); }, [session]);

  async function resetPool() {
    setActionLoading('reset');
    setActionMsg(null);
    try {
      const res = await fetch('/admin/pool/reset', {
        method: 'POST',
        headers: { Authorization: `Bearer ${session.accessToken}` },
      });
      const data = await res.json();
      setActionMsg(`Pool reset — ${data.combos_seeded} combos seeded across ${data.users} users`);
      await loadPool();
    } catch {
      setActionMsg('Reset failed');
    } finally {
      setActionLoading(null);
    }
  }

  async function runPerfTest() {
    setPerfLoading(true);
    setPerfResults(null);
    setActionMsg(null);
    try {
      const res = await fetch('/admin/perf-test', {
        method: 'POST',
        headers: { Authorization: `Bearer ${session.accessToken}` },
      });
      const data = await res.json();
      setPerfResults(data.results || []);
      const { pass, fail, total } = data.summary;
      setActionMsg(`Perf test done — ${pass}/${total} passed${fail > 0 ? `, ${fail} failed` : ' ✓'}`);
      await loadPool();
    } catch {
      setActionMsg('Perf test failed');
    } finally {
      setPerfLoading(false);
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold text-white">Pool Status</h1>
          <p className="mt-1 text-sm text-neutral-400">
            Tier-based routing pools with live scores. Pro (&ge;150B, &ge;200K ctx) &middot; Flash (&ge;70B, &ge;100K ctx) &middot; Flash-Lite (all models).
          </p>
        </div>
        <button
          onClick={loadPool}
          className="rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-xs text-neutral-300 hover:text-white transition-colors"
        >
          Refresh
        </button>
      </div>

      {/* Admin actions */}
      {isAdmin && (
        <div className="flex flex-wrap items-center gap-3">
          <button
            onClick={resetPool}
            disabled={!!actionLoading || perfLoading}
            className="flex items-center gap-2 rounded-lg border border-neutral-700 bg-neutral-800 px-4 py-2 text-sm font-medium text-neutral-200 hover:bg-neutral-700 hover:text-white transition-colors disabled:opacity-50"
          >
            <span className={actionLoading === 'reset' ? 'animate-spin inline-block' : ''}>⟳</span>
            Reset Pool
          </button>
          <button
            onClick={runPerfTest}
            disabled={perfLoading || !!actionLoading}
            className="flex items-center gap-2 rounded-lg border border-indigo-700 bg-indigo-900/40 px-4 py-2 text-sm font-medium text-indigo-300 hover:bg-indigo-900/70 hover:text-indigo-100 transition-colors disabled:opacity-50"
          >
            <span className={perfLoading ? 'animate-spin inline-block' : ''}>▶</span>
            Run Perf Test
          </button>
          {actionMsg && <span className="text-xs text-neutral-400">{actionMsg}</span>}
        </div>
      )}

      {/* Analytics */}
      <div>
        <h2 className="text-sm font-semibold text-neutral-300 mb-3">Your Analytics</h2>
        {poolLoading ? (
          <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6 text-center text-neutral-500 text-sm animate-pulse">Loading…</div>
        ) : (
          <AnalyticsPanel stats={pool.model_stats || []} />
        )}
      </div>

      {/* Perf results */}
      <PerfPanel results={perfResults} loading={perfLoading} />

      {/* Pool tables */}
      <ComboTable combos={pool.pro || []} label="Pro Pool — zer0-0.1-pro (≥150B params, ≥200K ctx)" loading={poolLoading} />
      <ComboTable combos={pool.flash || []} label="Flash Pool — zer0-0.1-flash (≥70B params, ≥100K ctx)" loading={poolLoading} />
      <ComboTable combos={pool.flash_lite || []} label="Flash-Lite Pool — zer0-0.1-flash-lite (all models)" loading={poolLoading} />
    </div>
  );
}
