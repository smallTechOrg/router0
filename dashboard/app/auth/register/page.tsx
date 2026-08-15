'use client';
import { useState } from 'react';
import { useRouter } from 'next/navigation';

export default function RegisterPage() {
  const [inviteToken, setInviteToken] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [zer0Key, setZer0Key] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const router = useRouter();

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError('');
    try {
      const res = await fetch('/go/auth/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ invite_token: inviteToken, email, password }),
      });
      const data = await res.json();
      if (!res.ok) {
        setError(data?.error?.message || 'Registration failed');
        return;
      }
      setZer0Key(data.zer0_key);
    } catch {
      setError('Network error');
    } finally {
      setLoading(false);
    }
  }

  if (zer0Key) {
    return (
      <div className="min-h-screen bg-neutral-950 flex items-center justify-center px-4">
        <div className="w-full max-w-lg">
          <div className="rounded-xl border border-green-500/30 bg-green-500/10 p-8 shadow-xl">
            <h2 className="text-xl font-bold text-white mb-4">Registration successful</h2>
            <p className="text-sm text-neutral-300 mb-2">
              Your Zer0 API key (save this — it will never be shown again):
            </p>
            <div className="rounded-lg bg-neutral-900 px-4 py-3 font-mono text-sm text-indigo-300 break-all mb-4 border border-indigo-500/30">
              {zer0Key}
            </div>
            <p className="text-xs text-neutral-500 mb-6">
              Use this key as your API key with base URL{' '}
              <code className="text-neutral-300">http://localhost:8080/v1</code>
            </p>
            <button
              onClick={() => router.push('/auth/login')}
              className="w-full rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-indigo-500 transition-colors"
            >
              Continue to login
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-neutral-950 flex items-center justify-center px-4">
      <div className="w-full max-w-md">
        <div className="mb-8 text-center">
          <span className="text-3xl font-bold tracking-tight text-white">
            Zer0<span className="text-indigo-400"> Router</span>
          </span>
          <p className="mt-2 text-sm text-neutral-400">Register with an invite</p>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-8 shadow-xl">
          <form className="space-y-5" onSubmit={handleSubmit}>
            {error && (
              <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300">
                {error}
              </div>
            )}
            <div>
              <label className="block text-sm font-medium text-neutral-300 mb-1.5">Invite token</label>
              <input
                type="text" value={inviteToken} onChange={e => setInviteToken(e.target.value)} required
                placeholder="Your invite token UUID"
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-neutral-300 mb-1.5">Email</label>
              <input
                type="email" value={email} onChange={e => setEmail(e.target.value)} required
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-neutral-300 mb-1.5">Password (min 12 chars)</label>
              <input
                type="password" value={password} onChange={e => setPassword(e.target.value)} required minLength={12}
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none"
              />
            </div>
            <button
              type="submit" disabled={loading}
              className="w-full rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50 transition-colors"
            >
              {loading ? 'Registering…' : 'Register'}
            </button>
          </form>
          <p className="mt-6 text-center text-sm text-neutral-500">
            Already have an account?{' '}
            <a href="/auth/login" className="text-indigo-400 hover:text-indigo-300 font-medium">Sign in</a>
          </p>
        </div>
      </div>
    </div>
  );
}
