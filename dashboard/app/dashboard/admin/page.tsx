'use client';
import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth, useLoader } from '@/lib/hooks';
import { createInvite, errorMessage, listAdminOrgs, listAdminUsers, type Invite } from '@/lib/api';
import { btnPrimary, Card, CopyButton, EmptyState, ErrorBanner, fmtTime, inputCls, Loading, PageHeader, tdCls, thCls } from '@/components/ui';

export default function AdminPage() {
  const { token, isPlatformAdmin, sessionLoading } = useAuth();
  const router = useRouter();
  const users = useLoader((t) => listAdminUsers(t), isPlatformAdmin ? token : undefined);
  const orgs = useLoader((t) => listAdminOrgs(t), isPlatformAdmin ? token : undefined);

  const [email, setEmail] = useState('');
  const [orgId, setOrgId] = useState('');
  const [orgRole, setOrgRole] = useState<'member' | 'admin'>('member');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [invite, setInvite] = useState<Invite | null>(null);

  useEffect(() => {
    if (!sessionLoading && !isPlatformAdmin) router.replace('/dashboard');
  }, [sessionLoading, isPlatformAdmin, router]);

  async function onInvite(e: React.FormEvent) {
    e.preventDefault();
    if (!token) return;
    setBusy(true); setError(''); setInvite(null);
    try {
      const inv = await createInvite(token, {
        ...(email ? { email } : {}),
        ...(orgId ? { org_id: orgId, org_role: orgRole } : {}),
      });
      setInvite(inv);
      setEmail('');
      users.reload(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  if (sessionLoading || !isPlatformAdmin) return null;

  const orgList = orgs.data?.orgs ?? [];
  const userList = users.data?.users ?? [];
  const registerUrl = typeof window !== 'undefined' ? `${window.location.origin}/auth/register` : '/auth/register';

  return (
    <div className="space-y-6">
      <PageHeader title="Admin" subtitle="Platform administration: invites, organizations and users." />
      <ErrorBanner message={users.error || orgs.error} onRetry={() => { users.reload(); orgs.reload(); }} />

      <Card title="Create invite">
        <form onSubmit={onInvite} className="space-y-4 p-5">
          <ErrorBanner message={error} />
          {invite && (
            <div role="status" className="space-y-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-4">
              <p className="text-sm font-medium text-emerald-300">Invite created. Share this token (email is not sent):</p>
              <div className="flex items-center gap-2">
                <code className="flex-1 break-all rounded bg-neutral-800 px-3 py-2 font-mono text-sm text-white">{invite.token}</code>
                <CopyButton text={invite.token} />
              </div>
              <p className="text-xs text-neutral-400">
                Expires {fmtTime(invite.expires_at)}. Registration page: <span className="font-mono">{registerUrl}</span>
              </p>
            </div>
          )}
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4 lg:items-end">
            <div>
              <label htmlFor="i-email" className="mb-1.5 block text-sm font-medium text-neutral-300">Email (optional)</label>
              <input id="i-email" type="email" className={inputCls} value={email} onChange={(e) => setEmail(e.target.value)} placeholder="user@example.com" />
            </div>
            <div>
              <label htmlFor="i-org" className="mb-1.5 block text-sm font-medium text-neutral-300">Organization</label>
              <select id="i-org" className={inputCls} value={orgId} onChange={(e) => setOrgId(e.target.value)}>
                <option value="">New organization (invitee becomes owner)</option>
                {orgList.map((o) => <option key={o.id} value={o.id}>Join: {o.name}</option>)}
              </select>
            </div>
            <div>
              <label htmlFor="i-role" className="mb-1.5 block text-sm font-medium text-neutral-300">Org role</label>
              <select id="i-role" className={inputCls} value={orgRole} onChange={(e) => setOrgRole(e.target.value as any)} disabled={!orgId}>
                <option value="member">member (read-only)</option>
                <option value="admin">admin</option>
              </select>
            </div>
            <button type="submit" className={btnPrimary} disabled={busy}>{busy ? 'Creating…' : 'Create invite'}</button>
          </div>
        </form>
      </Card>

      <Card title="Organizations">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <caption className="sr-only">Organizations</caption>
            <thead><tr className="border-b border-neutral-800">
              {['Name', 'Members', 'Tokens', 'Keys'].map((h) => <th key={h} scope="col" className={thCls}>{h}</th>)}
            </tr></thead>
            <tbody>
              {orgList.map((o) => (
                <tr key={o.id} className="border-b border-neutral-800/50">
                  <td className={`${tdCls} text-neutral-100`}>{o.name}</td>
                  <td className={tdCls}>{o.member_count ?? o.members ?? '—'}</td>
                  <td className={tdCls}>{o.token_count ?? o.tokens ?? '—'}</td>
                  <td className={tdCls}>{o.key_count ?? o.keys ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {orgs.loading && !orgs.data && <Loading />}
        {!orgs.loading && !orgs.error && orgList.length === 0 && <EmptyState title="No organizations yet" />}
      </Card>

      <Card title="Users">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <caption className="sr-only">Users</caption>
            <thead><tr className="border-b border-neutral-800">
              {['Email', 'Platform role', 'Status', 'Org', 'Joined'].map((h) => <th key={h} scope="col" className={thCls}>{h}</th>)}
            </tr></thead>
            <tbody>
              {userList.map((u) => (
                <tr key={u.id} className="border-b border-neutral-800/50">
                  <td className={`${tdCls} text-neutral-100`}>{u.email}</td>
                  <td className={tdCls}>{u.role}</td>
                  <td className={tdCls}>{u.status}</td>
                  <td className={tdCls}>{u.org ? `${u.org.name} (${u.org.role})` : '—'}</td>
                  <td className={`${tdCls} text-xs`}>{fmtTime(u.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {users.loading && !users.data && <Loading />}
        {!users.loading && !users.error && userList.length === 0 && <EmptyState title="No users yet" />}
      </Card>
    </div>
  );
}
