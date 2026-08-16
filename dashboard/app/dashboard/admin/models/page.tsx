'use client';
import { useEffect, useState } from 'react';
import { useSession } from 'next-auth/react';
import { useRouter } from 'next/navigation';

type RateLimit = {
  plan: string;
  limit_rpm: number | null;
  limit_tpm: number | null;
  limit_tpd: number | null;
  limit_rpd: number | null;
  source: string;
};

type Model = {
  provider: string;
  model_id: string;
  display_name: string;
  context_window: number;
  max_output_tokens: number;
  input_cost_per_mtok: number;
  output_cost_per_mtok: number;
  parameter_count_b: number | null;
  architecture: string | null;
  score: number | null;
  avg_latency_ms: number | null;
  error_rate: number | null;
  probe_status: string;
  total_requests: number | null;
  total_errors: number | null;
  p50_latency_ms: number | null;
  p95_latency_ms: number | null;
  p99_latency_ms: number | null;
  min_latency_ms: number | null;
  max_latency_ms: number | null;
  rate_limits: RateLimit[];
};

const PROVIDER_COLORS: Record<string, { bg: string; text: string; border: string }> = {
  groq:       { bg: 'bg-orange-900/30', text: 'text-orange-300', border: 'border-orange-800/50' },
  nvidia:     { bg: 'bg-green-900/30',  text: 'text-green-300',  border: 'border-green-800/50' },
  openrouter: { bg: 'bg-purple-900/30', text: 'text-purple-300', border: 'border-purple-800/50' },
  google:     { bg: 'bg-blue-900/30',   text: 'text-blue-300',   border: 'border-blue-800/50' },
  together:   { bg: 'bg-pink-900/30',   text: 'text-pink-300',   border: 'border-pink-800/50' },
};

