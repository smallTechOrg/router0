import type { Metadata } from 'next';
import Link from 'next/link';
import { headers } from 'next/headers';
import { apiBaseFor } from '@/lib/apiBase';

const SITE = 'https://router0.smalltech.in';

export const metadata: Metadata = {
  title: 'router0 — One OpenAI-compatible endpoint. Your keys. Automatic failover.',
  description:
    'router0 is a managed LLM gateway. Bring your own provider keys, call any model by name, and let router0 pick the provider, fail over on errors, and tell you exactly why a request failed.',
  alternates: { canonical: SITE },
  openGraph: {
    title: 'router0 — a managed LLM gateway',
    description: 'One OpenAI-compatible endpoint across Anthropic, OpenRouter, Groq, Gemini and Mesh API. Your keys, your routing order, automatic failover.',
    url: SITE,
    siteName: 'router0',
    type: 'website',
  },
};

const btnPrimary =
  'inline-flex items-center justify-center rounded-lg bg-indigo-600 px-5 py-3 text-sm font-semibold text-white transition-colors hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-300 focus:ring-offset-2 focus:ring-offset-neutral-950';
const btnSecondary =
  'inline-flex items-center justify-center rounded-lg border border-neutral-700 px-5 py-3 text-sm font-semibold text-neutral-100 transition-colors hover:border-neutral-500 hover:bg-neutral-900 focus:outline-none focus:ring-2 focus:ring-indigo-300 focus:ring-offset-2 focus:ring-offset-neutral-950';

const providers = ['Anthropic', 'OpenRouter', 'Groq', 'Google Gemini', 'Mesh API'];

const problems = [
  {
    title: 'A key per provider, in every service',
    body: 'Each backend carries its own set of provider keys, and rotating one means touching all of them.',
  },
  {
    title: 'A provider hiccup becomes your outage',
    body: 'Rate limits, empty credits and flaky endpoints each need their own retry logic, copied from app to app.',
  },
  {
    title: 'Errors that say nothing',
    body: 'When a call fails you have to guess whether the model is missing, the key is out of credit, or you are throttled.',
  },
];

const features = [
  {
    title: 'Call a model by name',
    body: 'Send a canonical model name or one of your own aliases. router0 works out which provider can serve it, so callers never hard-code a vendor.',
  },
  {
    title: 'Routing you control',
    body: 'Set a fixed provider order per organization and a key order within each provider. Override it for a single request with the X-Router0-Providers header.',
  },
  {
    title: 'Failover on any error',
    body: 'If a provider or key fails, router0 moves to the next one. Streaming fails over too, right up until the first byte is sent to your client.',
  },
  {
    title: 'Errors you can act on',
    body: '404 model_not_found, 402 insufficient_credits and 429 rate_limited are separate, with a Retry-After header when you are throttled.',
  },
  {
    title: 'Bring your own keys',
    body: 'Your provider keys stay yours and are stored encrypted. Spend lands on your own provider accounts, not ours.',
  },
  {
    title: 'Teams and service tokens',
    body: 'Organizations hold many named, revocable service tokens. Give each backend its own and cut one off without touching the rest.',
  },
  {
    title: 'See every request',
    body: 'A request log shows the model, status, latency and the full failover trail: which providers were tried, in what order, and what each said.',
  },
  {
    title: 'Live model catalog',
    body: 'Models are discovered from your providers and kept fresh. Browse them in the dashboard, search by kind, and define tenant-level aliases.',
  },
];

const steps = [
  { n: '1', title: 'Add your provider keys', body: 'Paste keys for the providers you already use. router0 checks them and lists the models each one offers.' },
  { n: '2', title: 'Set your routing order', body: 'Choose which provider is tried first and how your keys are ordered within it.' },
  { n: '3', title: 'Point your app at router0', body: 'Create a service token and swap the base URL. Your existing OpenAI-compatible client keeps working.' },
];

const curlSample = (apiBase: string) => `curl ${apiBase}/chat/completions \\
  -H "Authorization: Bearer $ROUTER0_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "llama-3.3-70b",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'`;

const pythonSample = (apiBase: string) => `from openai import OpenAI

client = OpenAI(
    base_url="${apiBase}",
    api_key="r0-sk-...",  # your router0 service token
)

resp = client.chat.completions.create(
    model="llama-3.3-70b",
    messages=[{"role": "user", "content": "Hello!"}],
)`;

function Logo({ className = 'text-xl' }: { className?: string }) {
  return (
    <span className={`font-bold tracking-tight text-white ${className}`}>
      router<span className="text-indigo-400">0</span>
    </span>
  );
}

