import type { NextAuthOptions } from 'next-auth';
import CredentialsProvider from 'next-auth/providers/credentials';

export const authOptions: NextAuthOptions = {
  providers: [
    CredentialsProvider({
      name: 'Credentials',
      credentials: {
        email: { label: 'Email', type: 'email' },
        password: { label: 'Password', type: 'password' },
      },
      async authorize(credentials) {
        // TODO Phase 1: call Go service POST /auth/login
        return null;
      },
    }),
  ],
  pages: {
    signIn: '/auth/login',
  },
  session: { strategy: 'jwt' },
};
