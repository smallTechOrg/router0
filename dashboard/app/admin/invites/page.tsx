export default function InviteManagementPage() {
  return (
    <div className="min-h-screen bg-neutral-950">
      <div className="mx-auto max-w-4xl px-6 py-8">
        <div className="mb-6">
          <h1 className="text-2xl font-bold text-white">Invite Management</h1>
          <p className="mt-1 text-sm text-neutral-400">
            Create and manage user invite tokens.
          </p>
        </div>

        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6">
          <p className="text-sm text-neutral-400 mb-4">
            Invite tokens allow new users to register. Each token is single-use.
          </p>
          <button
            disabled
            className="rounded-lg border border-neutral-700 bg-neutral-800 px-4 py-2 text-sm font-medium text-neutral-300 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Create Invite (available after Phase 1)
          </button>
        </div>
      </div>
    </div>
  );
}
