#!/usr/bin/env bash
set -euo pipefail

candidate="${1:?usage: should-publish-latest.sh TAG}"
if [[ ! "$candidate" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'false\n'
  exit 0
fi

tag_list="${FILABRIDGE_RELEASE_TAGS:-}"
if [[ -z "$tag_list" ]]; then
  tag_list="$(git tag --list 'v*')"
fi
latest="$(printf '%s\n' "$tag_list" | sed -nE '/^v[0-9]+\.[0-9]+\.[0-9]+$/p' | sort -V | tail -n 1)"
if [[ -n "$latest" && "$candidate" == "$latest" ]]; then
  printf 'true\n'
else
  printf 'false\n'
fi
