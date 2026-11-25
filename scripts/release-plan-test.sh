#!/usr/bin/env bash
# Exercises the channel rules of scripts/release-plan.sh against a throwaway
# repository: which tags each release gets, when latest moves, and which
# commits and versions are refused.
set -euo pipefail

plan=$(cd "$(dirname "$0")" && pwd)/release-plan.sh
repo=$(mktemp -d "${TMPDIR:-/tmp}/siftail-release-plan.XXXXXX")
trap 'rm -rf "$repo"' EXIT
cd "$repo"
export RELEASE_BRANCH_REF=master GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

g() { git -c user.name=test -c user.email=test@example.invalid "$@" >/dev/null; }
commit() { echo "$1" >>history; git add -A; g commit -q -m "$1"; git rev-parse HEAD; }

g init -q -b master
mkdir -p docs/release/notes
for v in 0.4.0 0.5.0 0.5.1 1.0.0 2.0.0; do echo "# $v" >"docs/release/notes/$v.md"; done
reviewed=$(commit reviewed)
g tag v0.4.0 "$reviewed"
g tag v0.5.0-rc.1 "$reviewed"
g tag v0.5.0 "$reviewed"
g tag v1.0.0 "$reviewed"
g checkout -q -b feature
unreviewed=$(commit unreviewed)
g tag v0.5.1 "$unreviewed"
g checkout -q master
g tag v0.4.1 "$reviewed"
git rm -q docs/release/notes/0.5.1.md
g commit -q -m "drop notes"
no_notes=$(git rev-parse HEAD)
g tag v0.5.2 "$no_notes"
g tag release-0.5.0 "$reviewed"
g tag v0.5.0-beta.1 "$reviewed"

failures=0
expect() {
  local name=$1 want=$2
  shift 2
  local got
  if ! got=$("$plan" "$@" 2>&1); then
    echo "FAIL $name: refused: $got"
    failures=$((failures + 1))
    return
  fi
  local line
  for line in $want; do
    if ! grep -qx -- "${line//+/ }" <<<"$got"; then
      echo "FAIL $name: want ${line//+/ }, got:"
      sed 's/^/     /' <<<"$got"
      failures=$((failures + 1))
      return
    fi
  done
  echo "ok   $name"
}
refuse() {
  local name=$1 reason=$2
  shift 2
  local got
  if got=$("$plan" "$@" 2>&1); then
    echo "FAIL $name: accepted"
    failures=$((failures + 1))
  elif grep -q -- "$reason" <<<"$got"; then
    echo "ok   $name"
  else
    echo "FAIL $name: refused for another reason: $got"
    failures=$((failures + 1))
  fi
}

# A `+` stands for a space inside an expected line.
expect "highest 1.x stable gets major, minor, latest" \
  "channel=stable tags=1.0.0+1.0+1+latest latest=true publish=true prerelease=false notes=docs/release/notes/1.0.0.md" \
  tag v1.0.0 "$reviewed"
expect "older stable line keeps latest where it is" \
  "channel=stable tags=0.5.0+0.5 latest=false" \
  tag v0.5.0 "$reviewed"
expect "candidate moves no floating tag" \
  "channel=candidate version=0.5.0-rc.1 tags=0.5.0-rc.1 latest=false prerelease=true publish=true" \
  tag v0.5.0-rc.1 "$reviewed"
expect "edge carries no release version" \
  "channel=edge version=edge-${reviewed:0:12} tags=edge+edge-${reviewed:0:12} latest=false publish=true notes=" \
  edge "$reviewed"
expect "dry run never publishes" \
  "channel=stable publish=false" \
  dry-run 0.5.0 "$reviewed"
expect "dry run of an unreleased highest version would move latest" \
  "tags=2.0.0+2.0+2+latest" \
  dry-run 2.0.0 "$reviewed"
expect "dry run of edge" "channel=edge publish=false" dry-run edge "$unreviewed"

refuse "unreviewed commit" "not on master" tag v0.5.1 "$unreviewed"
refuse "missing release notes" "does not exist" tag v0.5.2 "$no_notes"
refuse "tag without v" "start with v" tag release-0.5.0 "$reviewed"
refuse "other pre-release forms" "vX.Y.Z or vX.Y.Z-rc.N" tag v0.5.0-beta.1 "$reviewed"
refuse "tag on another commit" "points at" tag v0.5.0 "$no_notes"
refuse "unknown tag" "does not exist locally" tag v9.9.9 "$reviewed"
refuse "dry run of a non-version" "not a release version" dry-run latest "$reviewed"
refuse "leading zero" "not a release version" dry-run 0.05.0 "$reviewed"

if [[ $failures -ne 0 ]]; then
  echo "release-plan-test: $failures case(s) failed" >&2
  exit 1
fi
echo "release-plan-test: all cases passed"
