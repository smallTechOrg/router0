'use client';
import { useMemo, useState } from 'react';
import { useAuth, useLoader } from '@/lib/hooks';
import {
  addKey, deleteKey, errorMessage, getPriority, listKeys, listProviders, pollBalances, putPriority, resetKeyState, testKey,
  providerLabelFor, updateKey, type KeyBalance, type ProviderKey,
} from '@/lib/api';
import KeyStateBadge from '@/components/KeyStateBadge';
import SpendBar, { usd } from '@/components/SpendBar';
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
  const [polling, setPolling] = useState(false);

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

  async function onReset(k: ProviderKey) {
    if (!token) return;
    setError('');
    try {
      await resetKeyState(token, k.id);
      setNotice(`State of "${k.label}" reset: cooldowns and out-of-credits marks cleared.`);
      await reloadAll();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function onSaveBudget(k: ProviderKey, budget: number | null) {
    if (!token) return false;
    setError('');
    try {
      await updateKey(token, k.id, { monthly_budget_usd: budget });
      setNotice(budget === null ? `Budget of "${k.label}" removed.` : `Budget of "${k.label}" set to ${usd(budget)} per month.`);
      await reloadAll();
      return true;
    } catch (err) {
      setError(errorMessage(err));
      return false;
    }
  }

  async function onPoll() {
    if (!token) return;
    setPolling(true); setError('');
    try {
      const r = await pollBalances(token);
      setNotice(r.polled === 0 ? 'No keys with a balance API (only OpenRouter has one).' : `Polled ${r.polled} key balance${r.polled === 1 ? '' : 's'}.`);
      await reloadAll();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setPolling(false);
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
  const anyBalanceKey = (keys.data?.keys ?? []).some((k) => k.balance?.supported);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Provider keys"
        subtitle="Bring your own keys. Within a provider, keys are tried top to bottom; use the arrows to change the order. Status refreshes every 10 s."
        actions={canWrite && anyBalanceKey ? (
          <button className={btnGhost} onClick={onPoll} disabled={polling}>{polling ? 'Polling…' : 'Poll balances'}</button>
        ) : undefined}
      />
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
                      {k.state?.blocked && k.state.reason !== 'budget_exhausted' && (
                        <button className={btnGhost} onClick={() => onReset(k)} title="Clear cooldowns and out-of-credits marks now">Reset state</button>
                      )}
                      <button className={btnDanger} onClick={() => onRemove(k)}>Remove</button>
                    </div>
                  )}
                  <div className="flex w-full flex-wrap items-center gap-x-6 gap-y-2 pl-8">
                    <SpendBar spend={k.month_spend_usd ?? 0} budget={k.monthly_budget_usd} className="w-56" />
                    <BalanceText balance={k.balance} />
                    {canWrite && <BudgetEditor keyInfo={k} onSave={(b) => onSaveBudget(k, b)} />}
                  </div>
                </li>
              ))}
            </ul>
          </Card>
        ))
      )}
    </div>
  );
}

/** Last polled provider balance (OpenRouter), or a note that the provider has no balance API. */
function BalanceText({ balance }: { balance?: KeyBalance | null }) {
  if (!balance || !balance.supported) {
    return <p className="text-xs text-neutral-600">Balance: not available for this provider</p>;
  }
  const when = balance.polled_at ? ` · polled ${fmtTime(balance.polled_at)}` : '';
  switch (balance.status) {
    case 'pending':
      return <p className="text-xs text-neutral-500">Balance: not polled yet</p>;
    case 'error':
      return <p className="text-xs text-amber-400">Balance: last poll failed{when}</p>;
    case 'exhausted':
      return <p className="text-xs text-red-400">Balance: out of credits{when}</p>;
  }
  const rem = balance.remaining_usd;
  return (
    <p className="text-xs text-neutral-400">
      Balance: <span className="font-mono text-neutral-200">{rem === null ? 'unlimited' : usd(rem)}</span>
      {balance.limit_usd !== null && rem !== null ? <> of {usd(balance.limit_usd)}</> : null}
      <span className="text-neutral-600">{when}</span>
    </p>
  );
}

/** Inline monthly-budget editor: shows the budget, edits it, or clears it. */
function BudgetEditor({ keyInfo, onSave }: { keyInfo: ProviderKey; onSave: (budget: number | null) => Promise<boolean> }) {
  const current = keyInfo.monthly_budget_usd ?? null;
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState('');
  const [saving, setSaving] = useState(false);
  const [invalid, setInvalid] = useState(false);

  async function save(b: number | null) {
    setSaving(true);
    const ok = await onSave(b);
    setSaving(false);
    if (ok) setEditing(false);
  }

  if (!editing) {
    return (
      <button
        className="text-xs text-neutral-400 underline decoration-neutral-700 underline-offset-2 hover:text-neutral-200"
        onClick={() => { setValue(current === null ? '' : String(current)); setInvalid(false); setEditing(true); }}
      >
        {current === null ? 'Set monthly budget' : 'Edit budget'}
      </button>
    );
  }

  return (
    <form
      className="flex items-center gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        const n = Number(value);
        if (value.trim() === '' || !Number.isFinite(n) || n < 0) { setInvalid(true); return; }
        save(n);
      }}
    >
      <label htmlFor={`budget-${keyInfo.id}`} className="text-xs text-neutral-400">Budget $</label>
      <input
        id={`budget-${keyInfo.id}`} type="number" min="0" step="0.0001" inputMode="decimal" autoFocus
        className={`${inputCls} h-8 w-28 py-1 text-xs`} value={value}
        aria-invalid={invalid} onChange={(e) => { setValue(e.target.value); setInvalid(false); }}
      />
      <span className="text-xs text-neutral-500">/ month</span>
      <button type="submit" className={btnGhost} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      {current !== null && <button type="button" className={btnGhost} disabled={saving} onClick={() => save(null)}>Remove</button>}
      <button type="button" className={btnGhost} disabled={saving} onClick={() => setEditing(false)}>Cancel</button>
      {invalid && <span className="text-xs text-red-400" role="alert">Enter an amount of 0 or more.</span>}
    </form>
  );
}
