#!/usr/bin/env bash
set -euo pipefail

mode="${1:---structure}"
if [[ "$mode" != "--structure" && "$mode" != "--release" ]]; then
  printf 'usage: verify-golden-fixtures.sh [--structure|--release]\n' >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
manifest="${FILABRIDGE_GOLDEN_MANIFEST:-testdata/compatibility/golden-fixtures.json}"
pins="${FILABRIDGE_UPSTREAM_PINS:-testdata/compatibility/upstream-releases.json}"

command -v jq >/dev/null || {
  printf 'jq is required to verify golden fixtures\n' >&2
  exit 2
}

jq -e '
  . as $manifest |
  .schema_version == 2 and
  (.release_evidence_requirements | type == "array" and length > 0) and
  ([.release_evidence_requirements[] | [.printer_family, .printer_model]] | unique | length) ==
    (.release_evidence_requirements | length) and
  all(.release_evidence_requirements[];
    (.printer_family | test("^[A-Z0-9_]+$")) and
    (.printer_model | type == "string" and length > 0) and
    (.preview | type == "boolean")) and
  (.prusaslicer_artifacts | type == "array") and
  (.firmware_captures | type == "array") and
  (.hardware_verifications | type == "array") and
  all(.prusaslicer_artifacts[]; . as $artifact |
    (.id | type == "string" and length > 0) and
    (.artifact_path | type == "string" and startswith("testdata/compatibility/golden/")) and
    (.sha256 | test("^[0-9a-f]{64}$")) and
    (.source.version | type == "string" and length > 0) and
    (.source.commit | test("^[0-9a-f]{40}$")) and
    (.source.download_url | startswith("https://")) and
    (.source.preset_id | type == "string" and length > 0) and
    (.format == "gcode" or .format == "bgcode") and
    (.printer_model | type == "string" and length > 0) and
    (.toolheads | type == "number" and . >= 1) and
    (.used_toolheads | type == "number" and . >= 1 and . <= $artifact.toolheads) and
    (.expected_grams_by_logical_tool | type == "object") and
    (($artifact.expected_grams_by_logical_tool | keys | sort) ==
      ([range(0; $artifact.toolheads) | tostring] | sort)) and
    ([$artifact.expected_grams_by_logical_tool[] | select(. > 0)] | length == $artifact.used_toolheads) and
    (.production.producer == "prusaslicer" or .production.producer == "libbgcode") and
     (.production.producer_commit | test("^[0-9a-f]{40}$")) and
    (if .production.producer == "prusaslicer" then
       .production.producer_commit == .source.commit and
       (.production | has("input_artifact_id") | not)
     else
       .format == "bgcode" and
       (.production.input_artifact_id | type == "string" and length > 0) and
       any($manifest.prusaslicer_artifacts[];
         .id == $artifact.production.input_artifact_id and
         .format == "gcode" and
         .source.version == $artifact.source.version and
         .production.producer == "prusaslicer")
     end) and
    (.verification.verified_by | type == "string" and length > 0) and
    (.verification.verified_at | fromdateiso8601 > 0)) and
  all(.firmware_captures[];
    (.id | type == "string" and length > 0) and
    (.capture_path | type == "string" and startswith("testdata/compatibility/golden/")) and
    (.sha256 | test("^[0-9a-f]{64}$")) and
    (.firmware_version | type == "string" and length > 0) and
    (.release_url | startswith("https://")) and
    (.printer_family | test("^[A-Z0-9_]+$")) and
    (.printer_model | type == "string" and length > 0) and
    (. as $capture | any($manifest.release_evidence_requirements[];
      .printer_family == $capture.printer_family and .printer_model == $capture.printer_model)) and
    (.endpoints | type == "array" and length > 0) and
    (.sanitized == true) and
    (.verification.verified_by | type == "string" and length > 0) and
    (.verification.verified_at | fromdateiso8601 > 0)) and
  all(.hardware_verifications[];
    (.prusaslicer_version | type == "string" and length > 0) and
    (.firmware_version | type == "string" and length > 0) and
    (.printer_family | test("^[A-Z0-9_]+$")) and
    (.printer_model | type == "string" and length > 0) and
    (. as $hardware | any($manifest.release_evidence_requirements[];
      .printer_family == $hardware.printer_family and .printer_model == $hardware.printer_model)) and
    (.evidence_path | type == "string" and startswith("testdata/compatibility/golden/")) and
    (.sha256 | test("^[0-9a-f]{64}$")) and
    (.ascii_print == true) and
    (.bgcode_print == true) and
    (.accounting_verified == true) and
    (.restart_reconciliation_verified == true) and
    (.verified_by | type == "string" and length > 0) and
    (.verified_at | fromdateiso8601 > 0))
