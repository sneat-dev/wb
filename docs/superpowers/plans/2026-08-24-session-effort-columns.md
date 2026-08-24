# `wb session list` Effort/Worktree/Branch Columns: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wb session list` shows what each registered session is working on: distinct efforts, worktrees, and branches derived from the worktree owners registry, as single-value-or-count columns (full lists in JSON).

**Architecture:** A pure join helper in `cmd/wb/session_attribution.go` matches `worktrees.ListResult.Owners` entries to `session.View`s by PID + started-at reuse guard; `session_list.go` calls it, renders three new columns, and degrades to `-` + a stderr warning if derivation fails. `internal/session` and `internal/worktrees` stay independent.

**Tech Stack:** Go 1.26, cobra, existing `worktrees.List` / `session.List` APIs. No new persistence.

**Spec:** `docs/superpowers/specs/2026-08-24-session-effort-columns.md`.

## Global Constraints

- Matching rule: `owner.PID == session.PID && !owner.At.Before(session.StartedAt)`.
- Display: efforts — one distinct value → the ID, several → count, none → `-`; branches — same with the branch name; worktrees — count or `-`. Text-mode single values truncated to 24 runes with `…`; JSON carries full sorted lists untruncated.
- Column order: `PID RUNTIME MODEL WB STARTED EFFORTS WORKTREES BRANCHES STATE`.
- `session list` stays read-only (no WB home creation) and never fails because derivation failed: on `worktrees.List` error print `derive worktree attribution: <err>` once to stderr, render `-` columns, keep exit 0.
- The `--live` filter applies before derivation (never scan for rows that won't render). No sessions registered → existing message, no scan.
- `session.View` in `internal/session` is not modified; the enriched row is a cmd-layer struct embedding it. JSON additive: `efforts []string`, `worktrees []string`, `branches []string` (present, possibly empty arrays — use `[]string{}` not nil so JSON shows `[]`).
- Gates every task: `go fmt ./...`; `go build ./...`; `golangci-lint run ./...` → `0 issues` (paste output); tests also under `HOME=$(mktemp -d)`; `t.Setenv` for any env the tests rely on (`WB_HOME`).
- Capability manifest: `wb.session.list` exists in `ai/capabilities.json` (flags `--format`, `--live`; skill `ai/skills/wb-worktrees/references/ownership.md`; feature `spec/features/work-log/README.md`). No new command and no new flags → the validator should stay green; Task 2 verifies and updates the `modes` line + skill reference prose to mention the derived columns (claims must be true of shipped code).
- Work in `/home/ai/projects/sneat-dev/wb-session-efforts` on branch `feat/session-effort-columns` (from origin/main); never touch the shared checkout `/home/ai/projects/sneat-dev/wb` (currently on another session's branch). `wb session*.go` was authored by a parallel session — rebase onto origin/main before the PR and re-run gates.

---

## File structure

| Path | Responsibility |
|---|---|
| `cmd/wb/session_attribution.go` | pure join: sessions × worktree list results → enriched rows |
| `cmd/wb/session_attribution_test.go` | pure-function tests |
| `cmd/wb/session_list.go` (modify) | call the join, render columns, JSON shape, stderr degrade |
| `cmd/wb/session_list_test.go` (create or extend) | command-level rendering tests |
| `ai/capabilities.json`, `ai/skills/wb-worktrees/references/ownership.md` (modify) | mention derived columns |

---

### Task 1: The join helper

**Files:**
- Create: `cmd/wb/session_attribution.go`, `cmd/wb/session_attribution_test.go`

**Interfaces:**
- Consumes: `session.View` (`internal/session`: `Record{PID, Runtime, Model, AgentID, WBVersion, StartedAt}`, `View{Record, State}`), `worktrees.ListResult` (`Owners []OwnerView` where `OwnerView.OwnerRegistration` has `Agent, Model, Effort string; PID int; At time.Time`; plus `Branch`, `WorktreeDir`).
- Produces:
  ```go
  type sessionRow struct {
      session.View
      Efforts   []string `json:"efforts"`
      Worktrees []string `json:"worktrees"`
      Branches  []string `json:"branches"`
  }
  func attributeSessions(views []session.View, results []worktrees.ListResult) []sessionRow
  func condense(values []string, max int) string // "-", single (truncated to max runes + "…"), or count
  ```

- [ ] **Step 1: Write the failing tests**

```go
// cmd/wb/session_attribution_test.go
package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func sessionAt(pid int, started time.Time) session.View {
	return session.View{Record: session.Record{PID: pid, StartedAt: started}, State: session.StateLive}
}

func ownerAt(pid int, effort string, at time.Time) worktrees.OwnerView {
	return worktrees.OwnerView{OwnerRegistration: worktrees.OwnerRegistration{PID: pid, Effort: effort, At: at}}
}

func TestAttributeSessionsJoinsByPIDWithReuseGuard(t *testing.T) {
	started := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	views := []session.View{sessionAt(41, started)}
	results := []worktrees.ListResult{
		{Task: "task-7", Branch: "agent/task-7", WorktreeDir: "/wt/task-7/acme/x",
			Owners: []worktrees.OwnerView{
				ownerAt(41, "effort-a", started.Add(time.Minute)),      // counts
				ownerAt(41, "effort-old", started.Add(-time.Hour)),     // pre-registration: PID reuse, excluded
				ownerAt(99, "someone-else", started.Add(time.Minute)),  // other PID, excluded
			}},
		{Task: "task-9", Branch: "agent/task-9", WorktreeDir: "/wt/task-9/acme/y",
			Owners: []worktrees.OwnerView{ownerAt(41, "effort-b", started.Add(2 * time.Minute))}},
	}
	rows := attributeSessions(views, results)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if !reflect.DeepEqual(row.Efforts, []string{"effort-a", "effort-b"}) {
		t.Fatalf("Efforts = %v (sorted distinct, reuse-guarded)", row.Efforts)
	}
	if !reflect.DeepEqual(row.Worktrees, []string{"/wt/task-7/acme/x", "/wt/task-9/acme/y"}) {
		t.Fatalf("Worktrees = %v", row.Worktrees)
	}
	if !reflect.DeepEqual(row.Branches, []string{"agent/task-7", "agent/task-9"}) {
		t.Fatalf("Branches = %v", row.Branches)
	}
}

func TestAttributeSessionsDedupsAcrossOwnersAndIgnoresEmptyEfforts(t *testing.T) {
	started := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	views := []session.View{sessionAt(41, started)}
	results := []worktrees.ListResult{
		{Task: "t", Branch: "b", WorktreeDir: "/wt/t/a/r",
			Owners: []worktrees.OwnerView{
				ownerAt(41, "e1", started.Add(time.Minute)),
				ownerAt(41, "e1", started.Add(2*time.Minute)), // duplicate effort
				ownerAt(41, "", started.Add(3*time.Minute)),   // empty effort ignored for Efforts, still attributes the worktree
			}},
	}
	row := attributeSessions(views, results)[0]
	if !reflect.DeepEqual(row.Efforts, []string{"e1"}) || len(row.Worktrees) != 1 || len(row.Branches) != 1 {
		t.Fatalf("row = %+v", row)
	}
}

func TestAttributeSessionsEmptyInputs(t *testing.T) {
	started := time.Now().UTC()
	rows := attributeSessions([]session.View{sessionAt(41, started)}, nil)
	if len(rows) != 1 || len(rows[0].Efforts) != 0 || rows[0].Efforts == nil {
		t.Fatalf("want one row with empty non-nil slices, got %+v", rows)
	}
}

func TestCondense(t *testing.T) {
	cases := []struct {
		in   []string
		max  int
		want string
	}{
		{nil, 24, "-"},
		{[]string{"only"}, 24, "only"},
		{[]string{"a", "b", "c"}, 24, "3"},
		{[]string{"a-very-long-effort-identifier-here"}, 10, "a-very-lon…"},
	}
	for _, c := range cases {
		if got := condense(c.in, c.max); got != c.want {
			t.Errorf("condense(%v, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/wb/ -run 'TestAttributeSessions|TestCondense' -v`
Expected: FAIL — undefined symbols. (If `worktrees.OwnerView`/`OwnerRegistration` field names differ from the test's assumptions, check `internal/worktrees/owners.go` and adjust the TEST to the real names — the real code wins.)

- [ ] **Step 3: Implement**

```go
// cmd/wb/session_attribution.go
package main

import (
	"sort"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sessionRow is a registered session enriched with what it worked on,
// derived from the worktree owners registry. The lists are sorted, distinct,
// and always non-nil so JSON renders [] rather than null.
type sessionRow struct {
	session.View
	Efforts   []string `json:"efforts"`
	Worktrees []string `json:"worktrees"`
	Branches  []string `json:"branches"`
}

// attributeSessions joins owner entries to sessions by declared PID, with a
// started-at guard so an entry written by a previous holder of a recycled
// PID is never attributed to the new session.
func attributeSessions(views []session.View, results []worktrees.ListResult) []sessionRow {
	rows := make([]sessionRow, 0, len(views))
	for _, view := range views {
		efforts, worktreeDirs, branches := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, result := range results {
			matched := false
			for _, owner := range result.Owners {
				if owner.PID != view.PID || owner.At.Before(view.StartedAt) {
					continue
				}
				matched = true
				if owner.Effort != "" {
					efforts[owner.Effort] = true
				}
			}
			if matched {
				if result.WorktreeDir != "" {
					worktreeDirs[result.WorktreeDir] = true
				}
				if result.Branch != "" {
					branches[result.Branch] = true
				}
			}
		}
		rows = append(rows, sessionRow{
			View:      view,
			Efforts:   sortedKeys(efforts),
			Worktrees: sortedKeys(worktreeDirs),
			Branches:  sortedKeys(branches),
		})
	}
	return rows
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// condense renders a derived list for the text table: nothing → "-", one
// value → the value (truncated to max runes), several → their count.
func condense(values []string, max int) string {
	switch len(values) {
	case 0:
		return "-"
	case 1:
		runes := []rune(values[0])
		if len(runes) > max {
			return string(runes[:max]) + "…"
		}
		return values[0]
	default:
		return itoa(len(values))
	}
}
```

Use `strconv.Itoa` (import `strconv`) rather than a helper `itoa` — the snippet's `itoa` is a placeholder; write `strconv.Itoa(len(values))`.

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go test ./cmd/wb/ -run 'TestAttributeSessions|TestCondense' -v`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

`golangci-lint run ./...` → `0 issues`.
```bash
git add cmd/wb/session_attribution.go cmd/wb/session_attribution_test.go
git commit -m "feat(session): join owner registrations to sessions by PID with a reuse guard"
```

---

### Task 2: Wire into `wb session list` + docs

**Files:**
- Modify: `cmd/wb/session_list.go`, `ai/capabilities.json` (the `wb.session.list` `modes` line), `ai/skills/wb-worktrees/references/ownership.md` (one short paragraph)
- Create or extend: `cmd/wb/session_list_test.go`

**Interfaces:**
- Consumes: Task 1's `attributeSessions`/`condense`; `worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot})`; `sessionDirForRead`, `session.List`.
- Produces: `runSessionList(directory, projectsRoot string, onlyLive, jsonOut bool, out, errOut io.Writer) error` — extract from the current RunE so tests can call it with temp dirs and buffers; a `listWorktrees func` field/parameter seam is NOT needed — tests use a real temp WB_HOME.

- [ ] **Step 1: Write the failing tests**

```go
// Test list (write full bodies; register sessions by writing real record
// files via session.Register into a temp dir, and set WB_HOME to a temp home
// so worktrees.List sees a controlled worktrees tree):
//
// TestSessionListRendersDerivedColumns:
//   - session.Register(dir, session.Record{PID: os.Getpid(), Runtime: "test", StartedAt: <now-1h — Register may stamp it; check Register's behaviour and set fields the way its API allows>})
//     NOTE: read internal/session Register first — it sets StartedAt itself if zero? If Register controls StartedAt,
//     write the record file directly via session.Register then hand-edit is NOT allowed; instead build the fixture
//     through the API and use owner entries with At = time.Now() so the guard passes.
//   - Build a fake worktrees tree: the cheapest honest route is to call the worktrees package's own
//     write path if a cheap one exists (check internal/worktrees owners.go recordOwner/appendLocalEvent visibility);
//     they are unexported, so instead construct the on-disk local work log the same way its own tests do —
//     READ internal/worktrees/custody_test.go and owners_test.go and reuse their fixture approach from the cmd/wb
//     package via a real `worktrees.Create`… If that is too heavy, split runSessionList so the
//     worktrees.List call is one small var indirection `sessionWorktreeLister = worktrees.List` overridable in tests —
//     choose whichever the existing test files' conventions support, state the choice in the report.
//   - Assert text header contains "EFFORTS  WORKTREES  BRANCHES" (tabwriter spacing aside — assert the words in order)
//     and the row shows the single effort ID, worktree count, branch count/name.
// TestSessionListJSONCarriesFullLists: --format json output decodes; rows have efforts/worktrees/branches arrays (non-null).
// TestSessionListDegradesWhenWorktreesScanFails: point the lister/WB_HOME at an unreadable path → rows render "-" in the
//   three columns, stderr contains "derive worktree attribution:", exit nil.
// TestSessionListNoSessionsSkipsScan: empty session dir → existing "no session has registered" message; the lister
//   must not be called (with the var-indirection seam, install one that panics).
```

Prefer the `sessionWorktreeLister` var-indirection seam (matches how `remoteDeps` seams work in this codebase) unless the worktrees fixtures are genuinely cheap to build.

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/wb/ -run TestSessionList -v` → FAIL.

- [ ] **Step 3: Implement**

In `session_list.go`:
- Extract the RunE body into `runSessionList(directory, projectsRoot string, onlyLive, jsonOut bool, out, errOut io.Writer) error`.
- After the `--live` filter and the empty-check, call the lister (`sessionWorktreeLister(cmd.Context()-equivalent…, worktrees.ListOptions{ProjectsRoot: projectsRoot})` — use `context.Background()` as other commands do). On error: `fmt.Fprintf(errOut, "derive worktree attribution: %v\n", err)` and continue with `nil` results.
- `rows := attributeSessions(views, results)`; JSON: encode `rows`; text: extend `renderSessions` to take rows and print `PID RUNTIME MODEL WB STARTED EFFORTS WORKTREES BRANCHES STATE` using `condense(row.Efforts, 24)`, worktree count (`"-"` for 0, else `strconv.Itoa(len(...))`), `condense(row.Branches, 24)`.
- Keep the "no session has registered" early return BEFORE any worktrees scan.

Docs:
- `ai/capabilities.json` `wb.session.list` `modes`: append a mode line, e.g. `"Derives per-session EFFORTS, WORKTREES, and BRANCHES columns from worktree owner registrations (PID match with a registration-time reuse guard); --format json carries the full lists."` Run the manifest validator to confirm anchors/flags still hold.
- `ai/skills/wb-worktrees/references/ownership.md`: one short paragraph documenting the new columns and the single-value-or-count rule.

- [ ] **Step 4: Run tests**

`go fmt ./... && go test ./cmd/wb/ -run 'TestSessionList|TestAttributeSessions|TestCondense|TestAgentSkills|TestCapabilityManifest' -v` and the full `go test ./cmd/wb/` (all green — this feature has no deferred-docs phase), plus `HOME=$(mktemp -d) go test ./cmd/wb/ -run TestSessionList`.

- [ ] **Step 5: Lint + commit**

`golangci-lint run ./...` → `0 issues`. `specscore spec lint` → 0 violations.
```bash
git add cmd/wb ai
git commit -m "feat(session): show efforts, worktrees, and branches per session in wb session list"
```

---

### Task 3: Final verification and PR (controller)

- [ ] `go fmt ./...`; `golangci-lint run ./...`; `go build ./...`; `HOME=$(mktemp -d) go test ./...`; coverage ≥ 58%.
- [ ] Manual smoke: `wb session register --pid $PPID --runtime claude-code --model fable`, `wb worktree create` a scratch task with `--no-claim` (or reuse an existing worktree), then `wb session list` shows the derived columns; `--format json` well-formed.
- [ ] Rebase onto latest origin/main (session*.go moves fast), re-run gates, push, PR, watch CI, squash-merge with explicit subject when green, delete branch, close nothing (no superseded PR), pull main, wait for the release workflow, `wb self-update --yes`.

---

## Self-review against the spec

- Matching rule (PID + StartedAt guard) → Task 1, tested incl. reuse. ✔
- Single-value-or-count display, truncation, worktrees count-only → Task 1 `condense` + Task 2 rendering. ✔
- JSON full lists, additive, non-nil → Task 1 struct + Task 2 test. ✔
- Degrade on scan failure (stderr warning, `-`, exit 0); no-sessions skips scan; read-only → Task 2. ✔
- `internal/session` untouched; join in cmd layer → structure. ✔
- Docs truthfulness (capability mode line + ownership.md) validated by the manifest test → Task 2. ✔
- Rebase-before-PR coordination with the parallel session's code → Task 3 + Global Constraints. ✔
