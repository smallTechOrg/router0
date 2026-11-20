import { getServerSession } from 'next-auth';
import { redirect } from 'next/navigation';
import { authOptions } from '@/lib/auth';
import Link from 'next/link';

const navItems = [
  { label: 'Overview', href: '/dashboard' },
  { label: 'Tokens', href: '/dashboard/tokens' },
  { label: 'Provider Keys', href: '/dashboard/keys' },
  { label: 'Routing', href: '/dashboard/routing' },
  { label: 'Requests', href: '/dashboard/requests' },
  { label: 'Costs', href: '/dashboard/costs' },
];

const linkCls =
  'flex items-center whitespace-nowrap rounded-lg px-3 py-2 text-sm font-medium transition-colors hover:bg-neutral-800 focus:outline-none focus:ring-2 focus:ring-indigo-400';

export default async function DashboardLayout({ children }: { children: React.ReactNode }) {
  const session = await getServerSession(authOptions);
  if (!session) redirect('/auth/login');

  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="shrink-0 border-b border-neutral-800 bg-neutral-900 md:flex md:w-60 md:flex-col md:border-b-0 md:border-r">
        <div className="flex items-center justify-between gap-2 px-6 py-4 md:py-5 md:border-b md:border-neutral-800">
          <Link href="/dashboard" className="text-lg font-bold tracking-tight text-white">
            router<span className="text-indigo-400">0</span>
          </Link>
          <div className="text-right md:hidden">
            <p className="max-w-[10rem] truncate text-xs text-neutral-500">{session.user?.email}</p>
            <a href="/api/auth/signout" className="text-xs text-neutral-500 hover:text-neutral-300">Sign out</a>
          </div>
        </div>
        <nav aria-label="Main" className="flex gap-1 overflow-x-auto px-3 pb-3 md:flex-1 md:flex-col md:space-y-0.5 md:overflow-visible md:py-4">
          {navItems.map((item) => (
            <Link key={item.href} href={item.href} className={`${linkCls} text-neutral-300 hover:text-white`}>
              {item.label}
            </Link>
          ))}
          {(session as any)?.user?.role === 'admin' && (
            <Link href="/dashboard/admin" className={`${linkCls} text-amber-400`}>
              Admin
            </Link>
          )}
        </nav>
        <div className="hidden border-t border-neutral-800 px-6 py-4 md:block">
          <p className="truncate text-xs text-neutral-500">{session.user?.email}</p>
          <a href="/api/auth/signout" className="mt-1 inline-block text-xs text-neutral-500 hover:text-neutral-300">Sign out</a>
        </div>
      </aside>
      <main className="min-w-0 flex-1 bg-neutral-950">
        <div className="mx-auto max-w-7xl px-4 py-6 md:px-6 md:py-8">{children}</div>
      </main>
    </div>
  );
}
