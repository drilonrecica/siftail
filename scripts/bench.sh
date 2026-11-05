#!/usr/bin/env bash
# Run the benchmarks recorded under docs/performance with TMPDIR on a real disk.
#
# AGENTS.md section 17.2: durability, retention, backup, and throughput numbers
# only mean something when fsync is real. This script refuses a RAM-backed
# TMPDIR, prints the filesystem and device class to record beside the results,
# and runs each documented benchmark with the flags its document uses.
#
# Usage:  scripts/bench.sh [group...]
#   groups: ingest logs live sources retention audit backup database group-commit
#   default: every group except group-commit (it runs for many minutes);
#   name it explicitly, or pass "all", to include it.
# Env:    TMPDIR       directory on a real disk (default: ./.bench-tmp)
#         BENCH_COUNT  -count for the repeated groups (default: 5)
set -euo pipefail

cd "$(dirname "$0")/.."

TMPDIR="${TMPDIR:-$PWD/.bench-tmp}"
case "$TMPDIR" in
/*) ;;
*) TMPDIR="$PWD/$TMPDIR" ;;
esac
mkdir -p "$TMPDIR"
export TMPDIR

fstype="$(stat -f -c %T "$TMPDIR")"
case "$fstype" in
tmpfs | ramfs | devtmpfs | overlayfs | overlay)
	echo "bench: TMPDIR=$TMPDIR is on $fstype; set TMPDIR to a directory on a real disk (AGENTS.md section 17.2)" >&2
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
case "$rotational" in
0) class="non-rotational (SSD/NVMe)" ;;
1) class="rotational (HDD)" ;;
*) class="unknown (not a local block device; record it by hand)" ;;
esac
model="$(cat "/sys/block/$parent/device/model" 2>/dev/null | sed 's/ *$//' || true)"

echo "== environment"
echo "date:        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "kernel:      $(uname -sr)"
echo "cpu:         $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //')"
echo "go:          $(go version)"
echo "sqlite:      $(go list -m -f '{{.Path}} {{.Version}}' github.com/mattn/go-sqlite3)"
echo "TMPDIR:      $TMPDIR"
echo "filesystem:  $fstype on $source_dev (${model:-model unknown})"
echo "device:      $class"
echo

count="${BENCH_COUNT:-5}"
run() {
	echo "== go test $*"
	go test -run '^$' -benchmem "$@"
}

group() {
	case "$1" in
	ingest) # docs/performance/0.1.0.md and ingest-hot-path.md
		run -count="$count" -benchtime=2s \
			-bench 'Benchmark(Normalize|DecodeBatch100|QueueAdmission|BatchWriterSQLiteCommit|BatchWriterSourceResolution|BatchWriterIdempotentRetry|BatchWriter100Events|HTTPCommitLatency|HTTPSustained100EventBatch)$' \
			./internal/logs ./internal/ingest
		run -count=1 -benchtime=1x -bench '^BenchmarkQueueSaturationRSS$' ./internal/ingest
		;;
	group-commit) # docs/performance/group-commit.md
		run -count=3 -benchtime=1000x -timeout 60m -bench '^BenchmarkGroupCommitConcurrent$' ./internal/ingest
		;;
	logs) # docs/performance/history-queries.md
		run -count=1 -benchtime=5x -bench '^BenchmarkHistoryStore(100K|1M)$' ./internal/logs
		;;
	live) # docs/performance/live.md
		run -count="$count" -benchtime=3s -bench '^BenchmarkLive(BrokerPublish16Subscribers|SSEFrame)$' ./internal/logs ./internal/auth
		;;
	sources) # docs/performance/source-catalog.md
		run -count="$count" -benchtime=3s -bench '^BenchmarkSourceCatalog10000$' ./internal/sources
		;;
	retention) # docs/performance/retention.md
		run -count="$count" -benchtime=1x -bench '^BenchmarkRetention' ./internal/retention
		;;
	audit)
		run -count="$count" -bench '^BenchmarkAudit' ./internal/audit
		;;
	backup)
		run -count=3 -benchtime=1x -bench '^BenchmarkCreate' ./internal/backup
		;;
	database)
		run -count=3 -bench 'Benchmark(ProbeWritable|StoppedDatabaseCheck)$' ./internal/database
		;;
	*)
		echo "bench: unknown group '$1'" >&2
		exit 2
		;;
	esac
}

if [ "$#" -eq 0 ]; then
	set -- ingest logs live sources retention audit backup database
elif [ "$1" = all ]; then
	set -- ingest group-commit logs live sources retention audit backup database
fi
for g in "$@"; do
	group "$g"
done
