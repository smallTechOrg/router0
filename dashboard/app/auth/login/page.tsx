'use client';
import { signIn } from 'next-auth/react';
import { useState } from 'react';
import { useRouter } from 'next/navigation';

export default function LoginPage() {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const router = useRouter();

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError('');
    const result = await signIn('credentials', { email, password, redirect: false });
    setLoading(false);
    if (result?.ok) {
      router.push('/dashboard');
    } else {
      setError('Invalid email or password');
    }
  }

  return (
    <div className="min-h-screen bg-neutral-950 flex items-center justify-center px-4">
      <div className="w-full max-w-md">
        <div className="mb-8 text-center">
          <span className="text-3xl font-bold tracking-tight text-white">
            router<span className="text-indigo-400">0</span>
          </span>
          <p className="mt-2 text-sm text-neutral-400">Sign in to your dashboard</p>
        </div>
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-8 shadow-xl">
          <form className="space-y-5" onSubmit={handleSubmit}>
            {error && (
              <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-300">
                {error}
              </div>
            )}
            <div>
              <label htmlFor="email" className="block text-sm font-medium text-neutral-300 mb-1.5">Email address</label>
              <input
                id="email" type="email" autoComplete="email" value={email}
                onChange={e => setEmail(e.target.value)} required
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              />
            </div>
            <div>
              <label htmlFor="password" className="block text-sm font-medium text-neutral-300 mb-1.5">Password</label>
              <input
                id="password" type="password" autoComplete="current-password" value={password}
                onChange={e => setPassword(e.target.value)} required
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              />
            </div>
            <button
              type="submit" disabled={loading}
              className="w-full rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50 transition-colors"
            >
              {loading ? 'Signing in…' : 'Sign in'}
            </button>
          </form>
          <p className="mt-6 text-center text-sm text-neutral-500">
            Need an account?{' '}
            <a href="/auth/register" className="text-indigo-400 hover:text-indigo-300 font-medium">
              Register with an invite
            </a>
          </p>
        </div>
      </div>
    </div>
  );
}