export default function LandingPage() {
  // Reading request headers makes this page dynamic, so the sample matches the deployment.
  const h = headers();
  const host = h.get('x-forwarded-host') ?? h.get('host') ?? 'localhost';
  const proto = h.get('x-forwarded-proto')?.split(',')[0] ?? 'https';
  const apiBase = apiBaseFor(host, proto, process.env.PUBLIC_API_BASE_URL);
  const curl = curlSample(apiBase);
  const python = pythonSample(apiBase);

  const jsonLd = {
    '@context': 'https://schema.org',
    '@type': 'SoftwareApplication',
    name: 'router0',
    applicationCategory: 'DeveloperApplication',
    description: 'Managed multi-tenant LLM gateway with an OpenAI-compatible API, bring-your-own provider keys and automatic failover.',
    url: SITE,
  };

  return (
    <div className="min-h-screen bg-neutral-950 text-neutral-100">
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(jsonLd) }} />

      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-indigo-600 focus:px-3 focus:py-2 focus:text-sm focus:text-white"
      >
        Skip to content
      </a>

      <header className="sticky top-0 z-40 border-b border-neutral-900 bg-neutral-950/85 backdrop-blur">
        <div className="mx-auto flex max-w-6xl items-center justify-between px-4 py-3.5 sm:px-6">
          <Link href="/" aria-label="router0 home"><Logo /></Link>
          <nav aria-label="Primary" className="flex items-center gap-1 sm:gap-2">
            <a href="#features" className="hidden rounded-md px-3 py-2 text-sm text-neutral-300 hover:text-white sm:inline">Features</a>
            <a href="#how" className="hidden rounded-md px-3 py-2 text-sm text-neutral-300 hover:text-white sm:inline">How it works</a>
            <Link href="/auth/login" className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500">Sign in</Link>
          </nav>
        </div>
      </header>

      <main id="main">
        {/* Hero */}
        <section className="relative overflow-hidden">
          <div aria-hidden className="pointer-events-none absolute inset-x-0 top-0 h-[28rem] bg-[radial-gradient(60%_60%_at_50%_0%,rgba(99,102,241,0.22),transparent)]" />
          <div className="relative mx-auto max-w-6xl px-4 pb-16 pt-16 sm:px-6 sm:pb-24 sm:pt-24">
            <p className="mb-5 inline-flex rounded-full border border-neutral-800 bg-neutral-900 px-3 py-1 text-xs font-medium text-neutral-300">
              A managed LLM gateway for your backend services
            </p>
            <h1 className="max-w-3xl text-4xl font-bold leading-[1.1] tracking-tight text-white sm:text-6xl">
              One endpoint for every model.{' '}
              <span className="text-indigo-400">Your keys. No outages.</span>
            </h1>
            <p className="mt-6 max-w-2xl text-lg leading-relaxed text-neutral-300">
              router0 sits between your services and your LLM providers. Send a model name to a single OpenAI-compatible API.
              router0 picks the provider, fails over when something breaks, and tells you plainly why when nothing can serve the request.
            </p>
            <div className="mt-9 flex flex-col gap-3 sm:flex-row">
              <Link href="/auth/login" className={btnPrimary}>Sign in to the dashboard</Link>
              <Link href="/auth/register" className={btnSecondary}>Register with an invite</Link>
            </div>

            <div className="mt-14">
              <p className="text-xs font-medium uppercase tracking-wider text-neutral-500">Works with the providers you already use</p>
              <ul className="mt-3 flex flex-wrap gap-2">
                {providers.map((p) => (
                  <li key={p} className="rounded-full border border-neutral-800 bg-neutral-900 px-3.5 py-1.5 text-sm text-neutral-200">{p}</li>
                ))}
              </ul>
            </div>
          </div>
        </section>

        {/* Problem */}
        <section className="border-t border-neutral-900 bg-neutral-900/40">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6 sm:py-20">
            <h2 className="max-w-2xl text-2xl font-bold tracking-tight text-white sm:text-3xl">
              Calling several LLM providers directly gets messy fast
            </h2>
            <ul className="mt-8 grid gap-4 md:grid-cols-3">
              {problems.map((p) => (
                <li key={p.title} className="rounded-xl border border-neutral-800 bg-neutral-900 p-6">
                  <h3 className="text-base font-semibold text-white">{p.title}</h3>
                  <p className="mt-2 text-sm leading-relaxed text-neutral-400">{p.body}</p>
                </li>
              ))}
            </ul>
          </div>
        </section>

        {/* Features */}
        <section id="features" className="scroll-mt-16 border-t border-neutral-900">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6 sm:py-24">
            <h2 className="max-w-2xl text-2xl font-bold tracking-tight text-white sm:text-3xl">
              Everything between your code and the model
            </h2>
            <p className="mt-3 max-w-2xl text-neutral-400">
              Chat completions, streaming and tool calling, behind one API that behaves the same whichever provider ends up answering.
            </p>
            <ul className="mt-10 grid gap-x-8 gap-y-8 sm:grid-cols-2 lg:grid-cols-4">
              {features.map((f) => (
                <li key={f.title}>
                  <h3 className="flex items-center gap-2 text-base font-semibold text-white">
                    <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-indigo-400" />
                    {f.title}
                  </h3>
                  <p className="mt-2 text-sm leading-relaxed text-neutral-400">{f.body}</p>
                </li>
              ))}
            </ul>
          </div>
        </section>

        {/* How it works */}
        <section id="how" className="scroll-mt-16 border-t border-neutral-900 bg-neutral-900/40">
          <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6 sm:py-24">
            <h2 className="text-2xl font-bold tracking-tight text-white sm:text-3xl">Up and running in three steps</h2>
            <ol className="mt-10 grid gap-6 md:grid-cols-3">
              {steps.map((s) => (
                <li key={s.n} className="rounded-xl border border-neutral-800 bg-neutral-900 p-6">
                  <span className="flex h-8 w-8 items-center justify-center rounded-full bg-indigo-500/15 text-sm font-semibold text-indigo-300 ring-1 ring-inset ring-indigo-500/30">{s.n}</span>
                  <h3 className="mt-4 text-base font-semibold text-white">{s.title}</h3>
                  <p className="mt-2 text-sm leading-relaxed text-neutral-400">{s.body}</p>
                </li>
              ))}
            </ol>
          </div>
        </section>

        {/* Code */}
        <section className="border-t border-neutral-900">
          <div className="mx-auto grid max-w-6xl gap-10 px-4 py-16 sm:px-6 sm:py-24 lg:grid-cols-[1fr_1.2fr] lg:items-center">
            <div>
              <h2 className="text-2xl font-bold tracking-tight text-white sm:text-3xl">Change the base URL. That is the migration.</h2>
              <p className="mt-4 leading-relaxed text-neutral-300">
                router0 speaks the OpenAI chat completions API, including server-sent-event streaming and tool calling. Agents, SDKs and frameworks that
                let you set a base URL work without code changes.
              </p>
              <p className="mt-4 text-sm text-neutral-500">
                Model names are canonical (or your own aliases), so the same request works regardless of which provider serves it.
              </p>
            </div>
            <div className="min-w-0 space-y-4">
              <figure>
                <figcaption className="mb-1.5 text-xs font-medium text-neutral-500">curl</figcaption>
                <pre className="overflow-x-auto rounded-xl border border-neutral-800 bg-neutral-900 p-4 text-[13px] leading-relaxed text-neutral-200"><code>{curl}</code></pre>
              </figure>
              <figure>
                <figcaption className="mb-1.5 text-xs font-medium text-neutral-500">Python (OpenAI SDK)</figcaption>
                <pre className="overflow-x-auto rounded-xl border border-neutral-800 bg-neutral-900 p-4 text-[13px] leading-relaxed text-neutral-200"><code>{python}</code></pre>
              </figure>
            </div>
          </div>
        </section>

        {/* Final CTA */}
        <section className="border-t border-neutral-900">
          <div className="mx-auto max-w-6xl px-4 py-16 text-center sm:px-6 sm:py-24">
            <h2 className="mx-auto max-w-2xl text-2xl font-bold tracking-tight text-white sm:text-4xl">
              Stop wiring up providers. Start shipping.
            </h2>
            <p className="mx-auto mt-4 max-w-xl text-neutral-400">
              router0 is invite-only for now. If you have an invite, create your organization and your first token in a couple of minutes.
            </p>
            <div className="mt-8 flex flex-col justify-center gap-3 sm:flex-row">
              <Link href="/auth/register" className={btnPrimary}>Register with an invite</Link>
              <Link href="/auth/login" className={btnSecondary}>Sign in</Link>
            </div>
          </div>
        </section>
      </main>

      <footer className="border-t border-neutral-900">
        <div className="mx-auto flex max-w-6xl flex-col items-start justify-between gap-3 px-4 py-8 text-sm text-neutral-500 sm:flex-row sm:items-center sm:px-6">
          <Logo className="text-base" />
          <p>Built by smallTech. A managed LLM gateway.</p>
        </div>
      </footer>
    </div>
  );
}