' "$manifest" >/dev/null

verify_file_hashes() {
  local path hash actual
  while IFS=$'\t' read -r path hash; do
    [[ -z "$path" ]] && continue
    if [[ ! -f "$path" ]]; then
      printf 'golden fixture is missing: %s\n' "$path" >&2
      return 1
    fi
    actual="$(sha256sum "$path" | awk '{print $1}')"
    if [[ "$actual" != "$hash" ]]; then
      printf 'golden fixture checksum mismatch: %s\n' "$path" >&2
      return 1
    fi
  done
}

jq -r '.prusaslicer_artifacts[] | [.artifact_path, .sha256] | @tsv' "$manifest" | verify_file_hashes
jq -r '.firmware_captures[] | [.capture_path, .sha256] | @tsv' "$manifest" | verify_file_hashes
jq -r '.hardware_verifications[] | [.evidence_path, .sha256] | @tsv' "$manifest" | verify_file_hashes

if [[ "$mode" == "--structure" ]]; then
  printf 'Golden fixture manifest structure passed\n'
  exit 0
fi

slicer_version="$(jq -er '.prusaslicer_3' "$pins")"
firmware_version="$(jq -er '.firmware_buddy' "$pins")"

jq -e --arg slicer "$slicer_version" '
  any(.prusaslicer_artifacts[];
    .source.version == $slicer and .format == "gcode" and .production.producer == "prusaslicer") and
  any(.prusaslicer_artifacts[];
    .source.version == $slicer and .format == "bgcode" and
    (.production.producer == "prusaslicer" or .production.producer == "libbgcode")) and
  any(.prusaslicer_artifacts[];
    .source.version == $slicer and .format == "gcode" and
    .production.producer == "prusaslicer" and .toolheads >= 8)
' "$manifest" >/dev/null || {
  printf 'release blocked: %s needs a PrusaSlicer-produced G-code, an honestly attributed BGCode, and a PrusaSlicer-produced eight-slot artifact\n' "$slicer_version" >&2
  exit 1
}

while IFS=$'\t' read -r printer_family printer_model; do
  jq -e --arg firmware "$firmware_version" --arg family "$printer_family" --arg model "$printer_model" '
    any(.firmware_captures[];
      .firmware_version == $firmware and .printer_family == $family and .printer_model == $model)
  ' "$manifest" >/dev/null || {
    printf 'release blocked: %s %s (%s) needs a verified sanitized firmware capture\n' \
      "$printer_family" "$printer_model" "$firmware_version" >&2
    exit 1
  }

  jq -e --arg slicer "$slicer_version" --arg firmware "$firmware_version" \
    --arg family "$printer_family" --arg model "$printer_model" '
    any(.hardware_verifications[];
      .prusaslicer_version == $slicer and .firmware_version == $firmware and
      .printer_family == $family and .printer_model == $model)
  ' "$manifest" >/dev/null || {
    printf 'release blocked: %s %s needs a verified real-printer gate for %s with %s\n' \
      "$printer_family" "$printer_model" "$slicer_version" "$firmware_version" >&2
    exit 1
  }
done < <(jq -r '.release_evidence_requirements[] | select(.preview == false) | [.printer_family, .printer_model] | @tsv' "$manifest")

printf 'Golden fixture release gate passed\n'
