#!/usr/bin/env bash
# End-to-end smoke test for the payments platform.
#
# Asserts the Day 3 idempotency contract AND the Day 4 provider-outcome
# contract, by driving the real cluster through the gateway and checking what
# actually landed in Postgres.
#
# The three provider outcomes it proves:
#   healthy provider  -> 201 authorized, psp_ref stored, payment.authorized
#   503 provider      -> 201 failed,     no psp_ref,     payment.failed
#   slow provider     -> 202 pending,    no psp_ref,     NO terminal event
#
# That last one is the point of the whole day: a timeout is not a failure.
set -uo pipefail

NS=${NS:-ledgerline}
GW_PORT=${GW_PORT:-8080}
pass=0; fail=0
pf=""

ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
check(){ [[ "$2" == "$3" ]] && ok "$1 ($2)" || bad "$1: expected '$3', got '$2'"; }
head1(){ printf '\n\033[1;34m%s\033[0m\n' "$1"; }

cleanup() { [[ -n "$pf" ]] && kill "$pf" 2>/dev/null; }
trap cleanup EXIT

# All traffic goes through the gateway, so the reverse proxy and its 3s budget
# are exercised too rather than being taken on trust.
start_pf() {
  [[ -n "$pf" ]] && kill "$pf" 2>/dev/null
  kubectl -n "$NS" port-forward svc/gateway "$GW_PORT:80" >/dev/null 2>&1 &
  pf=$!
  for _ in $(seq 1 60); do
    curl -fsS "http://localhost:$GW_PORT/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  echo "gateway port-forward never became ready" >&2
  exit 1
}

# provider <latency_ms> <error_rate>
#
# Changing env triggers a rolling update, and `rollout status` returning is NOT
# enough to start measuring. With maxSurge 25% on a single replica there is a
# window where the old pod is terminating while kube-proxy still routes to its
# IP. A SYN to a pod IP that no longer exists gets no RST -- it simply vanishes
# -- so the caller hangs until its own timeout and sees an UNKNOWN outcome.
#
# That is the system behaving correctly, but it makes a test that measures the
# 503 path flaky. So wait until the dependency has settled: exactly one pod,
# exactly one ready endpoint, no stale routing left.
#
# Worth keeping in mind beyond this script: an ordinary deploy of a dependency
# manufactures unknown outcomes. Pending payments are not an exotic edge case,
# they happen every time psp-sim is redeployed.
provider() {
  kubectl -n "$NS" set env deploy/psp-sim "PSP_LATENCY_MS=$1" "PSP_ERROR_RATE=$2" >/dev/null
  kubectl -n "$NS" rollout status deploy/psp-sim --timeout=120s >/dev/null

  for _ in $(seq 1 120); do
    local pods ready
    pods=$(kubectl -n "$NS" get pods -l app=psp-sim --no-headers 2>/dev/null | wc -l)
    ready=$(kubectl -n "$NS" get endpointslices -l kubernetes.io/service-name=psp-sim \
             -o jsonpath='{range .items[*].endpoints[*]}{.conditions.ready}{"\n"}{end}' 2>/dev/null \
             | grep -c true)
    [[ "$pods" == "1" && "$ready" == "1" ]] && return 0
    sleep 0.5
  done
  echo "psp-sim endpoints never settled to a single ready pod" >&2
  return 1
}

q() { kubectl -n "$NS" exec pg-1 -c postgres -- \
        psql -U postgres -d ledgerline -qtA -c "$1" | tr -d '[:space:]'; }

post() { # $1 key, $2 body -> "<code> <body>"
  curl -sS -o /tmp/smoke.out -w '%{http_code}' \
    -X POST "http://localhost:$GW_PORT/v1/payments" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $1" --data "$2"
  printf ' '; cat /tmp/smoke.out
}

