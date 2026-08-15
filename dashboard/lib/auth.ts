import type { NextAuthOptions } from 'next-auth';
import CredentialsProvider from 'next-auth/providers/credentials';

const ROUTER_BASE = process.env.ROUTER_BASE_URL || 'http://localhost:8080';

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
    jwt({ token, user }: any) {
      if (user) {
        token.accessToken = user.accessToken;
        token.role = user.role;
        token.userId = user.id;
      }
      return token;
    },
    session({ session, token }: any) {
      session.accessToken = token.accessToken;
      session.user.role = token.role;
      session.user.id = token.userId;
      return session;
    },
  },
};
