#!/usr/bin/env bash
set -euo pipefail

artifact="${1:?usage: smoke-release-artifact.sh ARTIFACT}"
if [[ ! -f "$artifact" ]]; then
  printf 'artifact not found: %s\n' "$artifact" >&2
  exit 1
fi

artifact_dir="$(cd "$(dirname "$artifact")" && pwd)"
artifact_path="$artifact_dir/$(basename "$artifact")"
chmod +x "$artifact_path"

smoke_root="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
smoke_dir="$(mktemp -d "$smoke_root/filabridge-smoke.XXXXXX")"
data_dir="$smoke_dir/data"
log_file="$smoke_dir/filabridge.log"
port="${FILABRIDGE_SMOKE_PORT:-59321}"
mkdir -p "$data_dir"

pid=""
cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      if ! kill -0 "$pid" 2>/dev/null; then
        break
      fi
      sleep 0.1
    done
    kill -KILL "$pid" 2>/dev/null || true
  fi
  rm -rf "$smoke_dir"
}
trap cleanup EXIT INT TERM

FILABRIDGE_DB_PATH="$data_dir" "$artifact_path" \
  --web-only --host 127.0.0.1 --port "$port" >"$log_file" 2>&1 &
pid=$!

healthy=0
for _ in $(seq 1 60); do
  if ! kill -0 "$pid" 2>/dev/null; then
    printf 'artifact exited before becoming healthy:\n' >&2
    sed -n '1,200p' "$log_file" >&2
    exit 1
  fi
  if curl --fail --silent --max-time 2 \
    "http://127.0.0.1:$port/healthz" >/dev/null; then
    healthy=1
    break
  fi
  sleep 0.25
done

if [[ "$healthy" -ne 1 ]]; then
  printf 'artifact did not become healthy:\n' >&2
  sed -n '1,200p' "$log_file" >&2
  exit 1
fi

database="$data_dir/filabridge.db"
if [[ ! -s "$database" ]]; then
  printf 'artifact served HTTP but did not initialize SQLite database: %s\n' "$database" >&2
  exit 1
fi

printf 'Artifact smoke test passed: %s\n' "$(basename "$artifact_path")"
