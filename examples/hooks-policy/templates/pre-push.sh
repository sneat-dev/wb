#!/bin/sh
set -eu

git diff --check

# Adapt this template to the repository. Pre-push is the right place for the
# stronger checks that are too slow for every commit.
if [ -f go.mod ]; then
    go test ./...
fi
