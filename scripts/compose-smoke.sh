#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: scripts/compose-smoke.sh <local-siftail-image>" >&2
  exit 2
fi

image=$1
repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
suffix="$$-$RANDOM"
project="siftail-compose-smoke-$suffix"
recreated_image="siftail-compose-smoke-recreated:$suffix"
ui_port=${SIFTAIL_COMPOSE_SMOKE_UI_PORT:-38080}
ingest_port=${SIFTAIL_COMPOSE_SMOKE_INGEST_PORT:-38081}
public_url="http://127.0.0.1:$ui_port"
ingest_url="http://127.0.0.1:$ingest_port/api/v1/ingest"
password="siftail-compose-smoke-password-$suffix"
event_marker="compose-smoke-event-$suffix"
cookie_jar=$(mktemp "/tmp/siftail-compose-cookie.$suffix.XXXXXX")
history_page=$(mktemp "/tmp/siftail-compose-history.$suffix.XXXXXX")
resolved_config=$(mktemp "/tmp/siftail-compose-config.$suffix.XXXXXX")
service_logs=$(mktemp "/tmp/siftail-compose-logs.$suffix.XXXXXX")
volume_name="${project}_siftail-data"
teardown_complete=false

cleanup() {
  if [[ "$teardown_complete" != "true" ]]; then
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  docker image rm "$recreated_image" >/dev/null 2>&1 || true
  unlink "$cookie_jar" >/dev/null 2>&1 || true
  unlink "$history_page" >/dev/null 2>&1 || true
  unlink "$resolved_config" >/dev/null 2>&1 || true
  unlink "$service_logs" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for command in docker curl sed grep date; do
  if ! command -v "$command" >/dev/null; then
    echo "missing Compose smoke command: $command" >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required for the Compose smoke" >&2
  exit 1
fi

export COMPOSE_SIFTAIL_IMAGE="$image"
export SIFTAIL_PUBLIC_URL="$public_url"
export SIFTAIL_INGEST_PUBLIC_URL="$ingest_url"
export COMPOSE_SIFTAIL_LOCAL_UI_PORT="$ui_port"
export COMPOSE_SIFTAIL_LOCAL_INGEST_PORT="$ingest_port"

compose() {
  docker compose \
    --project-directory "$repository_root" \
    --project-name "$project" \
    -f "$repository_root/compose.yaml" \
    -f "$repository_root/compose.local.yaml" \
    "$@"
}

wait_ready() {
  local ready=false
  for _ in $(seq 1 200); do
    if curl --fail --silent --show-error \
      "$public_url/health/ready" >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 0.1
  done
  if [[ "$ready" != "true" ]]; then
    compose logs --no-color >&2 || true
    echo "Compose service did not become ready" >&2
    exit 1
  fi
}

history_contains_event() {
  curl --fail --silent --show-error --location \
    --cookie "$cookie_jar" --output "$history_page" \
    "$public_url/logs"
  grep --fixed-strings --quiet "$event_marker" "$history_page"
}

login() {
  login_code=$(
    curl --silent --show-error --output /dev/null \
      --write-out '%{http_code}' --cookie-jar "$cookie_jar" \
      --config - <<EOF
url = "$public_url/session"
request = "POST"
header = "Content-Type: application/x-www-form-urlencoded"
header = "Origin: $public_url"
data-urlencode = "username=Admin"
data-urlencode = "password=$password"
data-urlencode = "return=/logs"
EOF
  )
  if [[ "$login_code" != "303" ]]; then
    echo "Compose browser login returned HTTP $login_code" >&2
    exit 1
  fi
}

compose config >"$resolved_config"
if grep --fixed-strings --quiet "$password" "$resolved_config"; then
  echo "resolved Compose configuration exposed the smoke password" >&2
  exit 1
fi

printf '%s\n%s\n' "$password" "$password" |
  compose run --rm --no-deps -T siftail \
    admin create --username Admin >/dev/null
compose run --rm --no-deps -T siftail \
  server create --name Compose-Smoke >/dev/null
token_output=$(
  compose run --rm --no-deps -T siftail \
    token create --server 1 --name compose-smoke
)
token=$(sed -n 's/^token (shown once): //p' <<<"$token_output")
unset token_output
if [[ -z "$token" ]]; then
  echo "Compose smoke did not receive a one-time token" >&2
  exit 1
fi

compose up --detach --wait
wait_ready

container_id=$(compose ps --quiet siftail)
configured_user=$(docker inspect --format '{{.Config.User}}' "$container_id")
runtime_user=$(docker inspect --format '{{.State.Status}} {{.Config.User}}' "$container_id")
if [[ "$configured_user" != "65532:65532" ||
      "$runtime_user" != "running 65532:65532" ]]; then
  echo "unexpected Compose runtime identity: $runtime_user" >&2
  exit 1
fi
compose exec -T siftail /siftail config validate >/dev/null

event_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ingest_code=$(
  curl --silent --show-error --output /dev/null \
    --write-out '%{http_code}' --config - <<EOF
url = "$ingest_url"
request = "POST"
header = "Authorization: Bearer $token"
header = "Content-Type: application/json"
data = "{\"timestamp\":\"$event_time\",\"application\":\"compose-smoke\",\"service\":\"api\",\"log\":\"$event_marker\"}"
EOF
)
if [[ "$ingest_code" != "204" ]]; then
  echo "Compose ingestion returned HTTP $ingest_code" >&2
  exit 1
fi

login
history_contains_event

compose restart siftail >/dev/null
wait_ready
history_contains_event

docker image tag "$image" "$recreated_image"
export COMPOSE_SIFTAIL_IMAGE="$recreated_image"
compose up --detach --wait --force-recreate
wait_ready
history_contains_event

compose logs --no-color >"$service_logs"
for secret in "$password" "$token"; do
  if grep --fixed-strings --quiet "$secret" \
    "$resolved_config" "$service_logs"; then
    echo "Compose configuration or service logs exposed smoke credentials" >&2
    exit 1
  fi
done

compose down
if ! docker volume inspect "$volume_name" >/dev/null 2>&1; then
  echo "Compose down removed the persistent volume" >&2
  exit 1
fi

compose up --detach --wait
wait_ready
history_contains_event

compose down --volumes
teardown_complete=true
if docker volume inspect "$volume_name" >/dev/null 2>&1; then
  echo "Compose down --volumes did not remove the persistent volume" >&2
  exit 1
fi

unset token password
echo "Compose deployment smoke passed"
