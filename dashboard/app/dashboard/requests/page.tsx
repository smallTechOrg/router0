import React from 'react';
import { getServerSession } from 'next-auth';
import { authOptions } from '@/lib/auth';

async function getRequests(token: string) {
  try {
    const res = await fetch('http://localhost:8080/api/requests', {
      headers: { Authorization: `Bearer ${token}` },
      cache: 'no-store',
    });
    if (!res.ok) return [];
    const data = await res.json();
    return data.requests || [];
  } catch { return []; }
}

export default async function RequestsPage() {
  const session = await getServerSession(authOptions) as any;
  const requests = await getRequests(session?.accessToken || '');

  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Request Feed</h1>
        <p className="mt-1 text-sm text-neutral-400">Last 50 inference requests (no prompt content stored).</p>
      </div>
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800">
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Time</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Provider</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Model</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Pool</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Tokens</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Latency</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Cost</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-neutral-400 uppercase">Status</th>
              </tr>
            </thead>
            <tbody>
              {requests.length === 0 ? (
                <tr><td colSpan={8} className="px-6 py-12 text-center text-sm text-neutral-500">No requests yet.</td></tr>
              ) : requests.map((req: any) => {
                const isError = req.status !== 'success';
                return (
                  <React.Fragment key={req.id}>
                    <tr className={`border-b ${isError ? 'border-red-900/30 bg-red-950/10' : 'border-neutral-800/50'} hover:bg-neutral-800/30 transition-colors`}>
                      <td className="px-4 py-3 text-xs text-neutral-500">{new Date(req.created_at).toLocaleTimeString()}</td>
                      <td className="px-4 py-3 text-neutral-300 capitalize">{req.provider === 'none' ? <span className="text-red-400">—</span> : req.provider}</td>
                      <td className="px-4 py-3 font-mono text-xs text-neutral-400 max-w-[200px] truncate">{req.model}</td>
                      <td className="px-4 py-3">
                        <span className={`text-xs px-1.5 py-0.5 rounded-full ${
                          req.pool === 'pro' ? 'bg-amber-900/40 text-amber-400' : req.pool === 'flash' ? 'bg-indigo-900/40 text-indigo-400' : 'bg-neutral-800 text-neutral-400'
                        }`}>
                          {req.pool}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-xs text-neutral-400">{req.input_tokens} / {req.output_tokens}</td>
                      <td className="px-4 py-3 text-xs text-neutral-400">{req.latency_ms}ms</td>
                      <td className="px-4 py-3 text-xs text-neutral-400">${(req.estimated_cost_usd || 0).toFixed(6)}</td>
                      <td className="px-4 py-3">
                        <span className={`text-xs px-1.5 py-0.5 rounded-full font-medium ${
                          req.status === 'success' ? 'bg-green-900/40 text-green-400' : 'bg-red-900/40 text-red-400'
                        }`}>
                          {req.status === 'no_providers' ? '503 no providers' : req.status === 'all_failed' ? '503 all failed' : req.status}
                          {req.retry_count > 0 && ` (${req.retry_count} retries)`}
                        </span>
                      </td>
                    </tr>
                    {isError && req.error_message && (
                      <tr className="border-b border-red-900/20 bg-red-950/5">
                        <td colSpan={8} className="px-6 py-2">
                          <div className="text-xs text-red-400/80 font-mono leading-relaxed">
                            {req.error_message.split('; ').map((e: string, i: number) => (
                              <div key={i} className="flex items-start gap-2">
                                <span className="text-red-600 select-none">×</span>
                                <span>{e}</span>
                              </div>
                            ))}
                          </div>
                        </td>
                      </tr>
                    )}
                  </React.Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
