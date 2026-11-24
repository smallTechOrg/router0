'use client';
import Link from 'next/link';
import { useAuth, useLoader } from '@/lib/hooks';
import { listKeys, listRequests, listTokens } from '@/lib/api';
import { useApiBase } from '@/lib/useApiBase';
import KeyAlerts from '@/components/KeyAlerts';
import { Card, CopyButton, ErrorBanner, Loading, PageHeader } from '@/components/ui';

const snippetFor = (base: string) => `curl ${base}/chat/completions \\
  -H "Authorization: Bearer r0-sk-..." \\
  -H "Content-Type: application/json" \\
  -d '{"model":"llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}'`;

export default function OverviewPage() {
  const { token, me, meError } = useAuth();
  const snippet = snippetFor(useApiBase());
  const tokens = useLoader((t) => listTokens(t), token);
  const keys = useLoader((t) => listKeys(t), token);
  const reqs = useLoader((t) => listRequests(t, { limit: 200 }), token);

  const liveTokens = tokens.data?.tokens.filter((t) => !t.revoked_at).length ?? 0;
  const activeKeys = (keys.data?.keys ?? []).filter((k) => k.status === 'active');
  const byProvider: Record<string, number> = {};
  activeKeys.forEach((k) => { byProvider[k.provider] = (byProvider[k.provider] || 0) + 1; });

  const since = Date.now() - 24 * 3600 * 1000;
  const recent = (reqs.data?.requests ?? []).filter((r) => new Date(r.created_at).getTime() >= since);
  const errors = recent.filter((r) => r.status === 'error').length;
  const errRate = recent.length ? ((errors / recent.length) * 100).toFixed(1) : '0.0';
  const capped = (reqs.data?.requests.length ?? 0) >= 200;

  const loading = tokens.loading || keys.loading || reqs.loading;
  const error = meError || tokens.error || keys.error || reqs.error;

  return (
    <div className="space-y-6">
      <PageHeader title="Overview" subtitle={me ? `Organization: ${me.org.name}` : undefined} />
      <ErrorBanner message={error} />

      {!tokens.loading && !tokens.error && liveTokens === 0 && (
        <div className="rounded-xl border border-indigo-500/30 bg-indigo-500/10 p-5">
          <p className="text-sm font-medium text-indigo-200">Create your first service token</p>
          <p className="mt-1 text-sm text-neutral-400">Service tokens let your backend call router0 at /v1.</p>
          <Link href="/dashboard/tokens" className="mt-3 inline-block rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500">
            Go to Tokens
          </Link>
        </div>
      )}

      {keys.data && <KeyAlerts keys={keys.data.keys} />}

      {loading ? (
        <Loading />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Stat label="Live tokens" value={String(liveTokens)} />
          <Stat label="Requests (24 h)" value={`${recent.length}${capped ? '+' : ''}`} />
          <Stat label="Error rate (24 h)" value={`${errRate}%`} />
          <Card className="p-4">
            <p className="mb-1 text-xs text-neutral-400">Active keys</p>
            {Object.keys(byProvider).length === 0 ? (
              <p className="text-sm text-neutral-500">None yet. <Link className="text-indigo-400 hover:text-indigo-300" href="/dashboard/keys">Add a key</Link></p>
            ) : (
              <ul className="text-sm text-neutral-200">
                {Object.entries(byProvider).map(([p, n]) => <li key={p}>{p}: {n}</li>)}
              </ul>
            )}
          </Card>
        </div>
      )}

      <Card title="Quick start" actions={<CopyButton text={snippet} />}>
        <pre className="overflow-x-auto px-5 py-4 text-xs text-neutral-300"><code>{snippet}</code></pre>
      </Card>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <Card className="p-4">
      <p className="mb-1 text-xs text-neutral-400">{label}</p>
      <p className="text-2xl font-bold text-white">{value}</p>
    </Card>
  );
}
