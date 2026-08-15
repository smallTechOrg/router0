'use client';
import { useState } from 'react';
import { useSession } from 'next-auth/react';

export default function AdminInvitesPage() {
  const { data: session } = useSession() as any;
  const [email, setEmail] = useState('');
  const [result, setResult] = useState<any>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError('');
    setResult(null);
    try {
      const res = await fetch('/admin/invites', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${session?.accessToken}` },
        body: JSON.stringify({ email }),
      });
      const data = await res.json();
      if (!res.ok) { setError(data?.error?.message || 'Failed'); return; }
      setResult(data);
      setEmail('');
    } catch { setError('Network error'); }
    finally { setLoading(false); }
  }

  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Admin: Invites</h1>
        <p className="mt-1 text-sm text-neutral-400">Create invite tokens for new users.</p>
      </div>
      <div className="max-w-md rounded-xl border border-neutral-800 bg-neutral-900 p-6">
        <form className="space-y-4" onSubmit={handleCreate}>
          {error && <div className="text-sm text-red-400">{error}</div>}
          {result && (
            <div className="rounded-lg border border-green-500/30 bg-green-500/10 p-4">
              <p className="text-sm text-green-300 mb-2">Invite created!</p>
              <p className="text-xs text-neutral-400 mb-1">Token (share with user):</p>
              <p className="font-mono text-sm text-white break-all">{result.token}</p>
              <p className="text-xs text-neutral-500 mt-2">Expires: {new Date(result.expires_at).toLocaleDateString()}</p>
            </div>
          )}
          <div>
            <label className="block text-xs font-medium text-neutral-400 mb-1">Email (for reference)</label>
            <input
              type="email" value={email} onChange={e => setEmail(e.target.value)} required
              placeholder="newuser@example.com"
              className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 focus:border-indigo-500 focus:outline-none"
            />
          </div>
          <button
            type="submit" disabled={loading}
            className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50 transition-colors"
          >
            {loading ? 'Creating…' : 'Create invite'}
          </button>
        </form>
      </div>
    </div>
  );
}
