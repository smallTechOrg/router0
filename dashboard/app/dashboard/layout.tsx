import { getServerSession } from 'next-auth';
import { redirect } from 'next/navigation';
import { authOptions } from '@/lib/auth';
import Link from 'next/link';

const navItems = [
  { label: 'Pool Status', href: '/dashboard' },
  { label: 'Provider Keys', href: '/dashboard/keys' },
  { label: 'Requests', href: '/dashboard/requests' },
  { label: 'Costs', href: '/dashboard/costs' },
];

export default async function DashboardLayout({ children }: { children: React.ReactNode }) {
  const session = await getServerSession(authOptions);
  if (!session) redirect('/auth/login');

  return (
    <div className="flex h-full min-h-screen">
      <aside className="w-60 shrink-0 border-r border-neutral-800 bg-neutral-900 flex flex-col">
        <div className="flex items-center gap-2 px-6 py-5 border-b border-neutral-800">
          <span className="text-lg font-bold tracking-tight text-white">
            Zer0<span className="text-indigo-400"> Router</span>
          </span>
        </div>
        <nav className="flex-1 px-3 py-4 space-y-0.5">
          {navItems.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className="flex items-center rounded-lg px-3 py-2 text-sm font-medium text-neutral-300 hover:bg-neutral-800 hover:text-white transition-colors"
            >
              {item.label}
            </Link>
          ))}
          {(session as any)?.user?.role === 'admin' && (
            <Link
              href="/admin/invites"
              className="flex items-center rounded-lg px-3 py-2 text-sm font-medium text-amber-400 hover:bg-neutral-800 transition-colors"
            >
              Admin: Invites
            </Link>
          )}
        </nav>
        <div className="px-6 py-4 border-t border-neutral-800">
          <p className="text-xs text-neutral-500 truncate">{session.user?.email}</p>
          <a href="/api/auth/signout" className="mt-1 text-xs text-neutral-600 hover:text-neutral-400">Sign out</a>
        </div>
      </aside>
      <main className="flex-1 overflow-auto bg-neutral-950">
        <div className="mx-auto max-w-7xl px-6 py-8">{children}</div>
      </main>
    </div>
  );
}
