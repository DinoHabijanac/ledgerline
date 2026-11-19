#!/usr/bin/env bash
# Bring the whole local platform up from zero. Idempotent: safe to re-run.
#
#   ./scripts/up.sh          full bring-up
#   ./scripts/up.sh --deploy skip cluster+postgres, just rebuild and redeploy the Go services
#
set -euo pipefail

export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
export KO_DOCKER_REPO=kind.local
export KIND_CLUSTER_NAME=ledgerline

CNPG_VERSION=1.29.1
CNPG_BRANCH=release-1.29
NS=ledgerline
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31m==> %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- preflight
# Fail fast and in plain language. Without this, a stopped Docker Desktop
# surfaces as a confusing kind error several lines deep, and -- worse -- kind
# has been observed exiting 0 after failing to create the cluster, so the
# script would sail on and report success on a platform that does not exist.
#
# Lesson worth keeping: never trust an exit code alone, assert the
# postcondition you actually care about.
docker info >/dev/null 2>&1 || die "Docker is not reachable. Start Docker Desktop (and check its WSL integration), then re-run."

# --deploy re-deploys the Go services only. Used during a build session so you
# are not waiting 4 minutes for Postgres every time you change a handler.
if [[ "${1:-}" == "--deploy" ]]; then
  log "Rebuilding and redeploying Go services"
  ko apply -f deploy/base/
  kubectl -n "$NS" rollout status deploy/gateway deploy/payments deploy/psp-sim --timeout=120s
  exit 0
fi

# ---------------------------------------------------------------- cluster
if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
  log "kind cluster '$KIND_CLUSTER_NAME' already exists, reusing it"
else
  log "Creating kind cluster"
  kind create cluster --config deploy/kind/cluster.yaml
fi

# Verify the postcondition rather than trusting the exit code above.
kubectl --context "kind-$KIND_CLUSTER_NAME" get nodes >/dev/null 2>&1 \
  || die "kind reported no error but the cluster is not reachable. Try: kind delete cluster --name $KIND_CLUSTER_NAME"

# ---------------------------------------------------------------- operator
# --server-side avoids the "metadata.annotations too long" error that the plain
# client-side apply hits on CNPG's very large CRDs.
log "Installing CloudNativePG $CNPG_VERSION"
kubectl apply --server-side -f \
  "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/${CNPG_BRANCH}/releases/cnpg-${CNPG_VERSION}.yaml"
kubectl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=300s

# ---------------------------------------------------------------- postgres
log "Applying namespace and Postgres cluster"
kubectl apply -f deploy/base/00-namespace.yaml -f deploy/base/postgres.yaml
# Ready means the primary is up AND the replica has finished streaming.
kubectl -n "$NS" wait --for=condition=Ready cluster/pg --timeout=300s

# ---------------------------------------------------------------- migrations
log "Running migrations"
"$REPO_ROOT/scripts/migrate.sh"

# ---------------------------------------------------------------- services
log "Building and deploying Go services with ko"
ko apply -f deploy/base/
kubectl -n "$NS" rollout status deploy/gateway deploy/payments deploy/psp-sim --timeout=180s

log "Up. Cluster state:"
kubectl -n "$NS" get pods
printf '\nPort-forward the gateway with:\n  kubectl -n %s port-forward svc/gateway 8080:80\n' "$NS"
