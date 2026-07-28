#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: scripts/container-smoke.sh <image> <amd64|arm64>" >&2
  exit 2
fi

image=$1
architecture=$2
case "$architecture" in
  amd64|arm64) ;;
  *)
    echo "unsupported smoke architecture: $architecture" >&2
    exit 2
    ;;
esac

for command in podman curl sed grep awk tar; do
  if ! command -v "$command" >/dev/null; then
    echo "missing container-smoke command: $command" >&2
    exit 1
  fi
done

suffix="$$-$RANDOM"
container_name="siftail-container-smoke-$architecture-$suffix"
inspection_container="$container_name-inspect"
volume_name="siftail-container-smoke-$architecture-$suffix"
cookie_jar=$(mktemp "/tmp/siftail-container-cookie.$suffix.XXXXXX")
batch_status=$(mktemp "/tmp/siftail-container-status.$suffix.XXXXXX")
second_batch_status=$(mktemp "/tmp/siftail-container-status-two.$suffix.XXXXXX")
history_page=$(mktemp "/tmp/siftail-container-history.$suffix.XXXXXX")
content_listing=$(mktemp "/tmp/siftail-container-content.$suffix.XXXXXX")
bad_directory=$(mktemp -d "/tmp/siftail-container-bad.$suffix.XXXXXX")
good_directory=$(mktemp -d "/tmp/siftail-container-good.$suffix.XXXXXX")
ui_port=${SIFTAIL_SMOKE_UI_PORT:-28080}
ingest_port=${SIFTAIL_SMOKE_INGEST_PORT:-28081}
password="siftail-container-smoke-password"

cleanup() {
  podman container rm --force "$container_name" >/dev/null 2>&1 || true
  podman container rm --force "$inspection_container" >/dev/null 2>&1 || true
  podman volume rm "$volume_name" >/dev/null 2>&1 || true
  unlink "$cookie_jar" >/dev/null 2>&1 || true
  unlink "$batch_status" >/dev/null 2>&1 || true
  unlink "$second_batch_status" >/dev/null 2>&1 || true
  unlink "$history_page" >/dev/null 2>&1 || true
  unlink "$content_listing" >/dev/null 2>&1 || true
  chmod 0700 "$bad_directory" >/dev/null 2>&1 || true
  rmdir "$bad_directory" >/dev/null 2>&1 || true
  podman unshare chmod 0700 "$good_directory" >/dev/null 2>&1 || true
  podman unshare rmdir "$good_directory" >/dev/null 2>&1 || true
}
trap cleanup EXIT

qemu=()
entrypoint=()
if [[ "$architecture" == "arm64" && "$(uname -m)" != "aarch64" ]]; then
  if [[ -z "${SIFTAIL_QEMU_AARCH64:-}" ||
        ! -x "${SIFTAIL_QEMU_AARCH64}" ]]; then
    echo "SIFTAIL_QEMU_AARCH64 must name an executable static emulator" >&2
    exit 1
  fi
  qemu=(-v "${SIFTAIL_QEMU_AARCH64}:/qemu-aarch64-static:ro,Z")
  entrypoint=(--entrypoint /qemu-aarch64-static)
fi

