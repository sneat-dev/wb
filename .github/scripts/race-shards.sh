#!/bin/sh
# Prints the package import paths for one race.yml shard, one per line:
# orchestrate, worktrees, or rest (every package but those two). This is the
# single source of truth for shard membership: race.yml's three race-test
# jobs and its race-shards-cover-all-packages guard job all call this script
# for their package list, so a shard's selection and the guard that checks it
# can never drift apart from each other (issue #728 review: a guard that
# recomputes its own copy of the shard lists can never actually fail).
#
# "rest"'s exclusion is anchored with (/|$) rather than just $, so a future
# subpackage of internal/orchestrate or internal/worktrees (there are none
# today) is excluded from rest rather than silently running in two shards.
set -eu

shard=${1:?shard name is required (orchestrate, worktrees, or rest)}

case "$shard" in
  orchestrate)
    go list ./internal/orchestrate/...
    ;;
  worktrees)
    go list ./internal/worktrees/...
    ;;
  rest)
    go list ./... | grep -v -E '^github\.com/sneat-dev/wb/internal/(orchestrate|worktrees)(/|$)'
    ;;
  *)
    echo "race-shards.sh: unknown shard $shard, want orchestrate, worktrees, or rest" >&2
    exit 1
    ;;
esac
