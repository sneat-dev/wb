#!/usr/bin/env bash
set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/go-scope.sh
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
cd "$repo"
git init -q -b main
git config user.name 'CI scope test'
git config user.email 'ci-scope@example.invalid'
touch tracked.txt
git add tracked.txt
git commit -qm initial
base=$(git rev-parse HEAD)

assert_scope() {
  local event=$1 before=$2 after=$3 ref=$4 want=$5
  local got
  got=$(bash "$script" "$event" "$before" "$after" "$ref")
  if [[ $got != "$want" ]]; then
    printf 'scope for %s %s..%s: got <%s>, want <%s>\n' "$event" "$before" "$after" "$got" "$want" >&2
    exit 1
  fi
}

commit_paths() {
  local path
  git checkout -q -B case "$base"
  for path in "$@"; do
    mkdir -p "$(dirname "$path")"
    touch "$path"
  done
  git add .
  git commit -qm case
  git rev-parse HEAD
}

docs=$(commit_paths internal/worktrees/README.md internal/worktrees/DOMAIN_MAP.md)
assert_scope pull_request "$base" "$docs" refs/pull/1/merge $'required=false\ncontract_required=false'
assert_scope push "$base" "$docs" refs/heads/main $'required=false\ncontract_required=false'
example_docs=$(commit_paths examples/README.md)
assert_scope pull_request "$base" "$example_docs" refs/pull/1/merge $'required=false\ncontract_required=false'

tsv=$(commit_paths internal/worktrees/domain_map.tsv)
assert_scope pull_request "$base" "$tsv" refs/pull/1/merge $'required=false\ncontract_required=false'

for path in internal/worktrees/retire.go internal/worktrees/retire.s \
  internal/worktrees/retire.syso internal/worktrees/retire.c testdata/root.json \
  go.mod go.sum go.work go.work.sum \
  .github/workflows/go-ci.yml .github/scripts/ci-reuse-select.sh \
  .wb/quality.yaml .wb/hooks.yaml .wb/templates/go-sharded-pre-push.sh \
  examples/migrations/dalgo-record-v1.hcl proto/wb/daemon/v1/daemon.proto \
  ai/skills/wb/SKILL.md hub/web/dist/.gitkeep hub/web/src/pages/dashboard.astro \
  hub/web/package.json; do
  head=$(commit_paths "$path")
  assert_scope pull_request "$base" "$head" refs/pull/1/merge $'required=true\ncontract_required=false'
done

mixed=$(commit_paths internal/worktrees/README.md internal/worktrees/retire.go)
assert_scope pull_request "$base" "$mixed" refs/pull/1/merge $'required=true\ncontract_required=false'

contract=$(commit_paths spec/features/example/README.md)
assert_scope pull_request "$base" "$contract" refs/pull/1/merge $'required=false\ncontract_required=true'
for path in .github/agents/wb-merger.agent.md ai/README.md \
  internal/secretscan/gitleaks/LICENSE internal/secretscan/gitleaks/PROVENANCE.md; do
  contract=$(commit_paths "$path")
  assert_scope pull_request "$base" "$contract" refs/pull/1/merge $'required=false\ncontract_required=true'
  assert_scope push "$base" "$contract" refs/heads/main $'required=false\ncontract_required=true'
done

# The target has advanced with Go code since the feature forked. A PR still
# compares its head to the merge base, so it sees only the feature's docs.
git checkout -q main
mkdir -p internal/worktrees
touch internal/worktrees/new.go
git add .
git commit -qm target-advanced
target=$(git rev-parse HEAD)
assert_scope pull_request "$target" "$docs" refs/pull/1/merge $'required=false\ncontract_required=false'

# A main push uses before..after, not the absent pull_request.base.sha.
git checkout -q main
touch internal/worktrees/pushed.md
git add .
git commit -qm docs-push
pushed=$(git rev-parse HEAD)
assert_scope push "$target" "$pushed" refs/heads/main $'required=false\ncontract_required=false'

# Rename detection must not hide removal of a Go input behind a prose name.
touch internal/worktrees/removed.go
git add .
git commit -qm go-before-rename
rename_base=$(git rev-parse HEAD)
git mv internal/worktrees/removed.go internal/worktrees/removed.txt
git commit -qm rename-go-away
renamed=$(git rev-parse HEAD)
assert_scope pull_request "$rename_base" "$renamed" refs/pull/1/merge $'required=true\ncontract_required=false'
assert_scope push "$rename_base" "$renamed" refs/heads/main $'required=true\ncontract_required=false'

# Missing history and explicit tag/manual events retain full validation.
assert_scope push 0000000000000000000000000000000000000000 "$pushed" refs/heads/main $'required=true\ncontract_required=false'
assert_scope push "$target" "$pushed" refs/tags/v1.0.0 $'required=true\ncontract_required=false'
assert_scope workflow_dispatch '' "$pushed" refs/heads/main $'required=true\ncontract_required=false'

echo 'go-scope tests passed'
