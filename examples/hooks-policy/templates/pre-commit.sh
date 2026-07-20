#!/bin/sh
set -eu

# Keep pre-commit feedback cheap and deterministic.
git diff --cached --check
