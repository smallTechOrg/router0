'use client';
import { useEffect, useState } from 'react';
import { useSession } from 'next-auth/react';

const PROVIDERS = ['groq', 'google', 'openrouter', 'nvidia', 'together', 'baseten'];

export default function KeysPage() {
  const { data: session } = useSession() as any;
  const [keys, setKeys] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ provider: 'groq', key: '', label: '', model_id: '' });
  const [error, setError] = useState('');

  async function loadKeys() {
    if (!session?.accessToken) return;
    try {
      const res = await fetch('/api/keys', { headers: { Authorization: `Bearer ${session.accessToken}` } });
      const data = await res.json();
      setKeys(data.keys || []);
    } catch {
      setKeys([]);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { loadKeys(); }, [session]);

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    setError('');
    setAdding(true);
    try {
      const res = await fetch('/api/keys', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${session.accessToken}` },
        body: JSON.stringify(form),
      });
      const data = await res.json();
      if (!res.ok) { setError(data?.error?.message || 'Failed to add key'); return; }
      setForm({ provider: 'groq', key: '', label: '', model_id: '' });
      loadKeys();
    } catch { setError('Network error'); }
    finally { setAdding(false); }
  }

  async function handleDelete(id: string) {
    if (!confirm('Delete this key?')) return;
    await fetch(`/api/keys/${id}`, { method: 'DELETE', headers: { Authorization: `Bearer ${session.accessToken}` } });
    loadKeys();
  }

  async function handleTest(id: string) {
    const res = await fetch(`/api/keys/${id}/test`, { method: 'POST', headers: { Authorization: `Bearer ${session.accessToken}` } });
    const data = await res.json();
    alert(`Test result: ${data.status}${data.latency_ms ? ` (${data.latency_ms}ms)` : ''}`);
  }

  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Provider Keys</h1>
        <p className="mt-1 text-sm text-neutral-400">Manage your encrypted provider API keys.</p>
      </div>

      {/* Add key form */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6 mb-6">
        <h2 className="text-sm font-semibold text-neutral-200 mb-4">Add a new key</h2>
        <form className="space-y-4" onSubmit={handleAdd}>
          {error && <div className="text-sm text-red-400">{error}</div>}
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-xs font-medium text-neutral-400 mb-1">Provider</label>
              <select
                value={form.provider} onChange={e => setForm(f => ({ ...f, provider: e.target.value }))}
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 focus:border-indigo-500 focus:outline-none"
              >
                {PROVIDERS.map(p => <option key={p} value={p}>{p}</option>)}
              </select>
            </div>
            <div>
              <label className="block text-xs font-medium text-neutral-400 mb-1">Label (optional)</label>
              <input
                type="text" value={form.label} onChange={e => setForm(f => ({ ...f, label: e.target.value }))}
                placeholder={form.provider + ' key'}
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 focus:border-indigo-500 focus:outline-none"
              />
            </div>
          </div>
          <div>
            <label className="block text-xs font-medium text-neutral-400 mb-1">API Key</label>
            <input
              type="password" value={form.key} onChange={e => setForm(f => ({ ...f, key: e.target.value }))} required
              className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 focus:border-indigo-500 focus:outline-none"
            />
          </div>
          {form.provider === 'baseten' && (
            <div>
              <label className="block text-xs font-medium text-neutral-400 mb-1">Model ID (baseten)</label>
              <input
                type="text" value={form.model_id} onChange={e => setForm(f => ({ ...f, model_id: e.target.value }))} required
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 focus:border-indigo-500 focus:outline-none"
              />
            </div>
          )}
          <button
            type="submit" disabled={adding}
            className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50 transition-colors"
          >
            {adding ? 'Adding…' : 'Add key'}
          </button>
        </form>
      </div>

      {/* Keys list */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="px-6 py-4 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Your keys</h2>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-neutral-800">
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Provider</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Label</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Key hint</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Status</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Actions</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr><td colSpan={5} className="px-6 py-8 text-center text-neutral-500">Loading…</td></tr>
            ) : keys.length === 0 ? (
              <tr><td colSpan={5} className="px-6 py-8 text-center text-neutral-500">No keys yet.</td></tr>
            ) : keys.map((k: any) => (
              <tr key={k.id} className="border-b border-neutral-800/50">
                <td className="px-6 py-3 text-neutral-200 capitalize">{k.provider}</td>
                <td className="px-6 py-3 text-neutral-400">{k.label}</td>
                <td className="px-6 py-3 font-mono text-xs text-neutral-500">{k.key_hint}</td>
                <td className="px-6 py-3">
                  <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                    k.status === 'active' ? 'bg-green-900/40 text-green-400' : 'bg-red-900/40 text-red-400'
                  }`}>{k.status}</span>
                </td>
                <td className="px-6 py-3 flex gap-2">
                  <button onClick={() => handleTest(k.id)} className="text-xs text-indigo-400 hover:text-indigo-300">Test</button>
                  <button onClick={() => handleDelete(k.id)} className="text-xs text-red-400 hover:text-red-300">Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
