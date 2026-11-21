'use client';
import { useMemo, useState } from 'react';
import { useAuth, useLoader } from '@/lib/hooks';
import {
  addKey, deleteKey, errorMessage, getPriority, listKeys, listProviders, putPriority, testKey,
  providerLabelFor, type ProviderKey,
} from '@/lib/api';
import KeyStateBadge from '@/components/KeyStateBadge';
import { btnDanger, btnGhost, btnPrimary, Card, EmptyState, ErrorBanner, fmtTime, inputCls, Loading, Notice, PageHeader } from '@/components/ui';

const KEY_HINTS: Record<string, string> = {
  groq: 'gsk_…', anthropic: 'sk-ant-…', meshapi: 'rsk_…', openrouter: 'sk-or-…',
};

export default function KeysPage() {
  const { token, canWrite } = useAuth();
  const keys = useLoader((t) => listKeys(t), token, [], 10000);
  const prio = useLoader((t) => getPriority(t), token, [], 10000);
  const providers = useLoader((t) => listProviders(t), token);

  const [provider, setProvider] = useState('');
  const [secret, setSecret] = useState('');
  const [label, setLabel] = useState('');
  const [modelId, setModelId] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [testing, setTesting] = useState<string | null>(null);

  const providerLabel = useMemo(() => {
    const m: Record<string, string> = {};
    providers.data?.providers.forEach((p) => { m[p.id] = p.label; });
    return (id: string) => m[id] || providerLabelFor(id);
  }, [providers.data]);

  const byId = useMemo(() => {
    const m = new Map<string, ProviderKey>();
    keys.data?.keys.forEach((k) => m.set(k.id, k));
    return m;
  }, [keys.data]);

  // Groups in priority order; keys in position order. Disabled keys are not shown.
  const groups = (prio.data?.providers ?? []).map((g) => ({
    provider: g.provider,
    keys: [...g.keys].sort((a, b) => a.position - b.position)
      .map((k) => byId.get(k.id)).filter((k): k is ProviderKey => !!k),
  }));

  const effectiveProvider = provider || providers.data?.providers[0]?.id || '';

  async function reloadAll() {
    await Promise.all([keys.reload(true), prio.reload(true)]);
  }

  async function onAdd(e: React.FormEvent) {
    e.preventDefault();
    if (!token) return;
    setError('');
    setBusy(true);
    try {
      await addKey(token, {
        provider: effectiveProvider, key: secret, label,
        ...(effectiveProvider === 'baseten' && modelId ? { model_id: modelId } : {}),
      });
      setSecret(''); setLabel(''); setModelId('');
      setNotice(`Key "${label}" added. Model discovery runs in the background.`);
      await reloadAll();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function move(provider: string, idx: number, dir: -1 | 1) {
    if (!token || !prio.data) return;
    const g = groups.find((x) => x.provider === provider);
    if (!g) return;
    const ids = g.keys.map((k) => k.id);
    const j = idx + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[idx], ids[j]] = [ids[j], ids[idx]];
    setError('');
    try {
      const res = await putPriority(token, {
        providers: prio.data.providers.map((p) => p.provider),
        key_order: { [provider]: ids },
      });
      prio.setData(res);
    } catch (err) {
      setError(errorMessage(err));
      reloadAll();
    }
  }

  async function onTest(k: ProviderKey) {
    if (!token) return;
    setTesting(k.id); setError('');
    try {
      const r = await testKey(token, k.id);
      if (r.status === 'ok') setNotice(`"${k.label}" OK: ${r.model_count ?? 0} models, ${r.latency_ms ?? 0} ms.`);
      else setError(`Test of "${k.label}" failed (${r.class ?? 'error'}): ${r.message ?? 'unknown error'}`);
      await reloadAll();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setTesting(null);
    }
  }

  async function onRemove(k: ProviderKey) {
    if (!token) return;
    if (!window.confirm(`Remove key "${k.label}"? It will be disabled and no longer used for routing.`)) return;
    setError('');
    try {
      await deleteKey(token, k.id);
      setNotice(`Key "${k.label}" removed.`);
      await reloadAll();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  const loading = (keys.loading || prio.loading) && !keys.data;

  return (
    <div className="space-y-6">
      <PageHeader title="Provider keys" subtitle="Bring your own keys. Within a provider, keys are tried top to bottom; use the arrows to change the order. Status refreshes every 10 s." />
      <ErrorBanner message={keys.error || prio.error || providers.error} onRetry={reloadAll} />
      <ErrorBanner message={error} />
      <Notice message={notice} />

      {canWrite && (
        <Card title="Add provider key">
          <form onSubmit={onAdd} className="grid gap-3 p-5 sm:grid-cols-2 lg:grid-cols-4 lg:items-end">
            <div>
              <label htmlFor="k-provider" className="mb-1.5 block text-sm font-medium text-neutral-300">Provider</label>
              <select id="k-provider" className={inputCls} value={effectiveProvider} onChange={(e) => setProvider(e.target.value)}>
                {(providers.data?.providers ?? []).map((p) => <option key={p.id} value={p.id}>{p.label || providerLabelFor(p.id)}</option>)}
              </select>
            </div>
            <div>
              <label htmlFor="k-label" className="mb-1.5 block text-sm font-medium text-neutral-300">Label</label>
              <input id="k-label" className={inputCls} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="groq-a" required />
            </div>
            <div>
              <label htmlFor="k-secret" className="mb-1.5 block text-sm font-medium text-neutral-300">API key</label>
              <input id="k-secret" type="password" autoComplete="off" className={inputCls} value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={KEY_HINTS[effectiveProvider] || ''} required />
            </div>
            {effectiveProvider === 'baseten' && (
              <div>
                <label htmlFor="k-model" className="mb-1.5 block text-sm font-medium text-neutral-300">Model ID</label>
                <input id="k-model" className={inputCls} value={modelId} onChange={(e) => setModelId(e.target.value)} />
              </div>
            )}
            <div>
              <button type="submit" className={`${btnPrimary} w-full`} disabled={busy || !effectiveProvider}>{busy ? 'Adding…' : 'Add key'}</button>
            </div>
          </form>
        </Card>
      )}

      {loading ? (
        <Card><Loading /></Card>
      ) : groups.length === 0 ? (
        <Card><EmptyState title="No provider keys yet" hint={canWrite ? 'Add a provider key (Groq, OpenRouter, Anthropic, Mesh API …) above.' : 'Ask an org owner or admin to add one.'} /></Card>
      ) : (
        groups.map((g, gi) => (
          <Card key={g.provider} title={`${providerLabel(g.provider)}${gi === 0 ? ' (highest priority)' : ''}`}>
            <ul className="divide-y divide-neutral-800">
              {g.keys.map((k, i) => (
                <li key={k.id} className="flex flex-wrap items-center gap-3 px-5 py-3">
                  <span className="w-5 text-xs text-neutral-500" aria-label={`Position ${i + 1}`}>{i + 1}</span>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-neutral-100">{k.label}</p>
                    <p className="truncate font-mono text-xs text-neutral-500">
                      {k.key_hint} · last test: {k.last_test_result ? `${k.last_test_result}, ${fmtTime(k.last_tested_at)}` : 'never'}
                    </p>
                  </div>
                  <KeyStateBadge keyInfo={k} />
                  {canWrite && (
                    <div className="flex items-center gap-1.5">
                      <button className={btnGhost} onClick={() => move(g.provider, i, -1)} disabled={i === 0} aria-label={`Move ${k.label} up`}>▲</button>
                      <button className={btnGhost} onClick={() => move(g.provider, i, 1)} disabled={i === g.keys.length - 1} aria-label={`Move ${k.label} down`}>▼</button>
                      <button className={btnGhost} onClick={() => onTest(k)} disabled={testing === k.id}>{testing === k.id ? 'Testing…' : 'Test'}</button>
                      <button className={btnDanger} onClick={() => onRemove(k)}>Remove</button>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          </Card>
        ))
      )}
    </div>
  );
}
