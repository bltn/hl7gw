#!/bin/sh
# Stop review gate: when a response touched Go code, force one review pass
# against the coding conventions in .claude/rules/ before the agent finishes.
# Exit 2 blocks the stop and feeds stderr back to the model; on the resulting
# continuation stop_hook_active is true, so the gate allows the finish.

input=$(cat)

case "$input" in
  *'"stop_hook_active":true'*|*'"stop_hook_active": true'*) exit 0 ;;
esac

root="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[ -n "$root" ] || exit 0

changed=$(
  git -C "$root" diff --name-only HEAD 2>/dev/null
  git -C "$root" ls-files --others --exclude-standard 2>/dev/null
)

echo "$changed" | grep -q '\.go$' || exit 0

rules="$root/.claude/rules"

{
  echo "You are about to finish. Review this turn's changes against the following"
  echo "conventions and revise anything that violates them, then finish."
  echo
  cat "$rules/core.md"
  if echo "$changed" | grep -q '_test\.go$'; then
    echo
    cat "$rules/tests.md"
  fi
  if echo "$changed" | grep -q '^e2e/'; then
    echo
    cat "$rules/e2e.md"
  fi
} >&2

exit 2
