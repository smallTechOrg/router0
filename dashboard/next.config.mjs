/** @type {import('next').NextConfig} */
const nextConfig = {
  async rewrites() {
    const go = process.env.ROUTER_BASE_URL || 'http://localhost:8080';
    return [
      // Explicit Go API paths. Avoids capturing /api/auth/*, which is handled
      // locally by the NextAuth route handler.
      { source: '/api/tokens/:path*', destination: `${go}/api/tokens/:path*` },
      { source: '/api/providers', destination: `${go}/api/providers` },
      { source: '/api/keys/:path*', destination: `${go}/api/keys/:path*` },
      { source: '/api/routing/:path*', destination: `${go}/api/routing/:path*` },
      { source: '/api/catalog/:path*', destination: `${go}/api/catalog/:path*` },
      { source: '/api/requests/:path*', destination: `${go}/api/requests/:path*` },
      { source: '/api/costs', destination: `${go}/api/costs` },
      { source: '/api/me', destination: `${go}/auth/me` },
      { source: '/admin/:path*', destination: `${go}/admin/:path*` },
      { source: '/v1/:path*', destination: `${go}/v1/:path*` },
      // Go auth endpoints are prefixed /go/auth/* to avoid the Next.js /auth/* pages.
      { source: '/go/auth/:path*', destination: `${go}/auth/:path*` },
    ];
  },
};

export default nextConfig;
