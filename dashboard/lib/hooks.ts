'use client';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useSession } from 'next-auth/react';
import { useRouter } from 'next/navigation';
import { ApiError, errorMessage, getMe, type Me } from './api';

/** Session access token plus the caller's org identity. */
export function useAuth() {
  const { data: session, status } = useSession() as any;
  const router = useRouter();
  const token: string | undefined = session?.accessToken;
  const [me, setMe] = useState<Me | null>(null);
  const [meError, setMeError] = useState('');

  useEffect(() => {
    if (status === 'unauthenticated' || session?.error) router.replace('/auth/login');
  }, [status, session, router]);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    getMe(token)
      .then((m) => { if (!cancelled) setMe(m); })
      .catch((e) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 401) router.replace('/auth/login');
        else setMeError(errorMessage(e));
      });
    return () => { cancelled = true; };
  }, [token, router]);

  const canWrite = me?.org?.role === 'owner' || me?.org?.role === 'admin';
  return {
    token,
    ready: status !== 'loading' && !!token,
    me,
    meError,
    canWrite,
    isPlatformAdmin: session?.user?.role === 'admin',
    sessionLoading: status === 'loading',
  };
}

/** Fetches data with loading/error state, optional polling, and 401 redirect. */
export function useLoader<T>(
  fn: ((token: string) => Promise<T>) | null,
  token: string | undefined,
  deps: unknown[] = [],
  intervalMs = 0,
) {
  const router = useRouter();
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  const load = useCallback(async (silent = false) => {
    const f = fnRef.current;
    if (!token || !f) return;
    if (!silent) setLoading(true);
    try {
      setData(await f(token));
      setError('');
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) router.replace('/auth/login');
      else setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [token, router]);

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { load(); }, [load, ...deps]);

  useEffect(() => {
    if (!intervalMs || !token) return;
    const id = setInterval(() => load(true), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs, token, load]);

  return { data, setData, error, loading, reload: load };
}
