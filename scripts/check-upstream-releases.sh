#!/usr/bin/env bash
set -euo pipefail

manifest="testdata/compatibility/upstream-releases.json"
authorization="Authorization: Bearer ${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
api_version="X-GitHub-Api-Version: 2022-11-28"

latest_prusaslicer="$({ curl -fsSL -H "$authorization" -H "$api_version" \
  'https://api.github.com/repos/prusa3d/PrusaSlicer/releases?per_page=100'; } | \
  jq -er '[.[] | select(.draft == false) | select(.tag_name | startswith("version_3.0"))][0].tag_name')"
latest_firmware="$({ curl -fsSL -H "$authorization" -H "$api_version" \
  'https://api.github.com/repos/prusa3d/Prusa-Firmware-Buddy/releases/latest'; } | jq -er '.tag_name')"
latest_spoolman="$({ curl -fsSL -H "$authorization" -H "$api_version" \
  'https://api.github.com/repos/Donkie/Spoolman/releases/latest'; } | jq -er '.tag_name')"

changed=0
check_release() {
  key="$1"
  actual="$2"
  expected="$(jq -er --arg key "$key" '.[$key]' "$manifest")"
  if [[ "$actual" != "$expected" ]]; then
    echo "::error title=Upstream release changed::${key}: pinned ${expected}, latest ${actual}. Refresh compatibility fixtures and update ${manifest}."
    changed=1
  else
    echo "${key}: ${actual}"
  fi
}

check_release prusaslicer_3 "$latest_prusaslicer"
check_release firmware_buddy "$latest_firmware"
check_release spoolman "$latest_spoolman"
exit "$changed"
