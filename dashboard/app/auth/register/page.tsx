'use client';
import { useState } from 'react';
import { useRouter } from 'next/navigation';
import { signIn } from 'next-auth/react';
import { errorMessage, register } from '@/lib/api';
import { btnPrimary, ErrorBanner, inputCls } from '@/components/ui';

export default function RegisterPage() {
  const [inviteToken, setInviteToken] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [orgName, setOrgName] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const router = useRouter();

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError('');
    try {
      await register({
        invite_token: inviteToken.trim(),
        email,
        password,
        ...(orgName.trim() ? { org_name: orgName.trim() } : {}),
      });
      // Registration succeeded: start a dashboard session, then create the first token.
      const result = await signIn('credentials', { email, password, redirect: false });
      if (result?.ok) router.push('/dashboard/tokens');
      else router.push('/auth/login');
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  const label = 'mb-1.5 block text-sm font-medium text-neutral-300';
  return (
    <div className="flex min-h-screen items-center justify-center bg-neutral-950 px-4">
      <div className="w-full max-w-md">
        <div className="mb-8 text-center">
          <span className="text-3xl font-bold tracking-tight text-white">
            router<span className="text-indigo-400">0</span>
          </span>
          <p className="mt-2 text-sm text-neutral-400">Register with an invite</p>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-8 shadow-xl">
          <form className="space-y-5" onSubmit={handleSubmit}>
            <ErrorBanner message={error} />
            <div>
              <label htmlFor="invite" className={label}>Invite token</label>
              <input id="invite" type="text" value={inviteToken} onChange={(e) => setInviteToken(e.target.value)} required placeholder="Your invite token" className={inputCls} />
            </div>
            <div>
              <label htmlFor="org" className={label}>Organization name <span className="text-neutral-500">(optional)</span></label>
              <input id="org" type="text" value={orgName} onChange={(e) => setOrgName(e.target.value)} placeholder="Acme" className={inputCls} />
              <p className="mt-1 text-xs text-neutral-500">Used when your invite creates a new organization. Defaults to your email.</p>
            </div>
            <div>
              <label htmlFor="email" className={label}>Email</label>
              <input id="email" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required className={inputCls} />
            </div>
            <div>
              <label htmlFor="password" className={label}>Password (min 12 chars)</label>
              <input id="password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={12} className={inputCls} />
            </div>
            <button type="submit" disabled={loading} className={`${btnPrimary} w-full`}>
              {loading ? 'Registering…' : 'Register'}
            </button>
          </form>
          <p className="mt-6 text-center text-sm text-neutral-500">
            Already have an account?{' '}
            <a href="/auth/login" className="font-medium text-indigo-400 hover:text-indigo-300">Sign in</a>
          </p>
        </div>
      </div>
    </div>
  );
}
