export default function RegisterPage() {
  return (
    <div className="min-h-screen bg-neutral-950 flex items-center justify-center px-4">
      <div className="w-full max-w-md">
        {/* Logo / brand */}
        <div className="mb-8 text-center">
          <span className="text-3xl font-bold tracking-tight text-white">
            Zer0<span className="text-indigo-400"> Router</span>
          </span>
          <p className="mt-2 text-sm text-neutral-400">
            Create your account
          </p>
        </div>

        {/* Phase-1 notice */}
        <div className="mb-6 rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3">
          <p className="text-sm text-amber-300 leading-relaxed">
            <span className="font-semibold">Backend connection coming in Phase 1.</span>{' '}
            This form will be active after Phase 1 is deployed.
          </p>
        </div>

        {/* Register form */}
        <div className="rounded-xl border border-neutral-800 bg-neutral-900 p-8 shadow-xl">
          <form className="space-y-5">
            <div>
              <label
                htmlFor="email"
                className="block text-sm font-medium text-neutral-300 mb-1.5"
              >
                Email address
              </label>
              <input
                id="email"
                name="email"
                type="email"
                autoComplete="email"
                placeholder="you@example.com"
                disabled
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>

            <div>
              <label
                htmlFor="password"
                className="block text-sm font-medium text-neutral-300 mb-1.5"
              >
                Password
              </label>
              <input
                id="password"
                name="password"
                type="password"
                autoComplete="new-password"
                placeholder="••••••••"
                disabled
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>

            <div>
              <label
                htmlFor="invite"
                className="block text-sm font-medium text-neutral-300 mb-1.5"
              >
                Invite token
              </label>
              <input
                id="invite"
                name="invite"
                type="text"
                autoComplete="off"
                placeholder="zer0-inv-xxxxxxxx"
                disabled
                className="w-full rounded-lg border border-neutral-700 bg-neutral-800 px-3.5 py-2.5 text-sm text-neutral-100 placeholder-neutral-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 disabled:cursor-not-allowed disabled:opacity-50"
              />
              <p className="mt-1.5 text-xs text-neutral-500">
                You need a valid invite token to register.
              </p>
            </div>

            <button
              type="submit"
              disabled
              className="w-full rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-indigo-500 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-600 disabled:cursor-not-allowed disabled:opacity-40 transition-colors"
            >
              Create account
            </button>
          </form>

          <p className="mt-6 text-center text-sm text-neutral-500">
            Already have an account?{' '}
            <a
              href="/auth/login"
              className="text-indigo-400 hover:text-indigo-300 font-medium transition-colors"
            >
              Sign in
            </a>
          </p>
        </div>
      </div>
    </div>
  );
}
