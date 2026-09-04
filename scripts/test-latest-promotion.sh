#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

tags=$'v1.9.0\nv1.10.0\nv2.0.0-rc.1\nnot-a-release'
assert_result() {
  local expected="$1"
  local candidate="$2"
  local actual
  actual="$(FILABRIDGE_RELEASE_TAGS="$tags" bash scripts/should-publish-latest.sh "$candidate")"
  if [[ "$actual" != "$expected" ]]; then
    printf 'FAIL: latest promotion for %s = %s, want %s\n' "$candidate" "$actual" "$expected" >&2
    exit 1
  fi
}

assert_result true v1.10.0
assert_result false v1.9.0
assert_result false v2.0.0-rc.1
assert_result false malformed
printf 'Latest stable release promotion checks passed\n'
