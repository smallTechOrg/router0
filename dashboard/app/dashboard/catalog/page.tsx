'use client';
import { useEffect, useState } from 'react';
import { useAuth, useLoader } from '@/lib/hooks';
import {
  ApiError, createAlias, deleteAlias, errorMessage, getCatalog, listAliases, providerLabelFor, refreshCatalog,
  type CatalogKind, type CatalogRefresh,
} from '@/lib/api';
import ProviderChip from '@/components/ProviderChip';
import {
  btnDanger, btnGhost, btnPrimary, Card, EmptyState, ErrorBanner, fmtTime, inputCls, Loading, Notice, PageHeader, tdCls, thCls,
} from '@/components/ui';

const KINDS: (CatalogKind | 'all')[] = ['all', 'chat', 'image', 'audio', 'embedding', 'other'];

function aliasError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 409) return `That alias already exists. Delete it first or choose another name. (${e.message})`;
    if (e.status === 422) return `Alias rejected: ${e.message}. An alias cannot have the same name as a real model in your catalog.`;
  }
  return errorMessage(e);
}

export default function CatalogPage() {
  const { token, canWrite } = useAuth();
  const [kind, setKind] = useState<CatalogKind | 'all'>('chat');
  const [qInput, setQInput] = useState('');
  const [q, setQ] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setQ(qInput.trim()), 300);
    return () => clearTimeout(t);
  }, [qInput]);

  const catalog = useLoader((t) => getCatalog(t, { q, kind: kind === 'all' ? '' : kind }), token, [q, kind]);
  const aliases = useLoader((t) => listAliases(t), token);

  const [refreshes, setRefreshes] = useState<CatalogRefresh[] | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [aliasName, setAliasName] = useState('');
  const [aliasTarget, setAliasTarget] = useState('');
  const [aliasBusy, setAliasBusy] = useState(false);
  const [aliasErr, setAliasErr] = useState('');

  const shownRefreshes = refreshes ?? catalog.data?.refreshes ?? [];

  async function onRefresh() {
    if (!token) return;
    setRefreshing(true); setError('');
    try {
      const r = await refreshCatalog(token);
      setRefreshes(r.refreshes);
      setNotice('Catalog refresh finished.');
      await catalog.reload(true);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setRefreshing(false);
    }
  }

  async function onAddAlias(e: React.FormEvent) {
    e.preventDefault();
    if (!token) return;
    setAliasErr(''); setAliasBusy(true);
    try {
      await createAlias(token, { alias: aliasName.trim().toLowerCase(), canonical_model: aliasTarget.trim() });
      setNotice(`Alias "${aliasName.trim().toLowerCase()}" added.`);
      setAliasName(''); setAliasTarget('');
      await aliases.reload(true);
    } catch (err) {
      setAliasErr(aliasError(err));
    } finally {
      setAliasBusy(false);
    }
  }

  async function onDeleteAlias(alias: string) {
    if (!token) return;
    if (!window.confirm(`Delete alias "${alias}"?`)) return;
    setError('');
    try {
      await deleteAlias(token, alias);
      setNotice(`Alias "${alias}" deleted.`);
      await aliases.reload(true);
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  const models = catalog.data?.models ?? [];
  const notServed = kind !== 'chat';

  return (
    <div className="space-y-6">
      <PageHeader
        title="Model catalog"
        subtitle="Canonical models available through your provider keys. Request any canonical name or alias in the model field."
        actions={canWrite ? (
          <button className={btnPrimary} onClick={onRefresh} disabled={refreshing}>{refreshing ? 'Refreshing…' : 'Refresh now'}</button>
        ) : undefined}
      />
      <ErrorBanner message={catalog.error || aliases.error} onRetry={() => { catalog.reload(); aliases.reload(); }} />
      <ErrorBanner message={error} />
      <Notice message={notice} />

      {shownRefreshes.length > 0 && (
        <Card title="Provider refresh status">
          <ul className="divide-y divide-neutral-800">
            {shownRefreshes.map((r) => (
              <li key={r.provider} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-2.5 text-sm">
                <span className="w-44 font-medium text-neutral-100">{providerLabelFor(r.provider)}</span>
                <span className={r.last_status === 'ok' ? 'text-emerald-400' : r.last_status === 'in_progress' ? 'text-amber-400' : 'text-red-400'}>
                  {r.last_status || 'unknown'}
                </span>
                <span className="text-neutral-400">{r.model_count} models</span>
                <span className="text-neutral-500">{r.last_refreshed_at ? fmtTime(r.last_refreshed_at) : 'never refreshed'}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Card title="Models">
        <div className="flex flex-col gap-3 border-b border-neutral-800 p-4 sm:flex-row sm:items-end">
          <div className="flex-1">
            <label htmlFor="c-q" className="mb-1.5 block text-sm font-medium text-neutral-300">Search</label>
            <input id="c-q" type="search" className={inputCls} value={qInput} onChange={(e) => setQInput(e.target.value)} placeholder="llama" />
          </div>
          <div>
            <label htmlFor="c-kind" className="mb-1.5 block text-sm font-medium text-neutral-300">Kind</label>
            <select id="c-kind" className={inputCls} value={kind} onChange={(e) => setKind(e.target.value as CatalogKind | 'all')}>
              {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
          </div>
        </div>
        {notServed && (
          <p className="border-b border-neutral-800 px-5 py-2.5 text-xs text-amber-300/90">
            Models of kind other than chat are shown for visibility only and are not served by router0.
          </p>
        )}
        {catalog.loading && !catalog.data ? (
          <Loading />
        ) : models.length === 0 ? (
          <EmptyState
            title="No models found"
            hint={q || kind !== 'chat' ? 'Try a different search or kind.' : canWrite ? 'Add a provider key, then click "Refresh now".' : 'Ask an org owner or admin to add a provider key.'}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <caption className="sr-only">Canonical models and the providers serving them</caption>
              <thead className="border-b border-neutral-800">
                <tr>
                  <th scope="col" className={thCls}>Model</th>
                  <th scope="col" className={thCls}>Kind</th>
                  <th scope="col" className={thCls}>Providers</th>
                  <th scope="col" className={thCls}>Last seen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-neutral-800">
                {models.map((m) => {
                  const lastSeen = m.providers.map((p) => p.last_seen_at).filter(Boolean).sort().pop();
                  return (
                    <tr key={m.canonical} className="align-top">
                      <td className={`${tdCls} font-mono text-neutral-100`}>{m.canonical}</td>
                      <td className={tdCls}>{m.kind}</td>
                      <td className={tdCls}>
                        <div className="flex flex-wrap gap-1.5">
                          {m.providers.map((p) => (
                            <ProviderChip key={`${p.provider}/${p.provider_model_id}`} provider={p.provider} modelId={p.provider_model_id} available={p.available} />
                          ))}
                        </div>
                      </td>
                      <td className={`${tdCls} whitespace-nowrap text-neutral-500`}>{fmtTime(lastSeen)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title="Aliases">
        {canWrite && (
          <form onSubmit={onAddAlias} className="grid gap-3 border-b border-neutral-800 p-4 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
            <div>
              <label htmlFor="a-name" className="mb-1.5 block text-sm font-medium text-neutral-300">Alias</label>
              <input id="a-name" className={inputCls} value={aliasName} onChange={(e) => setAliasName(e.target.value)} placeholder="fast" required />
            </div>
            <div>
              <label htmlFor="a-target" className="mb-1.5 block text-sm font-medium text-neutral-300">Canonical model</label>
              <input id="a-target" list="a-models" className={inputCls} value={aliasTarget} onChange={(e) => setAliasTarget(e.target.value)} placeholder="llama-3.3-70b" required />
              <datalist id="a-models">
                {models.filter((m) => m.kind === 'chat').map((m) => <option key={m.canonical} value={m.canonical} />)}
              </datalist>
            </div>
            <button type="submit" className={btnPrimary} disabled={aliasBusy}>{aliasBusy ? 'Adding…' : 'Add alias'}</button>
            {aliasErr && <p role="alert" className="text-sm text-red-300 sm:col-span-3">{aliasErr}</p>}
          </form>
        )}
        {aliases.loading && !aliases.data ? (
          <Loading />
        ) : (aliases.data?.aliases ?? []).length === 0 ? (
          <EmptyState title="No aliases" hint="An alias is a short name that resolves to a canonical model at request time." />
        ) : (
          <ul className="divide-y divide-neutral-800">
            {aliases.data!.aliases.map((a) => (
              <li key={a.alias} className="flex flex-wrap items-center gap-3 px-5 py-3 text-sm">
                <span className="font-mono text-neutral-100">{a.alias}</span>
                <span aria-label="resolves to" className="text-neutral-500">→</span>
                <span className="min-w-0 flex-1 truncate font-mono text-neutral-300">{a.canonical_model}</span>
                <span className="text-xs text-neutral-500">{fmtTime(a.created_at)}</span>
                {canWrite && <button className={btnDanger} onClick={() => onDeleteAlias(a.alias)} aria-label={`Delete alias ${a.alias}`}>Delete</button>}
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
