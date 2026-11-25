#!/usr/bin/env bash
# PostToolUse hook (Edit/Write/MultiEdit): fast feedback on the Go file Claude
# just changed. gofmt on the file, go vet on its package.
# Exit 2 = the message on stderr goes back to Claude, which fixes it.
set -uo pipefail
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"

file=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("tool_input",{}).get("file_path",""))')
[[ "$file" == *.go && -f "$file" ]] || exit 0

cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}" || exit 0

# Only files inside this repo: go vet can't load a package outside the module,
# so a scratch file elsewhere would fail here forever.
rel=$(realpath --relative-to=. "$file")
[[ "$rel" != ../* ]] || exit 0

unformatted=$(gofmt -l "$file")
if [[ -n "$unformatted" ]]; then
  echo "gofmt: $file is not formatted. Run: gofmt -w $file" >&2
  exit 2
fi

pkg="./$(dirname "$rel")"
if ! out=$(go vet "$pkg" 2>&1); then
  printf 'go vet %s failed:\n%s\n' "$pkg" "$out" >&2
  exit 2
fi
exit 0
