// Auth guard wired in Phase 1

const navItems = [
  { label: 'Pool Status', href: '/dashboard' },
  { label: 'Provider Keys', href: '/dashboard/keys' },
  { label: 'Requests', href: '/dashboard/requests' },
  { label: 'Costs', href: '/dashboard/costs' },
];

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="flex h-full min-h-screen">
      {/* Sidebar */}
      <aside className="w-60 shrink-0 border-r border-neutral-800 bg-neutral-900 flex flex-col">
        {/* Brand */}
        <div className="flex items-center gap-2 px-6 py-5 border-b border-neutral-800">
          <span className="text-lg font-bold tracking-tight text-white">
            Zer0<span className="text-indigo-400"> Router</span>
          </span>
        </div>

        {/* Nav */}
        <nav className="flex-1 px-3 py-4 space-y-0.5">
          {navItems.map((item) => (
            <a
              key={item.href}
              href={item.href}
              className="flex items-center rounded-lg px-3 py-2 text-sm font-medium text-neutral-300 hover:bg-neutral-800 hover:text-white transition-colors"
            >
              {item.label}
            </a>
          ))}
        </nav>

        {/* Footer */}
        <div className="px-6 py-4 border-t border-neutral-800">
          <p className="text-xs text-neutral-600">Scaffold — Phase 1 pending</p>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto bg-neutral-950">
        <div className="mx-auto max-w-7xl px-6 py-8">
          {children}
        </div>
      </main>
    </div>
  );
}