jsonf() { sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" <<<"$1"; }

BODY_A='{"account_id":"acct_1","amount_cents":1250,"currency":"EUR"}'
BODY_B='{"account_id":"acct_1","amount_cents":9999,"currency":"EUR"}'

start_pf

# ===========================================================================
head1 "A. Healthy provider: charge succeeds"
# ===========================================================================
provider 50 0
start_pf
KEY="smoke-ok-$(date +%s)-$RANDOM"

read -r code body <<<"$(post "$KEY" "$BODY_A")"
check "status code" "$code" "201"
id=$(jsonf "$body" id)
check "payment status" "$(jsonf "$body" status)" "authorized"
ref=$(jsonf "$body" psp_ref)
[[ "$ref" =~ ^psp_[0-9a-f]{16}$ ]] && ok "psp_ref returned ($ref)" || bad "psp_ref malformed: '$ref'"

check "db status"  "$(q "SELECT status FROM payments WHERE id='$id';")" "authorized"
check "db psp_ref" "$(q "SELECT psp_ref FROM payments WHERE id='$id';")" "$ref"
check "outbox events" \
  "$(q "SELECT string_agg(event_type, ',' ORDER BY id) FROM outbox WHERE aggregate_id='$id';")" \
  "payment.created,payment.authorized"

head1 "B. Idempotency still holds (Day 3 contract)"
read -r code body <<<"$(post "$KEY" "$BODY_A")"
check "replay status code" "$code" "200"
check "replay same id" "$(jsonf "$body" id)" "$id"
check "replay keeps psp_ref" "$(jsonf "$body" psp_ref)" "$ref"
# A replay must not charge again, so no new events may appear.
check "no duplicate events after replay" \
  "$(q "SELECT count(*) FROM outbox WHERE aggregate_id='$id';")" "2"

read -r code body <<<"$(post "$KEY" "$BODY_B")"
check "same key, different body" "$code" "422"

head1 "C. Replica read (pg-ro)"
app_user=$(kubectl -n "$NS" get secret pg-app -o jsonpath='{.data.username}' | base64 -d)
app_pass=$(kubectl -n "$NS" get secret pg-app -o jsonpath='{.data.password}' | base64 -d)
ro=$(kubectl -n "$NS" exec pg-1 -c postgres -- env PGPASSWORD="$app_pass" \
  psql -h pg-ro -U "$app_user" -d ledgerline -qtA \
  -c "SELECT amount_cents FROM payments WHERE id='$id';" | tr -d '[:space:]')
check "amount via replica" "$ro" "1250"

# ===========================================================================
head1 "D. Provider returns 503: a DEFINITE failure"
# ===========================================================================
# 503 is a promise that nothing was charged, so a terminal 'failed' is safe.
provider 50 1
start_pf
KEY="smoke-503-$(date +%s)-$RANDOM"

read -r code body <<<"$(post "$KEY" "$BODY_A")"
check "status code" "$code" "201"
id=$(jsonf "$body" id)
check "payment status" "$(jsonf "$body" status)" "failed"
check "no psp_ref in body" "$(jsonf "$body" psp_ref)" ""
check "db status" "$(q "SELECT status FROM payments WHERE id='$id';")" "failed"
check "db psp_ref is NULL" "$(q "SELECT psp_ref IS NULL FROM payments WHERE id='$id';")" "t"
check "outbox events" \
  "$(q "SELECT string_agg(event_type, ',' ORDER BY id) FROM outbox WHERE aggregate_id='$id';")" \
  "payment.created,payment.failed"

# ===========================================================================
head1 "E. Provider too slow: UNKNOWN, and that is not a failure"
# ===========================================================================
# psp-sim sleeps 3s; payments' http.Client gives up at 2s; the gateway's budget
# is 3s, so payments -- not the gateway -- is the one that decides and answers.
provider 3000 0
start_pf
KEY="smoke-slow-$(date +%s)-$RANDOM"

# curl's own %{time_total} rather than wrapping date arithmetic around it:
# purpose-built, and it does not depend on the shell having GNU date's %N.
read -r code secs body <<<"$(
  curl -sS -o /tmp/smoke.out -w '%{http_code} %{time_total}' \
    -X POST "http://localhost:$GW_PORT/v1/payments" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $KEY" --data "$BODY_A"
  printf ' '; cat /tmp/smoke.out
)"
elapsed=$(awk -v s="$secs" 'BEGIN{printf "%d", s*1000}')
id=$(jsonf "$body" id)

check "status code" "$code" "202"
check "payment status" "$(jsonf "$body" status)" "pending"
printf '  \033[36mINFO\033[0m round trip %sms (payments timeout 2000ms, gateway budget 3000ms)\n' "$elapsed"
# The window is the real assertion: at least 2s proves payments waited for its
# own timeout, under 3s proves the gateway did not cut it off first. If the
# budgets were inverted this would come back just under 3000ms with a 504.
[[ $elapsed -ge 1900 && $elapsed -lt 2950 ]] \
  && ok "payments' 2s timeout fired, not the gateway's 3s budget" \
  || bad "round trip ${elapsed}ms: expected the inner 2s timeout to fire first"

check "db status stays pending" "$(q "SELECT status FROM payments WHERE id='$id';")" "pending"
check "db psp_ref is NULL" "$(q "SELECT psp_ref IS NULL FROM payments WHERE id='$id';")" "t"
# THE critical assertion: no terminal event was published. A payment.failed
# here would let a consumer or a reconciliation job conclude the charge did not
# happen, and re-charge a customer who may well have paid.
check "only payment.created, no terminal event" \
  "$(q "SELECT string_agg(event_type, ',' ORDER BY id) FROM outbox WHERE aggregate_id='$id';")" \
  "payment.created"

head1 "F. Replaying an unsettled payment does not re-charge"
provider 50 0   # provider is healthy again; a naive replay would now authorize
start_pf
read -r code body <<<"$(post "$KEY" "$BODY_A")"
check "replay status code" "$code" "200"
check "still pending" "$(jsonf "$body" status)" "pending"
check "no new events" \
  "$(q "SELECT count(*) FROM outbox WHERE aggregate_id='$id';")" "1"

# ===========================================================================
head1 "Restoring healthy provider defaults"
provider 50 0

echo
printf 'passed %d, failed %d\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
