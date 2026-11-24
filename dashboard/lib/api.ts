// Typed wrappers for every Phase 1 endpoint in spec/api.md.
// All /api, /admin and /auth calls go through Next.js rewrites to the Go service.

export class ApiError extends Error {
  status: number;
  code?: string;
  type?: string;
  param?: string | null;
  router0?: unknown;
  constructor(status: number, message: string, extra: Partial<ApiError> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    Object.assign(this, extra);
  }
}

/** Human-readable text for any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.code ? `${e.message} (${e.code})` : e.message;
  if (e instanceof Error) return e.message;
  return 'Unexpected error';
}

interface ReqOpts {
  method?: string;
  body?: unknown;
  token?: string;
  query?: Record<string, string | number | undefined | null>;
}

async function request<T>(path: string, opts: ReqOpts = {}): Promise<T> {
  let url = path;
  if (opts.query) {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(opts.query)) {
      if (v !== undefined && v !== null && v !== '') qs.set(k, String(v));
    }
    const s = qs.toString();
    if (s) url += (url.includes('?') ? '&' : '?') + s;
  }
  const headers: Record<string, string> = {};
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  if (opts.token) headers['Authorization'] = `Bearer ${opts.token}`;
  let res: Response;
  try {
    res = await fetch(url, {
      method: opts.method || 'GET',
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
      cache: 'no-store',
    });
  } catch {
    throw new ApiError(0, 'Cannot reach the router0 gateway. Is it running?');
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any = undefined;
  if (text) {
    try { data = JSON.parse(text); } catch { /* non-JSON body */ }
  }
  if (!res.ok) {
    // Both shapes carry error.message; /v1 additionally has param + router0 trail.
    const e = data?.error;
    throw new ApiError(res.status, e?.message || res.statusText || `HTTP ${res.status}`, {
      code: e?.code,
      type: e?.type,
      param: e?.param,
      router0: e?.router0,
    });
  }
  return data as T;
}

// ---------- Types ----------

export type OrgRole = 'owner' | 'admin' | 'member';

export interface Me {
  id: string;
  email: string;
  role: string;
  status: string;
  org: { id: string; name: string; role: OrgRole };
}

export interface ServiceToken {
  id: string;
  name: string;
  prefix: string;
  created_at: string;
  created_by: string | null;
  last_used_at: string | null;
  revoked_at: string | null;
  legacy: boolean;
}

export interface CreatedToken {
  id: string;
  name: string;
  token: string;
  prefix: string;
  created_at: string;
}

export interface ProviderInfo { id: string; label: string }

export interface KeyState {
  blocked: boolean;
  reason?: 'rate_limited' | 'insufficient_credits' | 'out_of_credits' | 'budget' | string;
  retry_after_s?: number;
  models?: string[];
}

export interface ProviderKey {
  id: string;
  provider: string;
  label: string;
  key_hint: string;
  position: number;
  status: 'active' | 'error' | 'disabled' | string;
  last_tested_at: string | null;
  last_test_result: string | null;
  state?: KeyState | null;
  created_at: string;
  /** Monthly USD budget; null = no budget. */
  monthly_budget_usd?: number | null;
  /** Estimated spend this UTC month (USD); null if unavailable. */
  month_spend_usd?: number | null;
  balance?: KeyBalance | null;
}

export interface KeyBalance {
  /** Whether router0 can poll this provider's balance (OpenRouter only). */
  supported: boolean;
  status: 'ok' | 'exhausted' | 'error' | 'unsupported' | 'pending' | string;
  remaining_usd: number | null;
  limit_usd: number | null;
  usage_usd: number | null;
  polled_at: string | null;
}

export interface TestKeyResult {
  status: 'ok' | 'error';
  latency_ms?: number;
  model_count?: number;
  class?: string;
  message?: string;
}

export interface PriorityProvider {
  provider: string;
  position: number;
  keys: { id: string; label: string; position: number }[];
}
export interface PriorityResponse { providers: PriorityProvider[] }
export interface PriorityUpdate {
  providers: string[];
  key_order?: Record<string, string[]>;
}

export interface CatalogProviderEntry {
  provider: string;
  provider_model_id: string;
  source: string;
  available: boolean;
  last_seen_at: string;
}
export interface CatalogModel { canonical: string; kind: string; providers: CatalogProviderEntry[] }
export interface CatalogRefresh {
  provider: string;
  last_refreshed_at: string | null;
  last_status: string;
  model_count: number;
}
export interface CatalogResponse {
  models: CatalogModel[];
  refreshes: CatalogRefresh[];
  aliases: { alias: string; canonical_model: string; created_at: string }[];
}

export interface TrailStep {
  order: number;
  provider: string;
  key_label: string;
  outcome: 'success' | 'error' | 'skipped' | string;
  class?: string;
  http_status?: number;
  latency_ms?: number;
  retry_after_s?: number;
}

export interface RequestLog {
  request_id: string;
  created_at: string;
  token_name: string | null;
  canonical_model: string | null;
  provider: string | null;
  provider_model_id: string | null;
  key_label: string | null;
  status: 'success' | 'error' | string;
  http_status: number | null;
  error_code: string | null;
  stream: boolean;
  latency_ms: number | null;
  input_tokens: number | null;
  output_tokens: number | null;
  estimated_cost_usd: number | null;
  attempt_count: number;
  trail: TrailStep[] | null;
}

export interface RequestsQuery {
  limit?: number;
  before?: string;
  status?: 'success' | 'error' | '';
  model?: string;
  token_id?: string;
}
export interface RequestsResponse { requests: RequestLog[]; next_before?: string | null }

/** api.md only says "grouped by org (provider, canonical model, key label)". Shape is parsed defensively. */
export type CostsResponse = Record<string, unknown>;

