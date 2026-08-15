/** @type {import('next').NextConfig} */
const nextConfig = {
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: 'http://localhost:8080/api/:path*',
      },
      {
        source: '/admin/:path*',
        destination: 'http://localhost:8080/admin/:path*',
      },
      {
        source: '/v1/:path*',
        destination: 'http://localhost:8080/v1/:path*',
      },
    ];
  },
};

export default nextConfig;
