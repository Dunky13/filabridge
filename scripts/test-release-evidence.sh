#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

manifest="testdata/compatibility/golden-fixtures.json"
fixture_path="$(jq -er '.prusaslicer_artifacts[] | select(.id == "prusaslicer-3.0.0-alpha11-core-one-gcode") | .artifact_path' "$manifest")"
fixture_hash="$(jq -er '.prusaslicer_artifacts[] | select(.id == "prusaslicer-3.0.0-alpha11-core-one-gcode") | .sha256' "$manifest")"
temporary_manifest="$(mktemp "${TMPDIR:-/tmp}/filabridge-release-evidence.XXXXXX.json")"
trap 'rm -f "$temporary_manifest"' EXIT

write_test_evidence() {
  local family="$1"
  local model="$2"
  jq --arg family "$family" --arg model "$model" --arg path "$fixture_path" --arg sha "$fixture_hash" '
    .firmware_captures = [{
      id: "synthetic-gate-test-capture",
      capture_path: $path,
      sha256: $sha,
      firmware_version: "v6.10.1",
      release_url: "https://github.com/prusa3d/Prusa-Firmware-Buddy/releases/tag/v6.10.1",
      printer_family: $family,
      printer_model: $model,
      endpoints: ["/api/v1/status"],
      sanitized: true,
      verification: {verified_by: "release-gate self-test", verified_at: "2026-09-04T00:00:00Z"}
    }] |
    .hardware_verifications = [{
      prusaslicer_version: "version_3.0.0-alpha11",
      firmware_version: "v6.10.1",
      printer_family: $family,
      printer_model: $model,
      evidence_path: $path,
      sha256: $sha,
      ascii_print: true,
      bgcode_print: true,
      accounting_verified: true,
      restart_reconciliation_verified: true,
      verified_by: "release-gate self-test",
      verified_at: "2026-09-04T00:00:00Z"
    }]
  ' "$manifest" >"$temporary_manifest"
}

assert_blocked_by() {
  local expected="$1"
  local output
  if output="$(FILABRIDGE_GOLDEN_MANIFEST="$temporary_manifest" bash scripts/verify-golden-fixtures.sh --release 2>&1)"; then
    printf 'FAIL: release evidence self-test unexpectedly passed\n' >&2
    exit 1
  fi
  if [[ "$output" != *"$expected"* ]]; then
    printf 'FAIL: release evidence gate reported %q, expected %q\n' "$output" "$expected" >&2
    exit 1
  fi
}

assert_passes() {
  local output
  if ! output="$(FILABRIDGE_GOLDEN_MANIFEST="$temporary_manifest" bash scripts/verify-golden-fixtures.sh --release 2>&1)"; then
    printf 'FAIL: release evidence self-test unexpectedly failed: %s\n' "$output" >&2
    exit 1
  fi
}

# Valid evidence for another required family must not satisfy COREONE.
write_test_evidence "COREONE_INDX" "Prusa CORE One INDX 8T"
assert_blocked_by "COREONE Prusa CORE One"

# COREONE evidence must advance the matrix and then stop at COREONE_INDX.
write_test_evidence "COREONE" "Prusa CORE One"
assert_blocked_by "COREONE_INDX Prusa CORE One INDX 8T"

# Preview-only models are schema-validated but do not block stable releases.
jq --arg path "$fixture_path" --arg sha "$fixture_hash" '
  .firmware_captures = [.release_evidence_requirements[] | select(.preview == false) | {
    id: ("stable-gate-test-" + .printer_family),
    capture_path: $path,
    sha256: $sha,
    firmware_version: "v6.10.1",
    release_url: "https://github.com/prusa3d/Prusa-Firmware-Buddy/releases/tag/v6.10.1",
    printer_family: .printer_family,
    printer_model: .printer_model,
    endpoints: ["/api/v1/status"],
    sanitized: true,
    verification: {verified_by: "release-gate self-test", verified_at: "2026-09-04T00:00:00Z"}
  }] |
  .hardware_verifications = [.release_evidence_requirements[] | select(.preview == false) | {
    prusaslicer_version: "version_3.0.0-alpha11",
    firmware_version: "v6.10.1",
    printer_family: .printer_family,
    printer_model: .printer_model,
    evidence_path: $path,
    sha256: $sha,
    ascii_print: true,
    bgcode_print: true,
    accounting_verified: true,
    restart_reconciliation_verified: true,
    verified_by: "release-gate self-test",
    verified_at: "2026-09-04T00:00:00Z"
  }]
' "$manifest" >"$temporary_manifest"
assert_passes

printf 'Release evidence family-isolation checks passed\n'
