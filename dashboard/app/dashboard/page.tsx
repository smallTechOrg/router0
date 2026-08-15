export default function PoolStatusPage() {
  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Pool Status</h1>
        <p className="mt-1 text-sm text-neutral-400">
          Active and common provider pool state.
        </p>
      </div>

      {/* Phase-1 notice card */}
      <div className="mb-6 rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3">
        <p className="text-sm text-amber-300">
          Provider pool data loading after Phase 1 is deployed.
        </p>
      </div>

      {/* Placeholder table */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="px-6 py-4 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Provider Pool</h2>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800">
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Provider</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Model</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Score</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Latency p95</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Error Rate</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Pool</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Last Checked</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colSpan={7} className="px-6 py-12 text-center text-sm text-neutral-500">
                  No data yet — pool data available after Phase 1.
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