export interface Invite {
  token: string;
  expires_at: string;
  email?: string;
  org_id?: string | null;
  org_role?: string | null;
}
export interface AdminUser {
  id: string;
  email: string;
  role: string;
  status: string;
  created_at: string;
  org?: { id: string; name: string; role: string } | null;
}
export interface AdminOrg {
  id: string;
  name: string;
  member_count?: number;
  token_count?: number;
  key_count?: number;
  members?: number;
  tokens?: number;
  keys?: number;
  created_at?: string;
}

export interface RegisterResponse {
  user: { id: string; email: string; role: string; status: string; created_at: string };
  org: { id: string; name: string; role: OrgRole };
  access_token: string;
}

// ---------- Auth ----------

export const register = (body: { invite_token: string; email: string; password: string; org_name?: string }) =>
  request<RegisterResponse>('/go/auth/register', { method: 'POST', body });

export const getMe = (token: string) => request<Me>('/api/me', { token });

// ---------- Service tokens ----------

export const listTokens = (token: string) =>
  request<{ tokens: ServiceToken[] }>('/api/tokens', { token });
export const createToken = (token: string, name: string) =>
  request<CreatedToken>('/api/tokens', { method: 'POST', token, body: { name } });
export const revokeToken = (token: string, id: string) =>
  request<void>(`/api/tokens/${encodeURIComponent(id)}`, { method: 'DELETE', token });

// ---------- Provider keys ----------

export const listProviders = (token: string) =>
  request<{ providers: ProviderInfo[] }>('/api/providers', { token });
export const listKeys = (token: string) => request<{ keys: ProviderKey[] }>('/api/keys', { token });
export const addKey = (token: string, body: { provider: string; key: string; label: string; model_id?: string }) =>
  request<ProviderKey>('/api/keys', { method: 'POST', token, body });
export const deleteKey = (token: string, id: string) =>
  request<void>(`/api/keys/${encodeURIComponent(id)}`, { method: 'DELETE', token });
export const testKey = (token: string, id: string) =>
  request<TestKeyResult>(`/api/keys/${encodeURIComponent(id)}/test`, { method: 'POST', token });
export const updateKey = (token: string, id: string, body: { label?: string; monthly_budget_usd?: number | null }) =>
  request<ProviderKey>(`/api/keys/${encodeURIComponent(id)}`, { method: 'PATCH', token, body });
export const resetKeyState = (token: string, id: string) =>
  request<void>(`/api/keys/${encodeURIComponent(id)}/reset-state`, { method: 'POST', token });
export const pollBalances = (token: string) =>
  request<{ polled: number }>('/api/keys/poll-balances', { method: 'POST', token });

// ---------- Routing priority ----------

export const getPriority = (token: string) =>
  request<PriorityResponse>('/api/routing/priority', { token });
export const putPriority = (token: string, body: PriorityUpdate) =>
  request<PriorityResponse>('/api/routing/priority', { method: 'PUT', token, body });

// ---------- Catalog and aliases ----------

export type CatalogKind = 'chat' | 'image' | 'audio' | 'embedding' | 'other';
export interface Alias { alias: string; canonical_model: string; created_at: string }

export const getCatalog = (token: string, opts: { q?: string; kind?: CatalogKind | '' } = {}) =>
  request<CatalogResponse>('/api/catalog', { token, query: { q: opts.q, kind: opts.kind } });
export const refreshCatalog = (token: string, provider?: string) =>
  request<{ refreshes: CatalogRefresh[] }>('/api/catalog/refresh', {
    method: 'POST',
    token,
    body: provider ? { provider } : {},
  });

export const listAliases = (token: string) => request<{ aliases: Alias[] }>('/api/aliases', { token });
export const createAlias = (token: string, body: { alias: string; canonical_model: string }) =>
  request<Alias | undefined>('/api/aliases', { method: 'POST', token, body });
export const deleteAlias = (token: string, alias: string) =>
  request<void>(`/api/aliases/${encodeURIComponent(alias)}`, { method: 'DELETE', token });

/** Display label for a provider id; unknown ids fall back to the id itself. */
const PROVIDER_LABELS: Record<string, string> = {
  groq: 'Groq', openrouter: 'OpenRouter', google: 'Gemini', nvidia: 'NVIDIA', together: 'Together',
  baseten: 'Baseten', anthropic: 'Anthropic', meshapi: 'Mesh API (AI Fiesta)',
};
export const providerLabelFor = (id: string) => PROVIDER_LABELS[id] || id;

/** OpenAI-compatible model list for a service token (r0-sk-...). Not used by the dashboard session. */
export const listV1Models = (serviceToken: string) =>
  request<{ object: 'list'; data: { id: string; router0?: { kind?: string; providers?: { provider: string; provider_model_id: string }[] } }[] }>(
    '/v1/models',
    { token: serviceToken },
  );

// ---------- Request log and costs ----------

export const listRequests = (token: string, q: RequestsQuery = {}) =>
  request<RequestsResponse>('/api/requests', { token, query: { ...q } });
export const getRequest = (token: string, requestId: string) =>
  request<RequestLog>(`/api/requests/${encodeURIComponent(requestId)}`, { token });
export const getCosts = (token: string) => request<CostsResponse>('/api/costs', { token });

// ---------- Admin (platform) ----------

export const createInvite = (token: string, body: { email?: string; org_id?: string; org_role?: 'admin' | 'member' }) =>
  request<Invite>('/admin/invites', { method: 'POST', token, body });
export const listAdminUsers = (token: string) => request<{ users: AdminUser[] }>('/admin/users', { token });
export const listAdminOrgs = (token: string) => request<{ orgs: AdminOrg[] }>('/admin/orgs', { token });
