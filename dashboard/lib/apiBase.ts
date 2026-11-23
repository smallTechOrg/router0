/**
 * OpenAI-compatible base URL (ending in /v1) to show in samples, derived from the host
 * that served the page so snippets work wherever router0 is deployed:
 *   localhost / 127.0.0.1   -> http://localhost:8080/v1 (local gateway)
 *   router0.<domain>        -> https://api.router0.<domain>/v1
 *   anything else           -> same origin; the dashboard proxies /v1 to the gateway
 */
export function apiBaseFor(host: string, proto: string, override?: string): string {
  const o = override?.trim().replace(/\/+$/, '');
  if (o) return /\/v1$/.test(o) ? o : `${o}/v1`;
  const hostname = host.split(':')[0].toLowerCase();
  if (hostname === 'localhost' || hostname === '127.0.0.1' || hostname.endsWith('.localhost')) {
    return 'http://localhost:8080/v1';
  }
  if (hostname.startsWith('router0.')) return `https://api.${hostname}/v1`;
  return `${proto}://${host}/v1`;
}
