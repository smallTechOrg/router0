import type { Metadata } from 'next';
import './globals.css';
import { SessionProviderWrapper } from '@/components/SessionProviderWrapper';

export const metadata: Metadata = {
  title: 'router0 dashboard',
  description: 'Org-scoped LLM gateway: service tokens, provider keys, routing and request log.',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className="h-full">
      <body className="h-full bg-neutral-950 text-neutral-100 font-sans antialiased">
        <SessionProviderWrapper>{children}</SessionProviderWrapper>
      </body>
    </html>
  );
}
