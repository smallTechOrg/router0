// Typed API client wrappers to the Go service

const BASE = '/api';

async function apiFetch<T>(path: string, options: RequestInit = {}, token?: string): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string> || {}),
  };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const res = await fetch(path, { ...options, headers });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: { message: res.statusText } }));
    throw new Error(err?.error?.message || res.statusText);
  }
  return res.json();
}

export async function getPoolStatus(token: string) {
  return apiFetch<{ active: any[]; common: any[] }>(`${BASE}/pool`, {}, token);
}

export async function getProviderKeys(token: string) {
  return apiFetch<{ keys: any[] }>(`${BASE}/keys`, {}, token);
}

export async function addProviderKey(token: string, payload: { provider: string; key: string; label?: string; model_id?: string }) {
  return apiFetch<any>(`${BASE}/keys`, { method: 'POST', body: JSON.stringify(payload) }, token);
}

export async function deleteProviderKey(token: string, id: string) {
  const res = await fetch(`${BASE}/keys/${id}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok && res.status !== 204) throw new Error('Failed to delete key');
}

export async function testProviderKey(token: string, id: string) {
  return apiFetch<any>(`${BASE}/keys/${id}/test`, { method: 'POST' }, token);
}

export async function getRequests(token: string) {
  return apiFetch<{ requests: any[] }>(`${BASE}/requests`, {}, token);
}

export async function getCosts(token: string) {
  return apiFetch<any>(`${BASE}/costs`, {}, token);
}
