'use client';
import { useEffect, useState } from 'react';
import { useSession } from 'next-auth/react';
import { useRouter } from 'next/navigation';

export default function AdminPage() {
  const { data: session, status } = useSession() as any;
  const router = useRouter();

  const [users, setUsers] = useState<any[]>([]);
  const [loadingUsers, setLoadingUsers] = useState(true);

  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteResult, setInviteResult] = useState<any>(null);
  const [inviteError, setInviteError] = useState('');
  const [inviteLoading, setInviteLoading] = useState(false);

  useEffect(() => {
    if (status === 'loading') return;
    if ((session as any)?.user?.role !== 'admin') {
      router.replace('/dashboard');
    }
  }, [session, status]);

  async function loadUsers() {
    if (!session?.accessToken) return;
    try {
      const res = await fetch('/admin/users', {
        headers: { Authorization: `Bearer ${session.accessToken}` },
      });
      const data = await res.json();
      setUsers(data.users || []);
    } catch {
      setUsers([]);
    } finally {
      setLoadingUsers(false);
    }
  }

  useEffect(() => { loadUsers(); }, [session]);

  async function handleCreateInvite(e: React.FormEvent) {
    e.preventDefault();
    setInviteLoading(true);
    setInviteError('');
    setInviteResult(null);
    try {
      const res = await fetch('/admin/invites', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${session?.accessToken}`,
        },
        body: JSON.stringify({ email: inviteEmail }),
      });
      const data = await res.json();
      if (!res.ok) {
        setInviteError(data?.error?.message || 'Failed to create invite');
        return;
      }
      setInviteResult(data);
      setInviteEmail('');
      loadUsers();
    } catch {
      setInviteError('Network error');
    } finally {
      setInviteLoading(false);
    }
  }

  if (status === 'loading' || (session as any)?.user?.role !== 'admin') {
    return null;
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-white">Admin Panel</h1>
        <p className="mt-1 text-sm text-neutral-400">Manage invites and users.</p>
      </div>

      {/* Create invite */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6">
        <h2 className="text-sm font-semibold text-neutral-200 mb-4">Create invite</h2>
        <form className="space-y-4" onSubmit={handleCreateInvite}>
          {inviteError && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300">
              {inviteError}
            </div>
          )}
          {inviteResult && (
            <div className="rounded-lg border border-green-500/30 bg-green-500/10 p-4 space-y-2">
              <p className="text-sm font-medium text-green-300">Invite created — share this token with the user:</p>
              <div className="flex items-center gap-2">
                <code className="flex-1 rounded bg-neutral-800 px-3 py-2 font-mono text-sm text-white break-all">
                  {inviteResult.token}
                </code>
                <button
                  type="button"
                  onClick={() => navigator.clipboard.writeText(inviteResult.token)}
                  className="shrink-0 rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-xs text-neutral-300 hover:text-white transition-colors"
                >
                  Copy
                </button>
              </div>
              <p className="text-xs text-neutral-500">
                Expires: {new Date(inviteResult.expires_at).toLocaleDateString()} ·
                User registers at{' '}
                <span className="font-mono text-neutral-400">
                  {typeof window !== 'undefined' ? window.location.origin : ''}/auth/register
                </span>
              </p>
            </div>
          )}
          <div className="flex gap-3">
            <input
              type="email"
              value={inviteEmail}
              onChange={e => setInviteEmail(e.target.value)}
              required
              placeholder="newuser@example.com"
              className="flex-1 rounded-lg border border-neutral-700 bg-neutral-800 px-3 py-2 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none"
            />
            <button
              type="submit"
              disabled={inviteLoading}
              className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50 transition-colors"
            >
              {inviteLoading ? 'Creating…' : 'Create invite'}
            </button>
          </div>
        </form>
      </div>

      {/* Users table */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="px-6 py-4 border-b border-neutral-800 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-neutral-200">All users</h2>
          <button
            onClick={loadUsers}
            className="text-xs text-neutral-500 hover:text-neutral-300 transition-colors"
          >
            Refresh
          </button>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-neutral-800">
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Email</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Role</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Status</th>
              <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Joined</th>
            </tr>
          </thead>
          <tbody>
            {loadingUsers ? (
              <tr>
                <td colSpan={4} className="px-6 py-8 text-center text-neutral-500">Loading…</td>
              </tr>
            ) : users.length === 0 ? (
              <tr>
                <td colSpan={4} className="px-6 py-8 text-center text-neutral-500">No users yet.</td>
              </tr>
            ) : users.map((u: any) => (
              <tr key={u.id} className="border-b border-neutral-800/50">
                <td className="px-6 py-3 text-neutral-200">{u.email}</td>
                <td className="px-6 py-3">
                  <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                    u.role === 'admin'
                      ? 'bg-amber-900/40 text-amber-400'
                      : 'bg-neutral-800 text-neutral-400'
                  }`}>
                    {u.role}
                  </span>
                </td>
                <td className="px-6 py-3">
                  <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                    u.status === 'active'
                      ? 'bg-green-900/40 text-green-400'
                      : 'bg-red-900/40 text-red-400'
                  }`}>
                    {u.status}
                  </span>
                </td>
                <td className="px-6 py-3 text-neutral-500 text-xs">
                  {new Date(u.created_at).toLocaleDateString()}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
