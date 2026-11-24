'use client';
import { useEffect, useState } from 'react';
import { useAuth, useLoader } from '@/lib/hooks';
import { useApiBase } from '@/lib/useApiBase';
import { errorMessage, getPriority, putPriority } from '@/lib/api';
import { btnGhost, btnPrimary, Card, CopyButton, EmptyState, ErrorBanner, Loading, Notice, PageHeader } from '@/components/ui';

const exampleFor = (base: string) => `curl ${base}/chat/completions \\
  -H "Authorization: Bearer r0-sk-..." \\
  -H "X-Router0-Providers: groq,openrouter" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"llama-3.3-70b","messages":[{"role":"user","content":"hi"}]}'`;

export default function RoutingPage() {
  const { token, canWrite } = useAuth();
  const EXAMPLE = exampleFor(useApiBase());
  const { data, error, loading, reload, setData } = useLoader((t) => getPriority(t), token);
  const [order, setOrder] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [notice, setNotice] = useState('');

  useEffect(() => {
    if (data) setOrder([...data.providers].sort((a, b) => a.position - b.position).map((p) => p.provider));
  }, [data]);

  const saved = data ? [...data.providers].sort((a, b) => a.position - b.position).map((p) => p.provider) : [];
  const dirty = order.join(',') !== saved.join(',');
  const keyCount = (p: string) => data?.providers.find((x) => x.provider === p)?.keys.length ?? 0;

  function move(i: number, dir: -1 | 1) {
    const j = i + dir;
    if (j < 0 || j >= order.length) return;
    const next = [...order];
    [next[i], next[j]] = [next[j], next[i]];
    setOrder(next);
  }

  // Native HTML5 drag-and-drop: the dragged row moves live as it passes over
  // others; Save persists. The ▲ ▼ buttons stay for keyboard users.
  const [dragging, setDragging] = useState<string | null>(null);
  function dragOver(e: React.DragEvent, over: string) {
    if (!dragging) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    if (over === dragging) return;
    const next = order.filter((p) => p !== dragging);
    next.splice(order.indexOf(over), 0, dragging);
    setOrder(next);
  }

  async function save() {
    if (!token) return;
    setSaving(true); setSaveError('');
    try {
      setData(await putPriority(token, { providers: order }));
      setNotice('Provider priority saved.');
    } catch (e) {
      setSaveError(errorMessage(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader title="Routing" subtitle="Provider priority for your org. router0 tries providers top to bottom and, within each, the keys in their order. Drag rows (or use the arrows), then Save." />
      <ErrorBanner message={error} onRetry={() => reload()} />
      <ErrorBanner message={saveError} />
      <Notice message={notice} />

      <Card title="Provider priority" actions={canWrite ? (
        <button className={btnPrimary} onClick={save} disabled={!dirty || saving}>{saving ? 'Saving…' : 'Save'}</button>
      ) : undefined}>
        {loading && !data ? <Loading /> : order.length === 0 && !error ? (
          <EmptyState title="No providers with keys yet" hint="Add a provider key on the Provider Keys page first." />
        ) : (
          <ol className="divide-y divide-neutral-800">
            {order.map((p, i) => (
              <li
                key={p}
                draggable={canWrite}
                onDragStart={(e) => { setDragging(p); e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', p); }}
                onDragOver={(e) => dragOver(e, p)}
                onDrop={(e) => { e.preventDefault(); setDragging(null); }}
                onDragEnd={() => setDragging(null)}
                className={`flex items-center gap-3 px-5 py-3 ${canWrite ? 'cursor-grab active:cursor-grabbing' : ''} ${dragging === p ? 'bg-neutral-800/60 opacity-70' : ''}`}
              >
                {canWrite && <span className="select-none text-neutral-600" aria-hidden="true" title="Drag to reorder">⋮⋮</span>}
                <span className="w-5 text-xs text-neutral-500">{i + 1}</span>
                <span className="flex-1 text-sm font-medium text-neutral-100">{p}</span>
                <span className="text-xs text-neutral-500">{keyCount(p)} key{keyCount(p) === 1 ? '' : 's'}</span>
                {canWrite && (
                  <div className="flex gap-1.5">
                    <button className={btnGhost} onClick={() => move(i, -1)} disabled={i === 0} aria-label={`Move ${p} up`}>▲</button>
                    <button className={btnGhost} onClick={() => move(i, 1)} disabled={i === order.length - 1} aria-label={`Move ${p} down`}>▼</button>
                  </div>
                )}
              </li>
            ))}
          </ol>
        )}
      </Card>

      <Card title="Per-request override" actions={<CopyButton text={EXAMPLE} />}>
        <div className="space-y-3 px-5 py-4 text-sm text-neutral-400">
          <p>
            Send the <code className="text-neutral-200">X-Router0-Providers</code> header to restrict <em>and</em> order providers for a single
            request, overriding the saved priority. Names are case-insensitive; <code className="text-neutral-200">gemini</code> is accepted for{' '}
            <code className="text-neutral-200">google</code>. Unknown names return 400; providers without keys in your org are ignored.
          </p>
          <pre className="overflow-x-auto rounded-lg bg-neutral-950 p-3 text-xs text-neutral-300"><code>{EXAMPLE}</code></pre>
        </div>
      </Card>
    </div>
  );
}
