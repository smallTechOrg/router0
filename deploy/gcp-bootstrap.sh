#!/usr/bin/env bash
# One-time (idempotent) GCP + GitHub setup for deploying router0 to Cloud Run
# from GitHub Actions via Workload Identity Federation (no long-lived keys).
#
# Prereqs: gcloud logged in as a project owner (`gcloud auth login`), `gh` logged
# in with admin rights on the repo, openssl.
#
# Usage (database: provide EXACTLY ONE of DATABASE_URL or CLOUDSQL_INSTANCE):
#   PROJECT_ID=my-proj ADMIN_EMAIL=you@example.com \
#   DATABASE_URL='postgres://user:pass@host:5432/router0?sslmode=require' \
#   ./deploy/gcp-bootstrap.sh
#   -- or, an existing Cloud SQL Postgres instance (the script creates the db + user) --
#   PROJECT_ID=my-proj ADMIN_EMAIL=you@example.com \
#   CLOUDSQL_INSTANCE=my-proj:us-central1:my-instance ./deploy/gcp-bootstrap.sh
#
# Optional: REGION (us-central1), GITHUB_REPO (smallTechOrg/router0),
#           ADMIN_PASSWORD (generated + printed once if unset), DB_NAME (router0).
set -euo pipefail

: "${PROJECT_ID:?set PROJECT_ID}"
: "${ADMIN_EMAIL:?set ADMIN_EMAIL (initial platform admin login)}"
REGION="${REGION:-us-central1}"
GITHUB_REPO="${GITHUB_REPO:-smallTechOrg/router0}"
DB_NAME="${DB_NAME:-router0}"
AR_REPO="router0"
DEPLOY_SA_NAME="router0-deployer"
RUNTIME_SA_NAME="router0-runtime"
POOL="github"
PROVIDER="github-router0"

if [ -n "${DATABASE_URL:-}" ] && [ -n "${CLOUDSQL_INSTANCE:-}" ]; then
  echo "Provide only one of DATABASE_URL or CLOUDSQL_INSTANCE" >&2; exit 1
fi
if [ -z "${DATABASE_URL:-}" ] && [ -z "${CLOUDSQL_INSTANCE:-}" ]; then
  echo "Provide DATABASE_URL or CLOUDSQL_INSTANCE" >&2; exit 1
fi

g() { gcloud --project "$PROJECT_ID" "$@"; }
say() { printf '\n==> %s\n' "$*"; }

PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
DEPLOY_SA="${DEPLOY_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
RUNTIME_SA="${RUNTIME_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
say "Project ${PROJECT_ID} (#${PROJECT_NUMBER}), region ${REGION}, repo ${GITHUB_REPO}"

say "Enabling APIs"
APIS=(run.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com
      iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com cloudresourcemanager.googleapis.com)
[ -n "${CLOUDSQL_INSTANCE:-}" ] && APIS+=(sqladmin.googleapis.com)
g services enable "${APIS[@]}"

say "Artifact Registry repo ${AR_REPO}"
g artifacts repositories describe "$AR_REPO" --location "$REGION" >/dev/null 2>&1 || \
  g artifacts repositories create "$AR_REPO" --repository-format=docker --location "$REGION" \
    --description "router0 images"

say "Service accounts"
for sa in "$DEPLOY_SA_NAME" "$RUNTIME_SA_NAME"; do
  g iam service-accounts describe "${sa}@${PROJECT_ID}.iam.gserviceaccount.com" >/dev/null 2>&1 || \
    g iam service-accounts create "$sa" --display-name "$sa"
done

say "IAM: deployer (Cloud Run admin, push images, act as runtime SA)"
g projects add-iam-policy-binding "$PROJECT_ID" --member "serviceAccount:${DEPLOY_SA}" \
  --role roles/run.admin --condition=None >/dev/null
g artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" \
  --member "serviceAccount:${DEPLOY_SA}" --role roles/artifactregistry.writer >/dev/null
g iam service-accounts add-iam-policy-binding "$RUNTIME_SA" \
  --member "serviceAccount:${DEPLOY_SA}" --role roles/iam.serviceAccountUser >/dev/null

# ---- Secrets (created once; NEVER overwritten: rotating AES_KEY would make every
# stored provider key undecryptable) ----------------------------------------------
secret_exists() { g secrets describe "$1" >/dev/null 2>&1; }
create_secret() { # name, value
  if secret_exists "$1"; then echo "  secret $1 exists (kept)"; return; fi
  g secrets create "$1" --replication-policy automatic >/dev/null
  printf '%s' "$2" | g secrets versions add "$1" --data-file=- >/dev/null
  echo "  secret $1 created"
}
say "Secrets"
GENERATED_ADMIN_PASSWORD=""
if [ -z "${ADMIN_PASSWORD:-}" ] && ! secret_exists router0-admin-password; then
  ADMIN_PASSWORD="$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)"; GENERATED_ADMIN_PASSWORD="$ADMIN_PASSWORD"
