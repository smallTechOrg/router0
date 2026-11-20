import { redirect } from 'next/navigation';

// Auth guard wired in Phase 1.
// For now, always redirect to login until NextAuth session is fully wired.
export default function RootPage() {
  redirect('/auth/login');
}
