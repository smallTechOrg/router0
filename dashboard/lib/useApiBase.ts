'use client';
import { useEffect, useState } from 'react';
import { apiBaseFor } from './apiBase';

/** Resolves the sample API base from window.location after mount (SSR-safe placeholder first). */
export function useApiBase(): string {
  const [base, setBase] = useState('https://<your-router0-host>/v1');
  useEffect(() => {
    setBase(apiBaseFor(window.location.host, window.location.protocol.replace(':', ''), process.env.NEXT_PUBLIC_ROUTER0_URL));
  }, []);
  return base;
}
