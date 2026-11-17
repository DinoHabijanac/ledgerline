#!/usr/bin/env bash
# End-to-end smoke test for the payments service.
#
# Asserts the Day 3 idempotency contract:
#   1. first POST with a fresh key          -> 201, status pending
#   2. replay, identical body               -> 200, same id, no new row
#   3. replay, different body, same key     -> 422
#   4. exactly one payment row and one payment.created outbox row
#   5. the row is readable through the streaming replica (pg-ro)
set -euo pipefail

NS=${NS:-ledgerline}
PORT=${PORT:-8081}
KEY="smoke-$(date +%s)-$RANDOM"
pass=0; fail=0

ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
check(){ [[ "$2" == "$3" ]] && ok "$1 ($2)" || bad "$1: expected $3, got $2"; }

kubectl -n "$NS" port-forward svc/payments "$PORT:80" >/dev/null 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true' EXIT
# Wait for the tunnel instead of a blind sleep.
for _ in $(seq 1 40); do
  curl -fsS "http://localhost:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.25
done

post() { # $1 = idempotency key, $2 = body -> prints "<http_code> <body>"
  curl -sS -o /tmp/smoke.out -w '%{http_code}' \
    -X POST "http://localhost:$PORT/v1/payments" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $1" \
    --data "$2"
  printf ' '; cat /tmp/smoke.out
}

BODY_A='{"account_id":"acct_1","amount_cents":1250,"currency":"EUR"}'
BODY_B='{"account_id":"acct_1","amount_cents":9999,"currency":"EUR"}'

echo "idempotency key: $KEY"

echo "1. first create"
read -r code body <<<"$(post "$KEY" "$BODY_A")"
check "status code" "$code" "201"
id1=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' <<<"$body")
status1=$(sed -n 's/.*"status":"\([^"]*\)".*/\1/p' <<<"$body")
check "payment status" "$status1" "pending"
[[ -n "$id1" ]] && ok "got id $id1" || bad "no id in response: $body"

echo "2. replay, identical body"
read -r code body <<<"$(post "$KEY" "$BODY_A")"
check "status code" "$code" "200"
id2=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' <<<"$body")
check "same id returned" "$id2" "$id1"

echo "3. replay, same key, different body"
read -r code body <<<"$(post "$KEY" "$BODY_B")"
check "status code" "$code" "422"

q() { kubectl -n "$NS" exec pg-1 -c postgres -- \
        psql -U postgres -d ledgerline -qtA -c "$1" | tr -d '[:space:]'; }

echo "4. database state"
check "payment rows for key" \
  "$(q "SELECT count(*) FROM payments WHERE idempotency_key='$KEY';")" "1"
check "outbox rows for payment" \
  "$(q "SELECT count(*) FROM outbox WHERE aggregate_id='$id1';")" "1"
check "outbox event type" \
  "$(q "SELECT event_type FROM outbox WHERE aggregate_id='$id1';")" "payment.created"
check "outbox unpublished" \
  "$(q "SELECT published_at IS NULL FROM outbox WHERE aggregate_id='$id1';")" "t"

# Proves streaming replication actually works: the row was written to the
# primary through pg-rw, and we read it back over TCP from the replica.
# Unlike the queries above this one cannot use the local socket's peer auth,
# so it needs the real app credentials out of the CNPG-generated secret.
echo "5. replica read (pg-ro)"
app_user=$(kubectl -n "$NS" get secret pg-app -o jsonpath='{.data.username}' | base64 -d)
app_pass=$(kubectl -n "$NS" get secret pg-app -o jsonpath='{.data.password}' | base64 -d)
ro=$(kubectl -n "$NS" exec pg-1 -c postgres -- env PGPASSWORD="$app_pass" \
  psql -h pg-ro -U "$app_user" -d ledgerline -qtA \
  -c "SELECT amount_cents FROM payments WHERE id='$id1';" | tr -d '[:space:]')
check "amount via replica" "$ro" "1250"

echo
printf 'passed %d, failed %d\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