run_command() {
  local volume=$1
  shift
  if [[ ${#entrypoint[@]} -eq 0 ]]; then
    podman run --rm --platform "linux/$architecture" \
      -v "$volume:/data:Z" "$image" "$@"
  else
    podman run --rm --platform "linux/$architecture" \
      -v "$volume:/data:Z" "${qemu[@]}" "${entrypoint[@]}" \
      "$image" /siftail "$@"
  fi
}

create_administrator() {
  if [[ ${#entrypoint[@]} -eq 0 ]]; then
    podman run --rm -i --platform "linux/$architecture" \
      -v "$volume_name:/data:Z" "$image" admin create --username Admin
  else
    podman run --rm -i --platform "linux/$architecture" \
      -v "$volume_name:/data:Z" "${qemu[@]}" "${entrypoint[@]}" \
      "$image" /siftail admin create --username Admin
  fi
}

configured_user=$(podman image inspect "$image" --format '{{.Config.User}}')
configured_arch=$(podman image inspect "$image" --format '{{.Architecture}}')
if [[ "$configured_user" != "65532:65532" ||
      "$configured_arch" != "$architecture" ]]; then
  echo "unexpected image identity: user=$configured_user arch=$configured_arch" >&2
  exit 1
fi

podman create --name "$inspection_container" --platform "linux/$architecture" \
  "$image" >/dev/null
podman export "$inspection_container" | tar -tf - >"$content_listing"
podman rm "$inspection_container" >/dev/null
for required_path in \
  LICENSE data/ siftail \
  etc/ssl/certs/ca-certificates.crt usr/share/zoneinfo/UTC; do
  if ! grep -qx "$required_path" "$content_listing"; then
    echo "runtime image is missing $required_path" >&2
    exit 1
  fi
done
for forbidden_path in \
  bin/sh bin/bash usr/bin/apt usr/bin/apt-get usr/bin/dpkg \
  usr/bin/gcc usr/bin/cc usr/local/go/bin/go \
  usr/bin/node usr/bin/npm usr/bin/npx \
  src/ cmd/ internal/ node_modules/ test-results/ playwright-report/; do
  if grep -qx "$forbidden_path" "$content_listing"; then
    echo "runtime image unexpectedly contains $forbidden_path" >&2
    exit 1
  fi
done

chmod 0500 "$bad_directory"
if run_command "$bad_directory" config validate >/dev/null 2>&1; then
  echo "non-root image wrote an intentionally unwritable bind mount" >&2
  exit 1
fi
podman unshare chown 65532:65532 "$good_directory"
podman unshare chmod 0750 "$good_directory"
run_command "$good_directory" config validate >/dev/null

podman volume create "$volume_name" >/dev/null
printf '%s\n%s\n' "$password" "$password" |
  create_administrator >/dev/null
run_command "$volume_name" server create --name Container-Smoke >/dev/null
token_output=$(run_command "$volume_name" token create \
  --server 1 --name container-smoke)
token=$(printf '%s\n' "$token_output" |
  sed -n 's/^token (shown once): //p')
if [[ -z "$token" ]]; then
  echo "container smoke did not receive a one-time token" >&2
  exit 1
fi

run_options=(
  -d
  --name "$container_name"
  --platform "linux/$architecture"
  -v "$volume_name:/data:Z"
  -p "127.0.0.1:$ui_port:8080"
  -p "127.0.0.1:$ingest_port:8081"
  -e "SIFTAIL_PUBLIC_URL=http://127.0.0.1:$ui_port"
  -e "SIFTAIL_INGEST_PUBLIC_URL=http://127.0.0.1:$ingest_port/api/v1/ingest"
)
if [[ ${#entrypoint[@]} -eq 0 ]]; then
  podman run "${run_options[@]}" "$image" >/dev/null
else
  podman run "${run_options[@]}" "${qemu[@]}" "${entrypoint[@]}" \
    "$image" /siftail serve >/dev/null
fi

ready=false
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$ui_port/health/ready" \
    >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.1
done
if [[ "$ready" != "true" ]]; then
  podman logs "$container_name" >&2
  echo "container did not become ready" >&2
  exit 1
fi
if [[ ${#entrypoint[@]} -eq 0 ]]; then
  podman healthcheck run "$container_name" >/dev/null
else
  podman exec "$container_name" /qemu-aarch64-static \
    /siftail healthcheck >/dev/null
fi

event_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer $token" \
  -H 'Content-Type: application/json' \
  --data-binary \
  "{\"timestamp\":\"$event_time\",\"application\":\"container-smoke\",\"service\":\"api\",\"log\":\"$architecture container smoke event\"}" \
  "http://127.0.0.1:$ingest_port/api/v1/ingest")
if [[ "$http_code" != "204" ]]; then
  echo "container ingestion returned HTTP $http_code" >&2
  exit 1
fi

login_code=$(curl -sS -o /dev/null -w '%{http_code}' -c "$cookie_jar" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -H "Origin: http://127.0.0.1:$ui_port" \
  --data-urlencode 'username=Admin' \
  --data-urlencode "password=$password" \
  --data-urlencode 'return=/logs' \
  "http://127.0.0.1:$ui_port/session")
if [[ "$login_code" != "303" ]]; then
  echo "container browser login returned HTTP $login_code" >&2
  exit 1
fi
curl -fsS -b "$cookie_jar" -L -o "$history_page" \
  "http://127.0.0.1:$ui_port/logs"
grep -q "$architecture container smoke event" "$history_page"

send_shutdown_batch() {
  local prefix=$1
  local destination=$2
  awk -v timestamp="$event_time" -v architecture="$architecture" \
    -v prefix="$prefix" \
    'BEGIN {
      for (i = 0; i < 5000; i++) {
        printf "{\"timestamp\":\"%s\",\"application\":\"container-smoke\",\"service\":\"shutdown\",\"event_id\":\"shutdown-%s-%s-%d\",\"log\":\"queued shutdown %s %d\"}\n", timestamp, architecture, prefix, i, prefix, i
      }
    }' |
    curl -sS -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/x-ndjson' \
      --data-binary @- \
      "http://127.0.0.1:$ingest_port/api/v1/ingest" >"$destination"
}

send_shutdown_batch first "$batch_status" &
first_request_pid=$!
send_shutdown_batch second "$second_batch_status" &
second_request_pid=$!
queued=false
for _ in $(seq 1 1000); do
  if curl -fsS -b "$cookie_jar" \
    "http://127.0.0.1:$ui_port/status" 2>/dev/null |
    grep -Eq '<dt>Queued events</dt><dd>[^0<][^<]*</dd>'; then
    queued=true
    break
  fi
  if ! kill -0 "$first_request_pid" 2>/dev/null &&
    ! kill -0 "$second_request_pid" 2>/dev/null; then
    break
  fi
  sleep 0.01
done
if [[ "$queued" != "true" ]]; then
  wait "$first_request_pid" || true
  wait "$second_request_pid" || true
  echo "container smoke did not observe a queued shutdown batch" >&2
  exit 1
fi
podman stop --time 20 "$container_name" >/dev/null
if ! wait "$first_request_pid" || ! wait "$second_request_pid"; then
  echo "a queued shutdown request lost its HTTP outcome" >&2
  exit 1
fi
for status_file in "$batch_status" "$second_batch_status"; do
  shutdown_status=$(tr -d '\r\n' <"$status_file")
  if [[ "$shutdown_status" != "204" ]]; then
    echo "queued shutdown request returned HTTP $shutdown_status" >&2
    exit 1
  fi
done

podman start "$container_name" >/dev/null
ready=false
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$ui_port/health/ready" \
    >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.1
done
if [[ "$ready" != "true" ]]; then
  podman logs "$container_name" >&2
  echo "container did not become ready after restart" >&2
  exit 1
fi
curl -fsS -b "$cookie_jar" -L -o "$history_page" \
  "http://127.0.0.1:$ui_port/logs"
grep -q 'queued shutdown' "$history_page"

runtime_identity=$(podman top "$container_name" user huser | tail -n 1)
container_user=$(awk '{print $1}' <<<"$runtime_identity")
host_user=$(awk '{print $2}' <<<"$runtime_identity")
if [[ "$container_user" != "nonroot" || "$host_user" == "0" ]]; then
  echo "unexpected runtime process identity: $runtime_identity" >&2
  exit 1
fi

podman stop --time 20 "$container_name" >/dev/null
echo "$architecture container smoke passed"
