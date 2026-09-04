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
        echo "WB hook: push-tier classifier exited $tier unexpectedly; running go vet as a safe default." >&2
        tier=2
        ;;
esac
if [ "$tier" -eq 0 ]; then
    exit 0
fi

if "$WB_EXECUTABLE" run --help 2>/dev/null | grep -q 'run -- <command>'; then
    "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" run -- go vet ./...
else
    # One-release bootstrap for the installed WB that is pushing this change.
    go vet ./...
fi
