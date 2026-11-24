'use client';
import { useState } from 'react';
import { useAuth, useLoader } from '@/lib/hooks';
import { createToken, errorMessage, listTokens, revokeToken, updateToken, type CreatedToken, type ServiceToken } from '@/lib/api';
import BudgetEditor from '@/components/BudgetEditor';
import AllowedModelsEditor from '@/components/AllowedModelsEditor';
import SpendBar, { usd } from '@/components/SpendBar';
import TokenCreatedModal from '@/components/TokenCreatedModal';
import { btnDanger, btnPrimary, Card, EmptyState, ErrorBanner, fmtTime, inputCls, Loading, Notice, PageHeader, tdCls, thCls } from '@/components/ui';

const NAME_RE = /^[A-Za-z0-9._-]{1,64}$/;

function statusOf(t: ServiceToken) {
  if (t.revoked_at) return { label: 'revoked', cls: 'bg-red-500/15 text-red-400 ring-red-500/20' };
  const budget = t.monthly_budget_usd;
  if (typeof budget === 'number' && Math.round((t.month_spend_usd ?? 0) * 1e6) >= Math.round(budget * 1e6)) {
    return { label: 'budget reached', cls: 'bg-orange-500/15 text-orange-400 ring-orange-500/20' };
  }
  if (t.legacy) return { label: 'legacy', cls: 'bg-amber-500/15 text-amber-400 ring-amber-500/20' };
  return { label: 'live', cls: 'bg-emerald-500/15 text-emerald-400 ring-emerald-500/20' };
}

export default function TokensPage() {
  const { token, canWrite } = useAuth();
  const { data, error, loading, reload } = useLoader((t) => listTokens(t), token);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');
  const [notice, setNotice] = useState('');
  const [created, setCreated] = useState<CreatedToken | null>(null);

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!token) return;
    setFormError('');
    if (!NAME_RE.test(name)) {
      setFormError('Name must be 1-64 characters: letters, digits, dot, underscore or hyphen.');
      return;
    }
    setBusy(true);
    try {
      setCreated(await createToken(token, name));
      setName('');
      reload(true);
    } catch (err) {
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onRevoke(t: ServiceToken) {
    if (!token) return;
    if (!window.confirm(`Revoke token "${t.name}"? Services using it will immediately get 401.`)) return;
    setFormError('');
    try {
      await revokeToken(token, t.id);
      setNotice(`Token "${t.name}" revoked.`);
      reload(true);
    } catch (err) {
      setFormError(errorMessage(err));
    }
  }

  async function onSaveBudget(t: ServiceToken, budget: number | null) {
    if (!token) return false;
    setFormError('');
    try {
      await updateToken(token, t.id, { monthly_budget_usd: budget });
      setNotice(budget === null ? `Budget of "${t.name}" removed.` : `Budget of "${t.name}" set to ${usd(budget)} per month.`);
      reload(true);
      return true;
    } catch (err) {
      setFormError(errorMessage(err));
      return false;
    }
  }

  async function onSaveModels(t: ServiceToken, models: string[] | null) {
    if (!token) return false;
    setFormError('');
    try {
      await updateToken(token, t.id, { allowed_models: models });
      setNotice(models === null ? `"${t.name}" may use every model.` : `"${t.name}" may use only: ${models.join(', ')}.`);
      reload(true);
      return true;
    } catch (err) {
      setFormError(errorMessage(err));
      return false;
    }
  }

  const tokens = data?.tokens ?? [];

  return (
    <div className="space-y-6">
      <PageHeader title="Service tokens" subtitle="Tokens your services use to call router0 at /v1. The full token is shown only once, at creation. Optionally restrict a token to some models (others get 403 model_not_allowed) and cap its estimated monthly spend (over it, 402 token_budget_exceeded until the 1st, UTC)." />
      <ErrorBanner message={error} onRetry={() => reload()} />
      <ErrorBanner message={formError} />
      <Notice message={notice} />

      {canWrite && (
        <Card title="Create token">
          <form onSubmit={onCreate} className="flex flex-col gap-3 p-5 sm:flex-row sm:items-end">
            <div className="flex-1">
              <label htmlFor="token-name" className="mb-1.5 block text-sm font-medium text-neutral-300">Name</label>
              <input id="token-name" className={inputCls} value={name} onChange={(e) => setName(e.target.value)} placeholder="billing-worker" maxLength={64} required />
            </div>
            <button type="submit" className={btnPrimary} disabled={busy}>{busy ? 'Creating…' : 'Create token'}</button>
          </form>
        </Card>
      )}

      <Card>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <caption className="sr-only">Service tokens</caption>
            <thead>
              <tr className="border-b border-neutral-800">
                <th scope="col" className={thCls}>Name</th>
                <th scope="col" className={thCls}>Prefix</th>
                <th scope="col" className={`${thCls} hidden 2xl:table-cell`}>Created by</th>
                <th scope="col" className={`${thCls} hidden xl:table-cell`}>Created</th>
                <th scope="col" className={thCls}>Last used</th>
                <th scope="col" className={thCls}>Models</th>
                <th scope="col" className={thCls}>This month</th>
                <th scope="col" className={thCls}>Status</th>
                {canWrite && <th scope="col" className={thCls}><span className="sr-only">Actions</span></th>}
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => {
                const s = statusOf(t);
                return (
                  <tr key={t.id} className="border-b border-neutral-800/50">
                    <td className={`${tdCls} font-medium text-neutral-100`}>{t.name}</td>
                    <td className={`${tdCls} font-mono text-xs`}>{t.prefix}…</td>
                    <td className={`${tdCls} hidden 2xl:table-cell`}>{t.created_by || '—'}</td>
                    <td className={`${tdCls} hidden text-xs xl:table-cell`}>{fmtTime(t.created_at)}</td>
                    <td className={`${tdCls} text-xs`}>{fmtTime(t.last_used_at)}</td>
                    <td className={tdCls}>
                      {t.revoked_at ? <span className="text-xs text-neutral-600">—</span> : (
                        <AllowedModelsEditor id={t.id} current={t.allowed_models ?? null} canWrite={canWrite} onSave={(m) => onSaveModels(t, m)} />
                      )}
                    </td>
                    <td className={tdCls}>
                      {t.revoked_at ? <span className="text-xs text-neutral-600">—</span> : (
                        <div className="space-y-1">
                          <SpendBar spend={t.month_spend_usd ?? 0} budget={t.monthly_budget_usd} className="w-44" />
                          {canWrite && <BudgetEditor id={t.id} current={t.monthly_budget_usd ?? null} onSave={(b) => onSaveBudget(t, b)} />}
                        </div>
                      )}
                    </td>
                    <td className={tdCls}>
                      <span className={`inline-flex whitespace-nowrap rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset ${s.cls}`}>{s.label}</span>
                    </td>
                    {canWrite && (
                      <td className={`${tdCls} text-right`}>
                        {!t.revoked_at && <button className={btnDanger} onClick={() => onRevoke(t)}>Revoke</button>}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {loading && !data && <Loading />}
        {!loading && !error && tokens.length === 0 && (
          <EmptyState title="No service tokens yet" hint={canWrite ? 'Create one above to start calling /v1.' : 'Ask an org owner or admin to create one.'} />
        )}
      </Card>

      {created && <TokenCreatedModal name={created.name} token={created.token} onClose={() => setCreated(null)} />}
    </div>
  );
}