fi
create_secret router0-aes-key "$(openssl rand -hex 32)"
create_secret router0-jwt-secret "$(openssl rand -base64 48 | tr -d '\n')"
create_secret router0-nextauth-secret "$(openssl rand -base64 32 | tr -d '\n')"
create_secret router0-admin-password "${ADMIN_PASSWORD:-unused-existing}"

if [ -n "${CLOUDSQL_INSTANCE:-}" ]; then
  say "Cloud SQL: database + user on ${CLOUDSQL_INSTANCE}"
  INSTANCE_NAME="${CLOUDSQL_INSTANCE##*:}"
  g sql databases describe "$DB_NAME" --instance "$INSTANCE_NAME" >/dev/null 2>&1 || \
    g sql databases create "$DB_NAME" --instance "$INSTANCE_NAME"
  if ! secret_exists router0-database-url; then
    DB_PASS="$(openssl rand -hex 24)"
    if g sql users list --instance "$INSTANCE_NAME" --format='value(name)' | grep -qx router0; then
      g sql users set-password router0 --instance "$INSTANCE_NAME" --password "$DB_PASS"
    else
      g sql users create router0 --instance "$INSTANCE_NAME" --password "$DB_PASS"
    fi
    create_secret router0-database-url \
      "postgres://router0:${DB_PASS}@/${DB_NAME}?host=/cloudsql/${CLOUDSQL_INSTANCE}&sslmode=disable"
  else
    echo "  secret router0-database-url exists (kept)"
  fi
  g projects add-iam-policy-binding "$PROJECT_ID" --member "serviceAccount:${RUNTIME_SA}" \
    --role roles/cloudsql.client --condition=None >/dev/null
else
  say "Database URL secret"
  create_secret router0-database-url "$DATABASE_URL"
fi

say "Runtime SA may read the secrets"
for s in router0-aes-key router0-jwt-secret router0-nextauth-secret router0-admin-password router0-database-url; do
  g secrets add-iam-policy-binding "$s" --member "serviceAccount:${RUNTIME_SA}" \
    --role roles/secretmanager.secretAccessor >/dev/null
done

say "Workload Identity Federation for GitHub repo ${GITHUB_REPO}"
g iam workload-identity-pools describe "$POOL" --location global >/dev/null 2>&1 || \
  g iam workload-identity-pools create "$POOL" --location global --display-name "GitHub Actions"
g iam workload-identity-pools providers describe "$PROVIDER" --location global \
  --workload-identity-pool "$POOL" >/dev/null 2>&1 || \
  g iam workload-identity-pools providers create-oidc "$PROVIDER" --location global \
    --workload-identity-pool "$POOL" --issuer-uri "https://token.actions.githubusercontent.com" \
    --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" \
    --attribute-condition "assertion.repository=='${GITHUB_REPO}'"
WIF_PROVIDER="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL}/providers/${PROVIDER}"
g iam service-accounts add-iam-policy-binding "$DEPLOY_SA" \
  --member "principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL}/attribute.repository/${GITHUB_REPO}" \
  --role roles/iam.workloadIdentityUser >/dev/null

say "GitHub repository variables (${GITHUB_REPO})"
setvar() { gh variable set "$1" --repo "$GITHUB_REPO" --body "$2" >/dev/null && echo "  $1 set"; }
setvar GCP_PROJECT_ID "$PROJECT_ID"
setvar GCP_PROJECT_NUMBER "$PROJECT_NUMBER"
setvar GCP_REGION "$REGION"
setvar GCP_RUNTIME_SA "$RUNTIME_SA"
setvar GCP_DEPLOY_SA "$DEPLOY_SA"
setvar GCP_WIF_PROVIDER "$WIF_PROVIDER"
setvar GCP_CLOUDSQL_INSTANCE "${CLOUDSQL_INSTANCE:-}"
setvar ROUTER0_ADMIN_EMAIL "$ADMIN_EMAIL"

say "Done"
if [ -n "$GENERATED_ADMIN_PASSWORD" ]; then
  echo "Initial admin login (shown ONCE, also in Secret Manager as router0-admin-password):"
  echo "  email:    ${ADMIN_EMAIL}"
  echo "  password: ${GENERATED_ADMIN_PASSWORD}"
fi
echo "Next: merge to main (or run the Deploy workflow) to deploy."