function fmtCtx(n: number) {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(0)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`;
  return String(n);
}

function fmtParams(b: number | null) {
  if (!b) return null;
  if (b >= 1000) return `${(b / 1000).toFixed(1)}T`;
  return `${b % 1 === 0 ? b : b.toFixed(1)}B`;
}

type RoutingTier = 'Pro' | 'Flash' | 'Flash-Lite';
function routingTier(b: number | null, ctx: number): RoutingTier {
  if (b && b >= 150 && ctx >= 200000) return 'Pro';
  if (b && b >= 70 && ctx >= 100000) return 'Flash';
  return 'Flash-Lite';
}
const TIER_STYLE: Record<RoutingTier, { badge: string; label: string; desc: string }> = {
  Pro:          { badge: 'bg-amber-900/40 text-amber-300 border border-amber-700/40', label: 'PRO', desc: '≥150B · ≥200K ctx' },
  Flash:        { badge: 'bg-indigo-900/40 text-indigo-300 border border-indigo-700/40', label: 'FLASH', desc: '≥70B · ≥100K ctx' },
  'Flash-Lite': { badge: 'bg-neutral-800 text-neutral-400 border border-neutral-700/40', label: 'LITE', desc: 'All models' },
};

function LatencyBar({ p50, p95, p99, max }: { p50: number | null; p95: number | null; p99: number | null; max: number | null }) {
  if (!p50 || !max) return <span className="text-xs text-neutral-600">—</span>;
  const scale = Math.max(max, p99 ?? max, 5000);
  const pct = (v: number) => Math.min(100, (v / scale) * 100);
  return (
    <div className="space-y-1.5 min-w-[120px]">
      {[
        { label: 'p50', val: p50, color: 'bg-green-500' },
        { label: 'p95', val: p95, color: 'bg-yellow-500' },
        { label: 'p99', val: p99, color: 'bg-orange-500' },
      ].map(({ label, val, color }) => val != null && (
        <div key={label} className="flex items-center gap-1.5">
          <span className="text-[10px] text-neutral-600 w-6">{label}</span>
          <div className="flex-1 h-1.5 rounded-full bg-neutral-800">
            <div className={`h-full rounded-full ${color}`} style={{ width: `${pct(val)}%` }} />
          </div>
          <span className="text-[10px] text-neutral-400 tabular-nums w-14 text-right">{val}ms</span>
        </div>
      ))}
    </div>
  );
}

function ScoreGauge({ score }: { score: number | null }) {
  if (score === null) return <span className="text-xs text-neutral-600">no data</span>;
  const pct = Math.round(score * 100);
  const color = pct >= 80 ? 'text-green-400' : pct >= 60 ? 'text-yellow-400' : 'text-red-400';
  const ring = pct >= 80 ? 'stroke-green-500' : pct >= 60 ? 'stroke-yellow-500' : 'stroke-red-500';
  const r = 16, circ = 2 * Math.PI * r;
  const dash = (pct / 100) * circ;
  return (
    <div className="flex flex-col items-center gap-1">
      <svg width="44" height="44" viewBox="0 0 44 44">
        <circle cx="22" cy="22" r={r} fill="none" stroke="#262626" strokeWidth="3.5" />
        <circle cx="22" cy="22" r={r} fill="none" className={ring} strokeWidth="3.5"
          strokeDasharray={`${dash} ${circ}`} strokeDashoffset={circ / 4}
          strokeLinecap="round" transform="rotate(-90 22 22)" style={{ transition: 'stroke-dasharray 0.4s' }} />
        <text x="22" y="26" textAnchor="middle" className={`text-[10px] font-bold fill-current ${color}`}
          style={{ fontSize: 10, fontWeight: 700 }}>{pct}</text>
      </svg>
    </div>
  );
}

function RateLimitBadges({ limits }: { limits: RateLimit[] }) {
  if (!limits.length) return <span className="text-xs text-neutral-600">—</span>;
  const observed = limits.find(l => l.source === 'observed');
  const best = observed || limits[0];
  return (
    <div className="flex flex-wrap gap-1">
      {best.limit_rpm != null && (
        <span className="rounded px-1.5 py-0.5 text-[10px] bg-neutral-800 text-neutral-400">{best.limit_rpm} RPM</span>
      )}
      {best.limit_tpm != null && (
        <span className="rounded px-1.5 py-0.5 text-[10px] bg-neutral-800 text-neutral-400">
          {best.limit_tpm >= 1000 ? `${(best.limit_tpm/1000).toFixed(0)}K` : best.limit_tpm} TPM
        </span>
      )}
      {best.limit_rpd != null && (
        <span className="rounded px-1.5 py-0.5 text-[10px] bg-neutral-800 text-neutral-400">{best.limit_rpd} RPD</span>
      )}
      {observed && (
        <span className="rounded px-1.5 py-0.5 text-[10px] bg-green-900/30 text-green-500">observed</span>
      )}
    </div>
  );
}

export default function ModelsPage() {
  const { data: session, status } = useSession() as any;
  const router = useRouter();

  const [models, setModels] = useState<Model[]>([]);
  const [loading, setLoading] = useState(true);
  const [provider, setProvider] = useState('');
  const [tierFilter, setTierFilter] = useState<RoutingTier | ''>('');
  const [search, setSearch] = useState('');
  const [actionLoading, setActionLoading] = useState(false);
  const [actionMsg, setActionMsg] = useState<string | null>(null);

  useEffect(() => {
    if (status === 'loading') return;
    if (status === 'unauthenticated') { router.replace('/auth/login'); return; }
    if (session?.user?.role !== 'admin') router.replace('/dashboard');
  }, [session, status]);

  async function loadModels() {
    if (!session?.accessToken) return;
    setLoading(true);
    try {
      const url = provider ? `/admin/models?provider=${provider}` : '/admin/models';
      const res = await fetch(url, { headers: { Authorization: `Bearer ${session.accessToken}` } });
      const data = await res.json();
      setModels(data.models || []);
    } catch { setModels([]); }
    finally { setLoading(false); }
  }

  async function triggerDiscovery() {
    setActionLoading(true);
    setActionMsg(null);
    try {
      await fetch('/admin/discovery', {
        method: 'POST',
        headers: { Authorization: `Bearer ${session?.accessToken}` },
      });
      setActionMsg('Discovery started — refresh in ~30s');
    } catch { setActionMsg('Discovery trigger failed'); }
    finally { setActionLoading(false); }
  }

  useEffect(() => { loadModels(); }, [session, provider]);

  const filtered = models.filter(m => {
    if (search && !m.model_id.toLowerCase().includes(search.toLowerCase()) &&
        !m.display_name?.toLowerCase().includes(search.toLowerCase())) return false;
    if (tierFilter && routingTier(m.parameter_count_b, m.context_window) !== tierFilter) return false;
    return true;
  });

  const byProvider: Record<string, number> = {};
  for (const m of models) byProvider[m.provider] = (byProvider[m.provider] || 0) + 1;
  const byTier: Partial<Record<RoutingTier, number>> = {};
  for (const m of models) { const t = routingTier(m.parameter_count_b, m.context_window); byTier[t] = (byTier[t] || 0) + 1; }

  const totalModels = filtered.length;
  const avgScore = filtered.filter(m => m.score != null).reduce((s, m) => s + m.score!, 0) /
    (filtered.filter(m => m.score != null).length || 1);
  const totalRequests = filtered.reduce((s, m) => s + (m.total_requests || 0), 0);

  if (status === 'loading') return <div className="p-8 text-neutral-500">Loading...</div>;
  if (session?.user?.role !== 'admin') return null;

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold text-white">Model Registry</h1>
          <p className="mt-1 text-sm text-neutral-400">
            Three routing tiers: <span className="text-amber-300 font-medium">Pro</span> (150B+, 200K+ ctx) · <span className="text-indigo-300 font-medium">Flash</span> (70B+, 100K+ ctx) · <span className="text-neutral-300 font-medium">Lite</span> (all)
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button onClick={loadModels}
            className="rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-xs text-neutral-300 hover:text-white transition-colors">
            Refresh
          </button>
          <button onClick={triggerDiscovery} disabled={actionLoading}
            className="rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-xs text-neutral-300 hover:text-white transition-colors disabled:opacity-50">
            {actionLoading ? 'Discovering...' : 'Refresh Registry'}
          </button>
        </div>
      </div>

      {actionMsg && <p className="text-xs text-neutral-400">{actionMsg}</p>}

      {/* Summary tiles */}
      <div className="grid grid-cols-3 gap-3">
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 px-5 py-4">
          <div className="text-xs text-neutral-500">Total Models</div>
          <div className="text-3xl font-bold text-white mt-1">{totalModels}</div>
          <div className="text-xs text-neutral-600 mt-0.5">{Object.keys(byProvider).length} providers</div>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 px-5 py-4">
          <div className="text-xs text-neutral-500">Avg Score</div>
          <div className={`text-3xl font-bold mt-1 ${avgScore >= 0.8 ? 'text-green-400' : 'text-yellow-400'}`}>
            {avgScore.toFixed(3)}
          </div>
          <div className="text-xs text-neutral-600 mt-0.5">across scored models</div>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 px-5 py-4">
          <div className="text-xs text-neutral-500">Total Requests (all-time)</div>
          <div className="text-3xl font-bold text-white mt-1">{totalRequests.toLocaleString()}</div>
          <div className="text-xs text-neutral-600 mt-0.5">perf tests excluded</div>
        </div>
      </div>

      {/* Tier filter chips */}
      <div className="flex flex-wrap gap-2 items-center">
        <span className="text-xs text-neutral-600 font-medium">Routing Tier:</span>
        {(['', 'Pro', 'Flash', 'Flash-Lite'] as const).map(t => {
          const label = t === '' ? 'All' : TIER_STYLE[t as RoutingTier].label;
          const count = t === '' ? models.length : (byTier[t as RoutingTier] || 0);
          const active = tierFilter === t;
          const style = t !== '' ? TIER_STYLE[t as RoutingTier] : null;
          return (
            <button key={t} onClick={() => setTierFilter(t === '' ? '' : t as RoutingTier)}
              className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                active
                  ? t === '' ? 'bg-neutral-700 text-white' : style!.badge
                  : 'bg-neutral-800/50 text-neutral-500 hover:text-white'
              }`}>
              {label} · {count}
            </button>
          );
        })}
      </div>

      {/* Provider filter chips */}
      <div className="flex flex-wrap gap-2 items-center">
        <span className="text-xs text-neutral-600 font-medium">Provider:</span>
        <button onClick={() => setProvider('')}
          className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
            !provider ? 'bg-neutral-700 text-white' : 'bg-neutral-800 text-neutral-400 hover:text-white'}`}>
          All · {models.length}
        </button>
        {Object.entries(byProvider).sort().map(([p, count]) => {
          const c = PROVIDER_COLORS[p];
          return (
            <button key={p} onClick={() => setProvider(provider === p ? '' : p)}
              className={`rounded-full px-3 py-1 text-xs font-medium transition-colors border ${
                provider === p
                  ? `${c?.bg || 'bg-neutral-700'} ${c?.text || 'text-white'} ${c?.border || 'border-neutral-600'}`
                  : 'bg-neutral-800 border-transparent text-neutral-400 hover:text-white'
              }`}>
              {p} · {count}
            </button>
          );
        })}
      </div>

      {/* Search */}
      <input type="text" placeholder="Search models..." value={search} onChange={e => setSearch(e.target.value)}
        className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none" />

      {/* Model cards */}
      {loading ? (
        <div className="py-16 text-center text-neutral-500">Loading models...</div>
      ) : filtered.length === 0 ? (
        <div className="py-16 text-center text-neutral-500">No models found.</div>
      ) : (
        <div className="space-y-3">
          {filtered.map(m => {
            const pc = PROVIDER_COLORS[m.provider];
            const params = fmtParams(m.parameter_count_b);
            const tier = routingTier(m.parameter_count_b, m.context_window);
            const ts = TIER_STYLE[tier];
            const errorPct = m.total_requests && m.total_errors != null && m.total_requests > 0
              ? ((m.total_errors / m.total_requests) * 100).toFixed(1) : null;
            return (
              <div key={m.provider + ':' + m.model_id}
                className="rounded-xl border border-neutral-800 bg-neutral-900 p-5 hover:border-neutral-700 transition-colors">
                <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:gap-6">

                  {/* Left: Identity */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className={`rounded-full px-2 py-0.5 text-[10px] font-semibold ${pc?.bg || 'bg-neutral-800'} ${pc?.text || 'text-neutral-300'}`}>
                        {m.provider}
                      </span>
                      {params && (
                        <span className="rounded-full px-2 py-0.5 text-[10px] font-medium bg-neutral-800 text-neutral-400">
                          {params}
                        </span>
                      )}
                      <span className={`rounded-full px-2 py-0.5 text-[10px] font-semibold ${ts.badge}`}>
                        {ts.label}
                      </span>
                      {m.architecture && (
                        <span className="rounded-full px-2 py-0.5 text-[10px] font-medium bg-neutral-800/60 text-neutral-500">
                          {m.architecture}
                        </span>
                      )}
                    </div>
                    <div className="mt-1.5 font-mono text-sm text-neutral-100 break-all">{m.model_id}</div>
                    {m.display_name && m.display_name !== m.model_id && (
                      <div className="mt-0.5 text-xs text-neutral-500">{m.display_name}</div>
                    )}
                    {/* Specs row */}
                    <div className="mt-3 flex flex-wrap gap-x-5 gap-y-1 text-xs text-neutral-500">
                      <span><span className="text-neutral-400 font-medium">Context</span> {fmtCtx(m.context_window)}</span>
                      {m.max_output_tokens > 0 && (
                        <span><span className="text-neutral-400 font-medium">Max output</span> {fmtCtx(m.max_output_tokens)}</span>
                      )}
                      {(m.input_cost_per_mtok > 0 || m.output_cost_per_mtok > 0) ? (
                        <span>
                          <span className="text-neutral-400 font-medium">Cost</span>{' '}
                          ${m.input_cost_per_mtok?.toFixed(2)}/M in · ${m.output_cost_per_mtok?.toFixed(2)}/M out
                        </span>
                      ) : <span className="text-green-600 font-medium">Free tier</span>}
                    </div>
                    {/* Rate limits */}
                    {m.rate_limits?.length > 0 && (
                      <div className="mt-2">
                        <RateLimitBadges limits={m.rate_limits} />
                      </div>
                    )}
                  </div>

                  {/* Center: Latency histogram */}
                  <div className="lg:w-56">
                    <div className="text-[10px] text-neutral-600 uppercase mb-2">Latency (via Zer0)</div>
                    <LatencyBar p50={m.p50_latency_ms} p95={m.p95_latency_ms} p99={m.p99_latency_ms} max={m.max_latency_ms} />
                    {m.min_latency_ms != null && m.max_latency_ms != null && (
                      <div className="mt-1.5 text-[10px] text-neutral-600">
                        range {m.min_latency_ms}ms - {m.max_latency_ms}ms
                      </div>
                    )}
                  </div>

                  {/* Right: Score + requests */}
                  <div className="flex gap-6 lg:flex-col lg:gap-3 lg:w-32 lg:items-center lg:text-center">
                    <div>
                      <div className="text-[10px] text-neutral-600 uppercase mb-1 lg:text-center">Zer0 Score</div>
                      <ScoreGauge score={m.score} />
                    </div>
                    <div className="lg:text-center">
                      <div className="text-[10px] text-neutral-600 uppercase mb-1">Requests</div>
                      {m.total_requests != null && m.total_requests > 0 ? (
                        <div>
                          <div className="text-sm font-bold text-neutral-200">{m.total_requests.toLocaleString()}</div>
                          {errorPct !== null && Number(errorPct) > 0 ? (
                            <div className="text-[10px] text-red-400">{errorPct}% errors</div>
                          ) : m.total_requests > 0 ? (
                            <div className="text-[10px] text-green-600">0 errors</div>
                          ) : null}
                        </div>
                      ) : <span className="text-xs text-neutral-600">-</span>}
                    </div>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
