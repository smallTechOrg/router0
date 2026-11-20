export default function StubChart() {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-6">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-sm font-semibold text-neutral-200">Spend over time</h3>
        <span className="rounded-full bg-neutral-800 px-2.5 py-0.5 text-xs text-neutral-400 ring-1 ring-inset ring-neutral-700">
          Phase 2
        </span>
      </div>

      {/* Fake chart skeleton */}
      <div className="h-32 flex items-end gap-1.5 px-2">
        {[40, 65, 30, 80, 55, 90, 45, 70, 35, 60, 85, 50].map((h, i) => (
          <div
            key={i}
            className="flex-1 rounded-t bg-neutral-800"
            style={{ height: `${h}%` }}
          />
        ))}
      </div>

      <p className="mt-4 text-center text-sm text-neutral-500">
        Analytics coming soon — Phase 2
      </p>
    </div>
  );
}
