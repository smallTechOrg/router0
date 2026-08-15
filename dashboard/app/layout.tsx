import type { Metadata } from 'next';
import './globals.css';

export const metadata: Metadata = {
  title: 'Zer0 Router Dashboard',
  description: 'Manage your AI provider pool and routing configuration.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className="h-full">
      <body className="h-full bg-neutral-950 text-neutral-100 font-sans antialiased">
        {children}
      </body>
    </html>
  );
}
