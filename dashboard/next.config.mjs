/** @type {import('next').NextConfig} */
const nextConfig = {
  async rewrites() {
    return [
      // Explicit Go API paths — avoids accidentally capturing /api/auth/* which
      // must be handled locally by the NextAuth route handler.
      { source: '/api/keys/:path*', destination: 'http://localhost:8080/api/keys/:path*' },
      { source: '/api/pool', destination: 'http://localhost:8080/api/pool' },
      { source: '/api/requests', destination: 'http://localhost:8080/api/requests' },
      { source: '/api/costs', destination: 'http://localhost:8080/api/costs' },
      { source: '/admin/:path*', destination: 'http://localhost:8080/admin/:path*' },
      { source: '/v1/:path*', destination: 'http://localhost:8080/v1/:path*' },
      // Go auth endpoints — prefixed /go/auth/* to avoid conflict with Next.js /auth/* pages
      { source: '/go/auth/:path*', destination: 'http://localhost:8080/auth/:path*' },
    ];
  },
};

export default nextConfig;
