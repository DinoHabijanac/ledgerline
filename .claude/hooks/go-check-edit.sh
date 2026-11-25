#!/usr/bin/env bash
# PostToolUse hook (Edit/Write/MultiEdit): fast feedback on the Go file Claude
# just changed. gofmt on the file, go vet on its package (ADR-005).
# gofmt failure: exit 2, stderr goes back to Claude as an error to fix now.
# go vet failure: exit 0 with the output as additionalContext, a note rather
# than a demand, because mid-way through a multi-file change the package is
# expected not to vet. The Stop hook's go test and CI are the hard gates.
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
  # Build the JSON in Python so quotes and newlines in vet's output are escaped.
  msg=$(printf 'go vet %s reports (non-blocking; fine if a planned edit fixes it):\n%s' "$pkg" "$out")
  MSG="$msg" python3 -c 'import json,os; print(json.dumps({"hookSpecificOutput": {"hookEventName": "PostToolUse", "additionalContext": os.environ["MSG"]}}))'
fi
exit 0
