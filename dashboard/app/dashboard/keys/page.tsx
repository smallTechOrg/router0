export default function ProviderKeysPage() {
  return (
    <div>
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-white">Provider Keys</h1>
        <p className="mt-1 text-sm text-neutral-400">
          Manage your encrypted provider API keys.
        </p>
      </div>

      {/* Phase-1 notice card */}
      <div className="mb-6 rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3">
        <p className="text-sm text-amber-300">
          Key management available after Phase 1 is deployed.
        </p>
      </div>

      {/* Placeholder table */}
      <div className="rounded-xl border border-neutral-800 bg-neutral-900 overflow-hidden">
        <div className="flex items-center justify-between px-6 py-4 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Provider Keys</h2>
          <button
            disabled
            className="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white disabled:cursor-not-allowed disabled:opacity-40"
          >
            Add Key (Phase 1)
          </button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-800">
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Provider</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Label</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Status</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Last Tested</th>
                <th className="px-6 py-3 text-left text-xs font-medium text-neutral-400 uppercase tracking-wider">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td colSpan={5} className="px-6 py-12 text-center text-sm text-neutral-500">
                  No keys yet — key management available after Phase 1.
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
