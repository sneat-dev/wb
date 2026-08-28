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
    go run ./cmd/wb coverage . \
        --test-shards 8 \
        --shard-package ./internal/worktrees \
        --minimum 58 \
        --format json \
        --non-interactive
fi
