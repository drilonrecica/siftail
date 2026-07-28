#!/usr/bin/env bash
set -euo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
empty_env=$(mktemp)
base_config=$(mktemp)
local_config=$(mktemp)
coolify_env=$(mktemp)
coolify_override=$(mktemp)
coolify_config=$(mktemp)

cleanup() {
  unlink "$empty_env" >/dev/null 2>&1 || true
  unlink "$base_config" >/dev/null 2>&1 || true
  unlink "$local_config" >/dev/null 2>&1 || true
  unlink "$coolify_env" >/dev/null 2>&1 || true
  unlink "$coolify_override" >/dev/null 2>&1 || true
  unlink "$coolify_config" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required for Compose validation" >&2
  exit 1
fi

compose=(
  docker compose
  --project-directory "$repository_root"
  --env-file "$empty_env"
)
public_url=http://127.0.0.1:18080
ingest_url=http://127.0.0.1:18081/api/v1/ingest

run_config() {
  env -u SIFTAIL_PUBLIC_URL -u SIFTAIL_INGEST_PUBLIC_URL \
    SIFTAIL_PUBLIC_URL="$public_url" \
    SIFTAIL_INGEST_PUBLIC_URL="$ingest_url" \
    "${compose[@]}" "$@"
}

run_config -f "$repository_root/compose.yaml" config >"$base_config"
run_config -f "$repository_root/compose.yaml" \
  -f "$repository_root/compose.local.yaml" config >"$local_config"

printf '%s\n' \
  'COMPOSE_SIFTAIL_IMAGE=ghcr.io/drilonrecica/siftail:sft-051' \
  'COMPOSE_SIFTAIL_STOP_GRACE_PERIOD=47s' \
  "SIFTAIL_PUBLIC_URL=$public_url" \
  "SIFTAIL_INGEST_PUBLIC_URL=$ingest_url" \
  >"$coolify_env"
printf '%s\n' \
  'services:' \
  '  siftail:' \
  '    env_file:' \
  "      - $coolify_env" \
  >"$coolify_override"
docker compose \
  --project-directory "$repository_root" \
  --env-file "$coolify_env" \
  -f "$repository_root/compose.yaml" \
  -f "$coolify_override" \
  config >"$coolify_config"

if ! grep --fixed-strings --quiet \
  "SIFTAIL_PUBLIC_URL: \"\${SIFTAIL_PUBLIC_URL:?}\"" \
  "$repository_root/compose.yaml" ||
  ! grep --fixed-strings --quiet \
    "SIFTAIL_INGEST_PUBLIC_URL: \"\${SIFTAIL_INGEST_PUBLIC_URL:?}\"" \
    "$repository_root/compose.yaml"; then
  echo "required URLs must use Coolify-compatible empty :? expressions" >&2
  exit 1
fi

if grep --quiet '^    ports:' "$base_config"; then
  echo "base Compose file must not publish host ports" >&2
  exit 1
fi
loopback_bind_count=$(
  grep --count 'host_ip: 127\.0\.0\.1' "$local_config" || true
)
if [[ "$loopback_bind_count" -ne 2 ]]; then
  echo "local Compose override must publish both listeners on loopback" >&2
  exit 1
fi
if ! grep --quiet \
  'image: ghcr\.io/drilonrecica/siftail:0\.5\.0' "$base_config"; then
  echo "base Compose file must default to the versioned 0.5.0 image" >&2
  exit 1
fi
if ! grep --quiet 'COOLIFY_APP_NAME: siftail-self' "$base_config"; then
  echo "base Compose file must preserve the Coolify self-exclusion identity" >&2
  exit 1
fi
if ! grep --quiet 'stop_grace_period: 40s' "$base_config"; then
  echo "base Compose file must preserve the default shutdown grace period" >&2
  exit 1
fi
if ! grep --quiet \
  'image: ghcr\.io/drilonrecica/siftail:sft-051' "$coolify_config" ||
  ! grep --quiet 'stop_grace_period: 47s' "$coolify_config"; then
  echo "Coolify-style Compose controls did not resolve" >&2
  exit 1
fi
if grep --extended-regexp --quiet \
  '^[[:space:]]+SIFTAIL_(IMAGE|STOP_GRACE_PERIOD|LOCAL_)' \
  "$coolify_config"; then
  echo "Coolify env_file leaked a Compose-only variable into SIFTAIL_*" >&2
  exit 1
fi

services=$(run_config -f "$repository_root/compose.yaml" config --services)
if [[ "$services" != "siftail" ]]; then
  echo "base Compose file must define exactly one siftail service" >&2
  exit 1
fi

volumes=$(run_config -f "$repository_root/compose.yaml" config --volumes)
if [[ "$volumes" != "siftail-data" ]]; then
  echo "base Compose file must define exactly one siftail-data volume" >&2
  exit 1
fi

if env -u SIFTAIL_PUBLIC_URL -u SIFTAIL_INGEST_PUBLIC_URL \
  "${compose[@]}" -f "$repository_root/compose.yaml" \
  config --quiet >/dev/null 2>&1; then
  echo "Compose validation accepted missing required public URLs" >&2
  exit 1
fi

if env -u SIFTAIL_INGEST_PUBLIC_URL \
  SIFTAIL_PUBLIC_URL="$public_url" \
  "${compose[@]}" -f "$repository_root/compose.yaml" \
  config --quiet >/dev/null 2>&1; then
  echo "Compose validation accepted a missing ingestion public URL" >&2
  exit 1
fi

echo "Compose configuration validation passed"
