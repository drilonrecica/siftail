#!/usr/bin/env bash
# Checks a built OCI release archive against the image contract in
# docs/operations/container-image.md before anything is smoke-tested or pushed.
#
#   scripts/release-inspect.sh <siftail-VERSION.oci.tar> <version> <commit-short>
#
# It reads the archive itself, so it inspects exactly the bytes that are
# published, including the health check that some image formats drop.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: scripts/release-inspect.sh <archive> <version> <commit-short>" >&2
  exit 2
fi
archive=$1
version=$2
short=$3

layout=$(mktemp -d "${TMPDIR:-/tmp}/siftail-inspect.XXXXXX")
trap 'rm -rf "$layout"' EXIT
tar -xf "$archive" -C "$layout"
blob() { cat "$layout/blobs/${1/://}"; }

failures=0
check() {
  local description=$1 expected=$2 actual=$3
  if [[ "$expected" == "$actual" ]]; then
    echo "ok   $description"
  else
    echo "FAIL $description: expected $expected, got $actual"
    failures=$((failures + 1))
  fi
}

[[ "$(jq '.manifests | length' "$layout/index.json")" == 1 ]] ||
  { echo "FAIL archive must hold exactly one image index"; exit 1; }
index=$(jq -r '.manifests[0].digest' "$layout/index.json")
echo "image index $index"

# Exactly the two supported platforms, and no attestation or unknown entries:
# provenance is attached by the release workflow, not embedded by BuildKit.
check "index platforms" "linux/amd64 linux/arm64" \
  "$(blob "$index" | jq -r '[.manifests[] | "\(.platform.os)/\(.platform.architecture)"] | sort | join(" ")')"

for arch in amd64 arm64; do
  manifest=$(blob "$index" | jq -r --arg arch "$arch" \
    '.manifests[] | select(.platform.architecture == $arch) | .digest')
  config=$(blob "$(blob "$manifest" | jq -r '.config.digest')")
  c() { jq -c "$1" <<<"$config"; }
  echo "linux/$arch manifest $manifest"
  check "$arch architecture" '"'"$arch"'"' "$(c .architecture)"
  check "$arch user" '"65532:65532"' "$(c .config.User)"
  check "$arch entrypoint" '["/siftail"]' "$(c .config.Entrypoint)"
  check "$arch command" '["serve"]' "$(c .config.Cmd)"
  check "$arch ports" '["8080/tcp","8081/tcp"]' "$(c '.config.ExposedPorts | keys')"
  check "$arch volumes" '["/data"]' "$(c '.config.Volumes | keys')"
  check "$arch health check" '["CMD","/siftail","healthcheck"]' "$(c .config.Healthcheck.Test)"
  check "$arch version label" '"'"$version"'"' "$(c '.config.Labels["org.opencontainers.image.version"]')"
  check "$arch revision label" '"'"$short"'"' "$(c '.config.Labels["org.opencontainers.image.revision"]')"
  check "$arch license label" '"Apache-2.0"' "$(c '.config.Labels["org.opencontainers.image.licenses"]')"
  check "$arch no environment secrets" 'false' \
    "$(c '[.config.Env[]? | test("PASSWORD|TOKEN|SECRET"; "i")] | any')"
done

if [[ $failures -ne 0 ]]; then
  echo "release-inspect: $failures check(s) failed" >&2
  exit 1
fi
echo "release-inspect: archive matches the image contract"
