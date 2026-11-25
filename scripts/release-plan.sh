#!/usr/bin/env bash
# Decides what one release run builds and where it may publish.
#
#   scripts/release-plan.sh tag <git-tag> <commit>       # vX.Y.Z or vX.Y.Z-rc.N
#   scripts/release-plan.sh edge <commit>                # successful master build
#   scripts/release-plan.sh dry-run <version|edge> <commit>
#
# Prints key=value lines for $GITHUB_OUTPUT. The channel rules are in
# docs/release/process.md: stable tags move latest only when they are the
# highest stable version, candidates never move a floating tag, edge never
# carries a version number, and a dry run never publishes.
#
# RELEASE_BRANCH_REF (default origin/master) names the reviewed branch every
# candidate and stable commit must already be on.
set -euo pipefail

image=ghcr.io/drilonrecica/siftail
branch_ref=${RELEASE_BRANCH_REF:-origin/master}
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.([1-9][0-9]*))?$'

fail() {
  echo "release-plan: $*" >&2
  exit 1
}

usage() {
  echo "usage: scripts/release-plan.sh tag <vX.Y.Z[-rc.N]> <commit>" >&2
  echo "       scripts/release-plan.sh edge <commit>" >&2
  echo "       scripts/release-plan.sh dry-run <X.Y.Z[-rc.N]|edge> <commit>" >&2
  exit 2
}

[[ $# -ge 2 ]] || usage
mode=$1
case "$mode" in
  tag|dry-run)
    [[ $# -eq 3 ]] || usage
    requested=$2
    commit_ref=$3
    ;;
  edge)
    [[ $# -eq 2 ]] || usage
    requested=edge
    commit_ref=$2
    ;;
  *) usage ;;
esac

commit=$(git rev-parse --verify --quiet "${commit_ref}^{commit}") ||
  fail "unknown commit: $commit_ref"
short=$(git rev-parse --short=12 "$commit")

if [[ "$mode" == "tag" ]]; then
  [[ "$requested" == v* ]] || fail "release tags start with v: $requested"
  version=${requested#v}
  [[ "$version" =~ $semver ]] ||
    fail "release tags are vX.Y.Z or vX.Y.Z-rc.N: $requested"
  tagged=$(git rev-parse --verify --quiet "refs/tags/${requested}^{commit}") ||
    fail "tag does not exist locally: $requested"
  [[ "$tagged" == "$commit" ]] ||
    fail "tag $requested points at $tagged, not $commit"
else
  version=$requested
fi

publish=false
[[ "$mode" != "dry-run" ]] && publish=true

if [[ "$version" == "edge" ]]; then
  # Edge is the development channel (AGENTS.md section 29). Its version names
  # the commit so `siftail version` never looks like a release.
  version="edge-$short"
  channel=edge
  tags="edge edge-$short"
  latest=false
  prerelease=true
  notes=
else
  [[ "$version" =~ $semver ]] || fail "not a release version: $version"
  major=${BASH_REMATCH[1]}
  minor=${BASH_REMATCH[2]}
  release=${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}
  notes="docs/release/notes/$release.md"

  # Candidates and stable releases come only from the reviewed branch.
  git rev-parse --verify --quiet "$branch_ref" >/dev/null ||
    fail "reviewed branch $branch_ref is not available; fetch it first"
  git merge-base --is-ancestor "$commit" "$branch_ref" ||
    fail "$short is not on $branch_ref; release only reviewed commits"
  git cat-file -e "$commit:$notes" 2>/dev/null ||
    fail "$notes does not exist at $short"

  if [[ -n "${BASH_REMATCH[4]}" ]]; then
    channel=candidate
    tags=$version
    latest=false
    prerelease=true
  else
    channel=stable
    prerelease=false
    tags="$version $major.$minor"
    # Before 1.0 a minor release may break compatibility, so a bare major
    # tag such as `0` would promise nothing. It starts at 1.0.0.
    if [[ "$major" != "0" ]]; then
      tags="$tags $major"
    fi
    # latest is the highest stable version, never a patch to an older line.
    highest=$( {
      git tag --list 'v*' | sed -n 's/^v//p' |
        grep -E '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || true
      echo "$version"
    } | sort -V | tail -n 1)
    if [[ "$highest" == "$version" ]]; then
      latest=true
      tags="$tags latest"
    else
      latest=false
    fi
  fi
fi

echo "version=$version"
echo "channel=$channel"
echo "commit=$commit"
echo "short=$short"
echo "image=$image"
echo "tags=$tags"
echo "latest=$latest"
echo "prerelease=$prerelease"
echo "publish=$publish"
echo "notes=$notes"
