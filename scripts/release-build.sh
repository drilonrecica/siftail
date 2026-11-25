#!/usr/bin/env bash
# Builds the release artifacts for one commit without publishing anything.
#
#   scripts/release-build.sh <version> <commit> <output-directory>
#
# The build context is `git archive <commit>`, never the working tree, and
# every timestamp comes from the commit time, so two runs of the same inputs
# produce the same image index digest (docs/release/process.md). Writes:
#
#   siftail-<version>.oci.tar              multi-architecture OCI image archive
#   siftail-<version>-linux-<arch>.cdx.json CycloneDX SBOM per platform
#   siftail-<version>.release.json          digests, inputs, binary checksums
#   SHA256SUMS                              checksums of the files above
#
# RELEASE_BUILDER selects a buildx builder (the workflow pins its BuildKit
# image). SYFT names the syft binary when it is not on PATH.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: scripts/release-build.sh <version> <commit> <output-directory>" >&2
  exit 2
fi

version=$1
commit_ref=$2
out=$3
image=ghcr.io/drilonrecica/siftail
platforms=(amd64 arm64)
syft=${SYFT:-syft}

for command in git docker tar jq sha256sum skopeo "$syft"; do
  command -v "$command" >/dev/null ||
    { echo "release-build: missing command: $command" >&2; exit 1; }
done
[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z.-]*$ ]] ||
  { echo "release-build: invalid version: $version" >&2; exit 1; }

commit=$(git rev-parse --verify "${commit_ref}^{commit}")
short=$(git rev-parse --short=12 "$commit")
epoch=$(git show -s --format=%ct "$commit")
build_date=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)

mkdir -p "$out"
out=$(cd "$out" && pwd)
if [[ -n "$(ls -A "$out")" ]]; then
  echo "release-build: output directory is not empty: $out" >&2
  exit 1
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/siftail-release.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir "$work/context"
git archive --format=tar "$commit" | tar -x -C "$work/context"

archive="$out/siftail-$version.oci.tar"
builder=()
[[ -n "${RELEASE_BUILDER:-}" ]] && builder=(--builder "$RELEASE_BUILDER")

echo "release-build: building $version from $short (SOURCE_DATE_EPOCH=$epoch)"
SOURCE_DATE_EPOCH=$epoch docker buildx build "${builder[@]}" \
  --platform linux/amd64,linux/arm64 \
  --no-cache --pull \
  --provenance=false --sbom=false \
  --build-arg "VERSION=$version" \
  --build-arg "COMMIT=$short" \
  --build-arg "BUILD_DATE=$build_date" \
  --build-arg "SOURCE_DATE_EPOCH=$epoch" \
  --output "type=oci,dest=$archive,rewrite-timestamp=true" \
  "$work/context"

mkdir "$work/layout"
tar -xf "$archive" -C "$work/layout"
blob() { cat "$work/layout/blobs/${1/://}"; }

index_digest=$(jq -r '.manifests[0].digest' "$work/layout/index.json")
platforms_json='[]'
for arch in "${platforms[@]}"; do
  manifest_digest=$(blob "$index_digest" | jq -r --arg arch "$arch" \
    '.manifests[] | select(.platform.os == "linux" and .platform.architecture == $arch) | .digest')
  [[ -n "$manifest_digest" ]] ||
    { echo "release-build: index has no linux/$arch manifest" >&2; exit 1; }
  config_digest=$(blob "$manifest_digest" | jq -r '.config.digest')

  # The binary checksum lets anyone compare an independent rebuild without
  # trusting the image metadata.
  binary_sha=
  while read -r layer; do
    if blob "$layer" | tar -tzf - 2>/dev/null | grep -qx 'siftail'; then
      binary_sha=$(blob "$layer" | tar -xzOf - siftail | sha256sum | cut -d' ' -f1)
      break
    fi
  done < <(blob "$manifest_digest" | jq -r '.layers[].digest')
  [[ -n "$binary_sha" ]] ||
    { echo "release-build: no /siftail in linux/$arch" >&2; exit 1; }

  skopeo copy --quiet --override-os linux --override-arch "$arch" \
    "oci-archive:$archive" "docker-archive:$work/siftail-$arch.tar"
  "$syft" scan --quiet "docker-archive:$work/siftail-$arch.tar" \
    --source-name "$image" --source-version "$version" \
    --output "cyclonedx-json=$out/siftail-$version-linux-$arch.cdx.json"

  platforms_json=$(jq --arg arch "$arch" --arg manifest "$manifest_digest" \
    --arg config "$config_digest" --arg binary "$binary_sha" \
    '. + [{platform: ("linux/" + $arch), manifest: $manifest, config: $config, binary_sha256: $binary}]' \
    <<<"$platforms_json")
done

base_images=$(sed -n 's/^ARG \(GO_IMAGE\|RUNTIME_IMAGE\)=//p' "$work/context/Dockerfile" |
  jq -R . | jq -s .)
jq -n \
  --arg version "$version" --arg commit "$commit" --arg image "$image" \
  --arg index "$index_digest" --argjson epoch "$epoch" \
  --arg build_date "$build_date" --argjson platforms "$platforms_json" \
  --argjson base_images "$base_images" \
  --arg buildx "$(docker buildx version)" \
  --arg buildkit "$(docker buildx inspect "${builder[@]:1}" | sed -n 's/^BuildKit version: *//p' | head -n 1)" \
  '{version: $version, commit: $commit, image: $image, index_digest: $index,
    source_date_epoch: $epoch, build_date: $build_date,
    platforms: $platforms, base_images: $base_images,
    buildx: $buildx, buildkit: $buildkit}' \
  >"$out/siftail-$version.release.json"

(cd "$out" && sha256sum -- siftail-* >SHA256SUMS)
echo "release-build: image index $index_digest"
cat "$out/SHA256SUMS"
