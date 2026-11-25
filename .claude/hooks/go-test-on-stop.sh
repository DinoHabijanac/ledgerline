#!/usr/bin/env bash
# Stop hook: before Claude ends its turn, run the full Go test suite under -race,
# but only if Go code changed (planning and theory sessions skip it).
# Exit 2 = Claude is sent back to fix the failure. After 3 blocked attempts it
# lets go once and only warns, so a test it can't fix can't loop forever. Giving
# up resets the counter, so the next turn is enforced again.
set -uo pipefail
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}" || exit 0

session=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("session_id","x"))')

# Go changes = uncommitted .go/go.mod/go.sum, or commits on this branch not on origin/main.
changed=$(git status --porcelain -- '*.go' go.mod go.sum)
if [[ -z "$changed" ]] && git rev-parse -q --verify origin/main >/dev/null; then
  changed=$(git diff --name-only origin/main...HEAD -- '*.go' go.mod go.sum)
fi
[[ -n "$changed" ]] || exit 0

counter="${TMPDIR:-/tmp}/ledgerline-stop-${session}"
if out=$(go test -race ./... 2>&1); then
  rm -f "$counter"
  exit 0
fi

n=$(( $(cat "$counter" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$counter"
if (( n > 3 )); then
  rm -f "$counter"
  echo "go test -race ./... still fails after 3 attempts; stopping. Tell the user." >&2
  exit 0
fi
printf 'go test -race ./... failed (attempt %d of 3). Fix it before finishing:\n%s\n' "$n" "$(echo "$out" | grep -v '^ok ' | tail -40)" >&2
exit 2
