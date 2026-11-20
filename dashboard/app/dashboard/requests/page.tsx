'use client';
import { Fragment, useCallback, useEffect, useState } from 'react';
import { useAuth, useLoader } from '@/lib/hooks';
import { ApiError, errorMessage, listRequests, listTokens, type RequestLog } from '@/lib/api';
import FailoverTrail from '@/components/FailoverTrail';
import { btnGhost, Card, EmptyState, ErrorBanner, fmtTime, inputCls, Loading, PageHeader, tdCls, thCls } from '@/components/ui';
import { useRouter } from 'next/navigation';

const PAGE = 50;

export default function RequestsPage() {
  const { token } = useAuth();
  const router = useRouter();
  const tokens = useLoader((t) => listTokens(t), token);

  const [status, setStatus] = useState<'' | 'success' | 'error'>('');
  const [model, setModel] = useState('');
  const [tokenId, setTokenId] = useState('');
  const [auto, setAuto] = useState(false);

  const [rows, setRows] = useState<RequestLog[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [more, setMore] = useState(false);
  const [error, setError] = useState('');
  const [open, setOpen] = useState<string | null>(null);

  const handle = useCallback((e: unknown) => {
    if (e instanceof ApiError && e.status === 401) router.replace('/auth/login');
    else setError(errorMessage(e));
  }, [router]);

  const loadFirst = useCallback(async (silent = false) => {
    if (!token) return;
    if (!silent) setLoading(true);
    try {
      const r = await listRequests(token, { limit: PAGE, status, model: model.trim(), token_id: tokenId });
      setRows(r.requests);
      setNext(r.next_before ?? null);
      setError('');
    } catch (e) { handle(e); } finally { setLoading(false); }
  }, [token, status, model, tokenId, handle]);

  // Debounce so typing in the model filter does not fire a request per keystroke.
  useEffect(() => {
    const t = setTimeout(() => loadFirst(), 250);
    return () => clearTimeout(t);
  }, [loadFirst]);

  useEffect(() => {
    if (!auto) return;
    const id = setInterval(() => loadFirst(true), 10000);
    return () => clearInterval(id);
  }, [auto, loadFirst]);

  async function loadOlder() {
    if (!token || !next) return;
    setMore(true);
    try {
      const r = await listRequests(token, { limit: PAGE, before: next, status, model: model.trim(), token_id: tokenId });
      setRows((prev) => [...prev, ...r.requests]);
      setNext(r.next_before ?? null);
    } catch (e) { handle(e); } finally { setMore(false); }
  }

  const tokenList = tokens.data?.tokens ?? [];

  return (
    <div className="space-y-6">
      <PageHeader title="Requests" subtitle="Every gateway request with its provider, key and failover trail. Prompt and response content is never stored." />
      <ErrorBanner message={error} onRetry={() => loadFirst()} />

      <Card>
        <div className="flex flex-wrap items-end gap-3 p-4">
          <div>
            <label htmlFor="f-status" className="mb-1 block text-xs text-neutral-400">Status</label>
            <select id="f-status" className={inputCls} value={status} onChange={(e) => setStatus(e.target.value as any)}>
              <option value="">All</option>
              <option value="success">Success</option>
              <option value="error">Error</option>
            </select>
          </div>
          <div>
            <label htmlFor="f-model" className="mb-1 block text-xs text-neutral-400">Model</label>
            <input id="f-model" className={inputCls} value={model} onChange={(e) => setModel(e.target.value)} placeholder="llama-3.3-70b" />
          </div>
          <div>
            <label htmlFor="f-token" className="mb-1 block text-xs text-neutral-400">Token</label>
            <select id="f-token" className={inputCls} value={tokenId} onChange={(e) => setTokenId(e.target.value)}>
              <option value="">All</option>
              {tokenList.map((t) => <option key={t.id} value={t.id}>{t.name}{t.revoked_at ? ' (revoked)' : ''}</option>)}
            </select>
          </div>
          <label className="flex items-center gap-2 pb-2.5 text-sm text-neutral-300">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
            Auto-refresh (10 s)
          </label>
        </div>

        <div className="overflow-x-auto border-t border-neutral-800">
          <table className="w-full text-sm">
            <caption className="sr-only">Request log</caption>
            <thead>
              <tr className="border-b border-neutral-800">
                {['Time', 'Token', 'Model', 'Provider', 'Key', 'Status', 'Latency', 'Tokens in/out', 'Attempts'].map((h) => (
                  <th key={h} scope="col" className={thCls}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const isOpen = open === r.request_id;
                return (
                  <Fragment key={r.request_id}>
                    <tr className="border-b border-neutral-800/50 hover:bg-neutral-800/40">
                      <td className={`${tdCls} whitespace-nowrap text-xs`}>
                        <button
                          className="mr-2 text-neutral-500 hover:text-white focus:outline-none focus:ring-2 focus:ring-indigo-400 rounded"
                          onClick={() => setOpen(isOpen ? null : r.request_id)}
                          aria-expanded={isOpen}
                          aria-label={`${isOpen ? 'Hide' : 'Show'} failover trail for ${r.request_id}`}
                        >{isOpen ? '▾' : '▸'}</button>
                        {fmtTime(r.created_at)}
                      </td>
                      <td className={tdCls}>{r.token_name || '—'}</td>
                      <td className={`${tdCls} font-mono text-xs`}>{r.canonical_model || '—'}</td>
                      <td className={tdCls}>{r.provider || '—'}</td>
                      <td className={tdCls}>{r.key_label || '—'}</td>
                      <td className={tdCls}>
                        <span className={`inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset ${
                          r.status === 'success' ? 'bg-emerald-500/15 text-emerald-400 ring-emerald-500/20' : 'bg-red-500/15 text-red-400 ring-red-500/20'}`}>
                          {r.http_status ?? '—'}{r.error_code ? ` ${r.error_code}` : ''}
                        </span>
                      </td>
                      <td className={`${tdCls} whitespace-nowrap`}>{r.latency_ms != null ? `${r.latency_ms} ms` : '—'}</td>
                      <td className={`${tdCls} whitespace-nowrap`}>{r.input_tokens ?? '—'} / {r.output_tokens ?? '—'}</td>
                      <td className={tdCls}>{r.attempt_count}</td>
                    </tr>
                    {isOpen && (
                      <tr className="border-b border-neutral-800/50 bg-neutral-950/60">
                        <td colSpan={9} className="px-4 py-3">
                          <p className="mb-2 font-mono text-xs text-neutral-500">{r.request_id}{r.stream ? ' · stream' : ''}</p>
                          <FailoverTrail trail={r.trail} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
        {loading && rows.length === 0 && <Loading />}
        {!loading && !error && rows.length === 0 && (
          <EmptyState title="No requests found" hint="Call /v1/chat/completions with a service token, or relax the filters." />
        )}
        {next && (
          <div className="border-t border-neutral-800 p-4 text-center">
            <button className={btnGhost} onClick={loadOlder} disabled={more}>{more ? 'Loading…' : 'Load older'}</button>
          </div>
        )}
      </Card>
    </div>
  );
}
