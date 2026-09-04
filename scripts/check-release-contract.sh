#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

failures=0
require_match() {
  local pattern="$1"
  local file="$2"
  local message="$3"
  if ! grep -Eq "$pattern" "$file"; then
    printf 'FAIL: %s\n' "$message" >&2
    failures=$((failures + 1))
  fi
}

reject_match() {
  local pattern="$1"
  local file="$2"
  local message="$3"
  if grep -Eq "$pattern" "$file"; then
    printf 'FAIL: %s\n' "$message" >&2
    failures=$((failures + 1))
  fi
}

require_match 'needs:.*\[.*test.*build.*\]' .github/workflows/release.yml \
  'release publication must depend on both tests and native builds'
require_match 'runs-on:.*matrix\.runner' .github/workflows/release.yml \
  'release artifacts must build on target-native matrix runners'
reject_match 'CGO_ENABLED:.*0' .github/workflows/release.yml \
  'release artifacts cannot disable CGO while mattn/go-sqlite3 is in use'
for workflow in .github/workflows/*.yml; do
  while IFS= read -r action; do
    if [[ "$action" == ./* ]]; then
      continue
    fi
    if [[ ! "$action" =~ @[0-9a-f]{40}$ ]]; then
      printf 'FAIL: workflow action is not pinned to an immutable commit SHA: %s (%s)\n' \
        "$action" "$workflow" >&2
      failures=$((failures + 1))
    fi
  done < <(sed -nE 's/^[[:space:]-]*uses:[[:space:]]*([^[:space:]#]+).*$/\1/p' "$workflow")
done
require_match 'smoke-release-artifact\.sh' .github/workflows/release.yml \
  'every native artifact must pass the database startup smoke test'
require_match 'govulncheck' .github/workflows/ci.yml \
  'CI must block reachable Go vulnerabilities'
require_match 'static/js/\*\.js' .github/workflows/ci.yml \
  'CI must syntax-check every tracked browser JavaScript file'
require_match 'npm run test:browser' .github/workflows/ci.yml \
  'CI must run the real-browser smoke test'
require_match 'npm run test:browser' .github/workflows/release.yml \
  'release validation must run the real-browser smoke test'
require_match 'npm run test:browser' .github/workflows/docker-build.yml \
  'standalone container publication must run the real-browser smoke test'
require_match 'sbom-action' .github/workflows/release.yml \
  'release assets must include an SBOM'
require_match 'verify-golden-fixtures\.sh --release' .github/workflows/release.yml \
  'tag publication must require verified real upstream fixtures'
require_match 'needs:[[:space:]]*validate' .github/workflows/docker-build.yml \
  'container publication must depend on full validation'
require_match 'verify-golden-fixtures\.sh --release' .github/workflows/docker-build.yml \
  'container publication must require verified real upstream fixtures'
require_match 'sbom:[[:space:]]*true' .github/workflows/docker-build.yml \
  'published container images must include an SBOM attestation'
require_match 'smoke-container-image\.sh' .github/workflows/docker-build.yml \
  'container publication must pass the image health and non-root smoke test'
require_match 'workflow_call:' .github/workflows/docker-build.yml \
  'container publication must be reusable by the exact-head release workflow'
require_match 'should-publish-latest\.sh' .github/workflows/promote-latest.yml \
  'container publication must compute highest-stable latest-tag eligibility'
require_match 'release:' .github/workflows/promote-latest.yml \
  'latest promotion must run only after GitHub release publication'
require_match 'group:[[:space:]]*filabridge-container-latest-promotion' .github/workflows/promote-latest.yml \
  'latest promotion must serialize concurrent releases'
require_match 'cancel-in-progress:[[:space:]]*false' .github/workflows/promote-latest.yml \
  'latest promotion must not cancel an in-flight promotion'
require_match 'buildx imagetools create' .github/workflows/promote-latest.yml \
  'latest promotion must retag the already-published multi-platform manifest'
require_match 'test-latest-promotion\.sh' .github/workflows/ci.yml \
  'CI must verify latest-tag promotion policy'
require_match 'uses:[[:space:]]*\./\.github/workflows/docker-build\.yml' .github/workflows/release.yml \
  'tag releases must gate container publication behind native artifact smoke tests'

require_match '^FROM golang:[0-9]+\.[0-9]+\.[0-9]+-alpine[0-9]+\.[0-9]+@sha256:[0-9a-f]{64} AS builder$' Dockerfile \
  'builder image must use an explicit Go and Alpine patch tag plus digest'
require_match '^FROM alpine:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$' Dockerfile \
  'runtime image must use an explicit Alpine patch tag plus digest'
require_match '^USER filabridge$' Dockerfile \
  'runtime image must run as the dedicated filabridge user'
require_match '^HEALTHCHECK ' Dockerfile \
  'runtime image must define a health check'

for required in .dockerignore scripts/smoke-release-artifact.sh scripts/smoke-release-artifact.ps1 scripts/smoke-container-image.sh \
  scripts/verify-golden-fixtures.sh scripts/test-release-evidence.sh scripts/should-publish-latest.sh scripts/test-latest-promotion.sh testdata/compatibility/golden-fixtures.json \
  testdata/compatibility/golden-fixture.schema.json; do
  if [[ ! -f "$required" ]]; then
    printf 'FAIL: required release contract file is missing: %s\n' "$required" >&2
    failures=$((failures + 1))
  fi
done

if [[ "$failures" -ne 0 ]]; then
  printf '%d release contract check(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'Release contract checks passed\n'
