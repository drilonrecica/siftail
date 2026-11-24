#!/usr/bin/env bash
# Measure a Siftail production container image: idle and loaded RSS, 1,000
# events/s sustained and 5,000 events/s burst ingestion with commit latency,
# browser readers and Live streams, queue saturation, database growth,
# backup, size retention, and the 100,000/1,000,000/10,000,000-event query
# gate. docs/performance/0.5.0-container.md records the method and results.
#
# Usage:  scripts/container-bench.sh <image> [--quick] [--keep]
# Env:    TMPDIR  directory on a real disk (default: ./.bench-tmp). The full
#                 run needs about 20 GB free and takes about an hour.
#         OUT     JSON summary path (default: $TMPDIR/container-bench.json)
#
# The image must be linux/amd64 or match the host; it is run with podman,
# without memory or CPU limits, on ports 28180 (UI) and 28181 (ingestion).
set -euo pipefail

cd "$(dirname "$0")/.."

if [ "$#" -lt 1 ]; then
	echo "usage: scripts/container-bench.sh <image> [--quick] [--keep]" >&2
	exit 2
fi
image="$1"
shift
flags=()
for argument in "$@"; do
	case "$argument" in
	--quick) flags+=(-quick) ;;
	--keep) flags+=(-keep) ;;
	*)
		echo "container-bench: unknown option $argument" >&2
		exit 2
		;;
	esac
done

TMPDIR="${TMPDIR:-$PWD/.bench-tmp}"
case "$TMPDIR" in
/*) ;;
*) TMPDIR="$PWD/$TMPDIR" ;;
esac
mkdir -p "$TMPDIR"
export TMPDIR

# AGENTS.md section 17.2: throughput, durability, retention, and backup
# numbers only mean something when fsync reaches a real disk.
fstype="$(stat -f -c %T "$TMPDIR")"
case "$fstype" in
tmpfs | ramfs | devtmpfs | overlayfs | overlay)
	echo "container-bench: TMPDIR=$TMPDIR is on $fstype; set TMPDIR to a directory on a real disk (AGENTS.md section 17.2)" >&2
	exit 1
	;;
esac

source_dev="$(df --output=source "$TMPDIR" | tail -n 1)"
dev_name="$(basename "$(readlink -f "$source_dev" 2>/dev/null || echo "$source_dev")")"
parent="$dev_name"
if [ -e "/sys/class/block/$dev_name" ]; then
	parent="$(basename "$(dirname "$(readlink -f "/sys/class/block/$dev_name")")")"
	[ "$parent" = block ] && parent="$dev_name"
fi
rotational="$(cat "/sys/block/$parent/queue/rotational" 2>/dev/null || echo unknown)"
model="$(sed 's/ *$//' "/sys/block/$parent/device/model" 2>/dev/null || true)"

echo "== environment"
echo "date:        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "kernel:      $(uname -sr)"
echo "cpu:         $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
echo "podman:      $(podman version --format '{{.Client.Version}}')"
echo "image:       $image $(podman image inspect "$image" --format '{{.Id}} {{.Architecture}} {{.Size}} bytes')"
echo "TMPDIR:      $TMPDIR"
echo "filesystem:  $fstype on $source_dev (${model:-model unknown}, rotational=$rotational)"
echo

workdir="$TMPDIR/container-bench"
mkdir -p "$workdir"
out="${OUT:-$TMPDIR/container-bench.json}"
go run ./tests/containerbench -image "$image" -workdir "$workdir" -out "$out" "${flags[@]}"
echo "summary: $out"
