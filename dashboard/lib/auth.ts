import type { NextAuthOptions } from 'next-auth';
import CredentialsProvider from 'next-auth/providers/credentials';

const ROUTER_BASE = process.env.ROUTER_BASE_URL || 'http://localhost:8080';

async function refreshAccessToken(refreshToken: string) {
  try {
    const res = await fetch(`${ROUTER_BASE}/auth/refresh`, {
      method: 'POST',
      headers: { Cookie: `refresh_token=${refreshToken}` },
    });
    if (!res.ok) return null;
    const data = await res.json();
    return data.access_token as string;
  } catch {
    return null;
  }
}

export const authOptions: NextAuthOptions = {
  providers: [
    CredentialsProvider({
      name: 'Credentials',
      credentials: {
        email: { label: 'Email', type: 'email' },
        password: { label: 'Password', type: 'password' },
      },
      async authorize(credentials) {
        if (!credentials?.email || !credentials?.password) return null;
        try {
          const res = await fetch(`${ROUTER_BASE}/auth/login`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ email: credentials.email, password: credentials.password }),
          });
          if (!res.ok) return null;
          const data = await res.json();
          return {
            id: data.user.id,
            email: data.user.email,
            name: data.user.email,
            role: data.user.role,
            accessToken: data.access_token,
            refreshToken: data.refresh_token,
            // store expiry so jwt callback knows when to refresh (23h to refresh 1h before actual expiry)
            accessTokenExpires: Date.now() + 23 * 60 * 60 * 1000,
          };
        } catch {
          return null;
        }
      },
    }),
  ],
  pages: { signIn: '/auth/login' },
  session: { strategy: 'jwt' },
  callbacks: {
    async jwt({ token, user }: any) {
      // Initial sign-in: persist tokens from authorize()
      if (user) {
        token.accessToken = user.accessToken;
        token.refreshToken = user.refreshToken;
        token.accessTokenExpires = user.accessTokenExpires;
        token.role = user.role;
        token.userId = user.id;
        return token;
      }

      // Token still valid — return as-is
      if (Date.now() < token.accessTokenExpires) {
        return token;
      }

      // Access token expired — try to refresh
      if (token.refreshToken) {
        const newAccessToken = await refreshAccessToken(token.refreshToken);
        if (newAccessToken) {
          token.accessToken = newAccessToken;
          token.accessTokenExpires = Date.now() + 23 * 60 * 60 * 1000;
          return token;
        }
      }

      // Refresh failed — mark as expired so session returns null and middleware redirects to login
      token.error = 'RefreshAccessTokenError';
      return token;
    },
    session({ session, token }: any) {
      session.accessToken = token.accessToken;
      session.user.role = token.role;
      session.user.id = token.userId;
      session.error = token.error;
      return session;
    },
  },
};
