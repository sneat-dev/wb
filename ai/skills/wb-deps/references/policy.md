# Dependency and layering policy

`wb deps policy` holds every repository to one central rule document: which
kinds of repository may depend on which kinds of dependency, and which
direction imports may travel between packages inside a repository.

The scan is lexical — Go import blocks and `go.mod`, never a resolved module
graph. No credentials, no downloads, and a verdict even when the build cannot
start. That makes it safe to run as the first job in CI.

## Choose the narrowest verb

| Intent | Command |
|---|---|
| Gate one repository | `wb deps policy check` |
| Understand one verdict | `wb deps policy explain` |
| See the rules in force here | `wb deps policy show` |
| Lint a policy document | `wb deps policy validate` |
| Run a policy's own assertions | `wb deps policy test` |
| Adopt in a repository | `wb deps policy init` |
| Fleet burn-down | `wb deps policy report` |
| Who is governed, by what | `wb deps policy drift` |
| Blast radius of a policy edit | `wb deps policy impact` |

## Gate one repository

```
wb deps policy check ./backend --format github
```

Exits `0` clean, `1` when an enforcing rule is violated, `2` when the
invocation or policy is unusable. Findings from rules the policy runs in report
mode print and count but never change the exit code. `--strict` promotes them
for one run.

## Understand a verdict

Never guess why a check failed — ask:

```
wb deps policy explain github.com/dal-go/dalgo2firestore ./backend
```

It names the group that won, the pattern that matched and its position, every
later pattern that would also have matched, the repository's type and how it
was chosen, and the verdict in each of `source`, `tests` and `main`.

The shadowed-match list is the important part. Groups are first-match-wins, so
a broad pattern above a narrow one silently takes every path the narrow one was
written for.

```
wb deps policy show ./backend
```

prints the allow list actually in force, plus the layer order and mode.

## The repository's own file

`.wb-deps-policy.yaml` is two lines:

```yaml
policy: acme/cicd//policy/backend.yaml
type: extension-implementation
```

A repository may add `strict: true` and nothing else. Declaring groups or
types, extending an allow list, setting a rule mode, or writing
`strict: false` is refused with exit `2`. It names the policy **source** and
never a release: a repository frozen on an old policy would be carrying an
exception nobody wrote down.

`type` is optional wherever the module path is enough to detect it.

## Author and change a policy

```
wb deps policy validate policy/backend.yaml
wb deps policy test policy/backend.yaml
```

`validate` reports patterns an earlier declaration already claims in full —
the ordering mistake that changes every verdict downstream and errors nowhere.
`test` runs the `expect:` assertions the policy declares about itself, and
fails a policy that declares none.

Before tagging a policy change, measure it:

```
wb deps policy impact policy/backend.yaml --match 'acme/*'
```

Because repositories cannot pin a release, a tightened rule reaches all of them
at once. This puts that blast radius in the candidate's pull request.

## Watch the fleet

```
wb deps policy report --match 'acme/*'
wb deps policy drift --match 'acme/*'
```

`report` groups findings by rule and counts modules no policy governs — a
report-mode rule is invisible until someone counts it, and that count is what
has to reach zero before it can be promoted to enforcing. `drift` answers the
quieter failure: which modules nobody wired up, and where a declared type
disagrees with detection.

## Adopt

```
wb deps policy init ./backend --policy acme/cicd//policy/backend.yaml
```

Writes the declaration with the detected type, then runs `check` immediately —
adoption starts with an honest verdict rather than a green tick. Expect it to
exit `1` on a repository that is not clean yet; wire the check as a required
status only once it passes.
