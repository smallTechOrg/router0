# Deploying router0 to Cloud Run

Two Cloud Run services, deployed by GitHub Actions on every push to `main`
(after CI passes), authenticating to GCP with Workload Identity Federation
(no stored keys):

| Service | Image | What it is |
|---|---|---|
| `router0-gateway` | `Dockerfile` | the Go gateway: OpenAI-compatible `/v1/*`, `/api/*`, `/auth/*`, `/admin/*`. Point your agents at `<gateway-url>/v1`. |
| `router0-dashboard` | `dashboard/Dockerfile` | the Next.js dashboard, proxying API calls to the gateway |

The only stateful dependency is **Postgres** (no Redis).

## One-time setup

```bash
gcloud auth login          # as a project owner
gh auth status             # needs admin on the repo (sets repository variables)

# External/any reachable Postgres:
PROJECT_ID=<project> ADMIN_EMAIL=<you@example.com> \
DATABASE_URL='postgres://user:pass@host:5432/router0?sslmode=require' \
./deploy/gcp-bootstrap.sh

# ...or an existing Cloud SQL (Postgres) instance in the project:
PROJECT_ID=<project> ADMIN_EMAIL=<you@example.com> \
CLOUDSQL_INSTANCE=<project>:us-central1:<instance> ./deploy/gcp-bootstrap.sh
```

The script is idempotent. It enables APIs, creates the Artifact Registry repo,
the deploy/runtime service accounts, the Secret Manager secrets (`router0-aes-key`,
`router0-jwt-secret`, `router0-nextauth-secret`, `router0-admin-password`,
`router0-database-url`), the GitHub OIDC trust, and sets the repository variables
the workflow reads. Existing secrets are never overwritten: **do not rotate
`router0-aes-key`** unless you are happy to lose every stored provider key
(they are AES-GCM encrypted with it).

## Deploy

Merge to `main` (or run **Actions → Deploy → Run workflow**). The workflow builds
both images, deploys the gateway (migrations run on startup), smoke-tests
`/healthz`, then builds the dashboard with the gateway URL baked in (Next bakes
proxy rewrites at build time), deploys it, smoke-tests `/auth/login`, and prints
both URLs in the run summary.

## Runtime choices (and why)

- `--max-instances 1` on the gateway: key cooldown state and Gemini 3 thought
  signatures are cached in-process. Postgres stays the source of truth, but
  several instances would briefly disagree and lose signatures across turns.
- Request-based billing (CPU only while a request is in flight): this GCP project's
  org policy (`custom.denyInstanceBilling`) forbids always-allocated CPU. So the
  request log is written synchronously before each request returns
  (`ROUTER0_RECORDER_SYNC=true`), and the 6-hourly catalog refresh may be delayed
  while the service is idle (it also refreshes at start-up, on a model miss, and
  from the Catalog page's Refresh button).
- `--timeout 900`: long streaming completions.
- Provider keys are entered in the dashboard by each org (BYOK); none are deployed.
- `DB_MAX_CONNS` (optional env var) caps the gateway's connection pool.

## First login

Log in to the dashboard with `ROUTER0_ADMIN_EMAIL` and the generated admin
password (printed once by the bootstrap script; also in Secret Manager as
`router0-admin-password`). Then add provider keys (Provider Keys page), create a
service token (Tokens page), and use it as the API key with base URL
`<gateway-url>/v1`.
