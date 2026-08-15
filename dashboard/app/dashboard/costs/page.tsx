import StubChart from '@/components/StubChart';

export default function CostTrackerPage() {
  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Cost Tracker</h1>
        <p className="mt-1 text-sm text-neutral-400">
          Estimated spend per provider and model.
        </p>
      </div>

      {/* Spend chart stub */}
      <div className="mb-6">
        <StubChart />
      </div>

      {/* Placeholder table */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="px-6 py-4 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Cost Breakdown</h2>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800">
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Provider</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Model</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Tokens</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Cost USD</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colSpan={4} className="px-6 py-12 text-center text-sm text-neutral-500">
                  No cost data yet — cost tracking available after Phase 1.
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
