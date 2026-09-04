#!/usr/bin/env bash
set -euo pipefail

image="${1:?usage: smoke-container-image.sh IMAGE}"
suffix="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}-$$"
container="filabridge-smoke-${suffix//[^a-zA-Z0-9_.-]/-}"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d --name "$container" \
  -e FILABRIDGE_WEB_USERNAME=smoke \
  -e FILABRIDGE_WEB_PASSWORD=smoke-only-password \
  "$image" >/dev/null

health=""
for _ in $(seq 1 60); do
  health="$(docker inspect "$container" --format '{{.State.Health.Status}}')"
  if [[ "$health" == "healthy" ]]; then
    break
  fi
  if [[ "$health" == "unhealthy" ]]; then
    docker logs "$container" >&2
    exit 1
  fi
  sleep 1
done

if [[ "$health" != "healthy" ]]; then
  docker logs "$container" >&2
  printf 'container did not become healthy\n' >&2
  exit 1
fi

docker exec "$container" sh -ec '
  test "$(id -u)" = 10001
  test -s /app/data/filabridge.db
  wget -q -O /dev/null http://127.0.0.1:5000/healthz
'

printf 'Container smoke test passed: %s\n' "$image"
