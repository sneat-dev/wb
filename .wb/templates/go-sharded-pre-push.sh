#!/bin/sh
set -eu

# `wb hooks lint` classifies the push (the push-tier contract), then runs go
# vet and the CI-pinned golangci-lint over every Go module the pushed commits
# touch, each through `wb run --`.
if "$WB_EXECUTABLE" hooks lint --help 2>/dev/null | grep -q -- "--language"; then
    exec "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" hooks lint --hook pre-push --language go
fi

# One-release bootstrap: an installed WB predating `wb hooks lint` must still
# be able to push the release that introduces it.
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
"$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" run -- go vet ./...
