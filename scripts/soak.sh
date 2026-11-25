#!/usr/bin/env bash
# Run the bounded production soak (SFT-055, ARCHITECTURE.md section 36)
# against a Siftail production container image: continuous mixed ingestion
# with bursts, History readers, Live clients including a slow one, size
# retention, online backups, graceful restarts, token rotation, session
# revocation, source transitions, queue saturation, and a controlled
# degraded database, then offline verification of every acknowledged batch,
# integrity checks, and a restore drill. docs/performance/0.5.0-soak.md
# records the procedure and results.
#
# Usage:  scripts/soak.sh <image> [--quick] [--duration 3h] [--keep]
# Env:    TMPDIR  directory on a real disk (default: ./.bench-tmp). The
#                 default three-hour run needs about 15 GB free.
#         OUT     JSON summary path (default: $TMPDIR/soak.json)
#         SEED    workload seed (default 55)
#
# The image must be linux/amd64 or match the host; it runs with podman,
# without memory or CPU limits, on ports 28280/28281 (and 28282/28283 for
# the restore drill). Cleanup removes both containers and every data
# directory unless --keep is given.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ "$#" -lt 1 ]; then
	echo "usage: scripts/soak.sh <image> [--quick] [--duration 3h] [--keep]" >&2
	exit 2
fi
image="$1"
shift
flags=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--quick) flags+=(-quick) ;;
	--keep) flags+=(-keep) ;;
	--duration)
		[ "$#" -ge 2 ] || { echo "soak: --duration needs a value" >&2; exit 2; }
		flags+=(-duration "$2")
		shift
		;;
	*)
		echo "soak: unknown option $1" >&2
		exit 2
		;;
	esac
	shift
done

TMPDIR="${TMPDIR:-$PWD/.bench-tmp}"
case "$TMPDIR" in
/*) ;;
*) TMPDIR="$PWD/$TMPDIR" ;;
esac
mkdir -p "$TMPDIR"
export TMPDIR

# AGENTS.md section 17.2: durability, retention, and backup behavior only
# means something when fsync reaches a real disk.
fstype="$(stat -f -c %T "$TMPDIR")"
case "$fstype" in
tmpfs | ramfs | devtmpfs | overlayfs | overlay)
	echo "soak: TMPDIR=$TMPDIR is on $fstype; set TMPDIR to a directory on a real disk (AGENTS.md section 17.2)" >&2
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
echo "cpu:         $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //') ($(nproc) cores)"
echo "load:        $(cut -d' ' -f1-3 /proc/loadavg)"
echo "podman:      $(podman version --format '{{.Client.Version}}')"
echo "image:       $image $(podman image inspect "$image" --format '{{.Id}} {{.Architecture}} {{.Size}} bytes')"
echo "TMPDIR:      $TMPDIR"
echo "filesystem:  $fstype on $source_dev (${model:-model unknown}, rotational=$rotational)"
echo

workdir="$TMPDIR/soak"
mkdir -p "$workdir"
out="${OUT:-$TMPDIR/soak.json}"
# Build first: the degraded-database scenario re-runs this binary inside
# podman's user namespace to hold a SQLite write lock.
bin="$workdir/containerbench"
go build -o "$bin" ./tests/containerbench
status=0
"$bin" -soak -image "$image" -workdir "$workdir" -out "$out" \
	-ui-port 28280 -ingest-port 28281 -seed "${SEED:-55}" "${flags[@]}" || status=$?
rm -f "$bin"
echo "summary: $out"
exit "$status"
