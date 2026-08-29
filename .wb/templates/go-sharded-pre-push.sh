#!/bin/sh
set -eu

tier=2
set +e
"$WB_EXECUTABLE" hooks push-tier
tier=$?
set -e
case "$tier" in
    0|1|2) ;;
    *)
        echo "WB hook: push-tier classifier exited $tier unexpectedly; running the full tier (vet + sharded coverage) as a safe default." >&2
        tier=2
        ;;
esac
if [ "$tier" -eq 0 ]; then
    exit 0
fi

go vet ./...
if [ "$tier" -eq 2 ]; then
    # Keep the observer-facing hook output bounded. The complete coverage
    # report is durable and resumable; only its short summary crosses the
    # agent/session notification boundary.
    metrics_path="${WB_HOOK_METRICS_PATH:-.wb/hook-events.jsonl}"
    report_root="${WB_HOOK_REPORT_ROOT:-${metrics_path%/*}/reports}"
    report_dir="${WB_HOOK_REPORT_DIR:-$report_root/coverage-$$}"
    umask 077
    mkdir -p "$report_dir"
    go run ./cmd/wb coverage . \
        --test-shards 8 \
        --shard-package ./internal/worktrees \
        --minimum 58 \
        --format summary \
        --report-dir "$report_dir" \
        --non-interactive
fi
