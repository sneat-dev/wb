# `wb remote` — Fleet State Across Machines: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one machine publish its wb fleet state (dirty/unpushed repos, live worktrees) to a shared git repository, and let any machine read every machine's published state as one worklist.

**Architecture:** A new `internal/remotestate` package owns the `Snapshot` model, the `Provider` interface, and config loading; `internal/remotestate/gitrepo` is the only provider (one `machines/<login>/<machine>/snapshot.yaml` per machine in a team state repo). `cmd/wb/remote*.go` adds `wb remote publish|status|machines`, reusing the existing `wb status` scan and `worktrees.List`. `wb sync --publish` is the only automation hook.

**Tech Stack:** Go 1.26, cobra, `gopkg.in/yaml.v3`, `internal/gitops` (shell-out git), `internal/discover` (gh CLI), bare-repo test fixtures as in `internal/fleetsync/unpushed_test.go`.

**Spec:** `docs/superpowers/specs/2026-08-23-remote-state-design.md` (PR #133).

## Global Constraints

- Config file: `~/.config/wb/wb.yaml`, new top-level `remote:` section — `provider: git`, `repo: <owner>/<name>`, `machine: <name>` (required, no hostname fallback), `publish.unpushed: subjects|counts` (default `subjects`).
- Snapshot `schema_version` is `1`. Only non-clean repositories are listed; `repositories_scanned` holds the full count.
- Store layout: `README.md` + `machines/<login>/<machine>/snapshot.yaml`. Commit message: `wb: publish <login>/<machine> @ <RFC3339>`.
- Publish = pull --rebase → write → add → commit (skip if unchanged) → push; on rejection rebase + push once more, then fail with the local commit kept.
- Exit codes: `0` ok, `1` findings/runtime failure (`exitFindings`), `2` usage/unconfigured (`exitUsage`). `wb remote status` exits `0` even when some entries have errors.
- The git provider never writes to any repository except the state repo clone at `<projects-root>/<owner>/<name>`.
- No dependency on any synchestra module. No daemon.
- Every public command needs: `ai/skills/commands.json` coverage, a skill reference file, a `spec/features/...` feature, and an `ai/capabilities.json` entry — `cmd/wb/skills_test.go` enforces this and will fail CI otherwise.
- UI/CLI rule from the user's global instructions: one command/screen per file.
- Before any build/lint/test: `gofmt`/`go fmt ./...` on changed Go files. CI coverage floor: `MINIMUM_COVERAGE: "58.0"` total; new packages aim for full coverage of their own code.
- Work on branch `feat/remote-state` in a **git worktree** (the shared checkout stays on `main`): `git -C ~/projects/sneat-dev/wb worktree add ../wb-remote-state -b feat/remote-state main` and run every command below inside `~/projects/sneat-dev/wb-remote-state`.

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/wbconfig/path.go` | `DefaultPath()` for `~/.config/wb/wb.yaml` (moved out of `cmd/wb/run.go`) |
| `internal/remotestate/snapshot.go` | `Snapshot`, `RepositoryState`, `WorktreeState`, `Build(...)`, redaction, YAML encode/decode with schema check |
| `internal/remotestate/config.go` | `Config`, `LoadConfig(path)`, validation, `UnconfiguredError` with YAML snippet |
| `internal/remotestate/provider.go` | `Provider`, `Entry`, `PublishResult` |
| `internal/remotestate/gitrepo/provider.go` | git-backed provider |
| `internal/gitops/gitops.go` (append) | `PullRebase`, `Push`, `AddCommit`, `HeadSHA` |
| `cmd/wb/remote.go` | `wb remote` group + shared flag/config helpers |
| `cmd/wb/remote_collect.go` | builds a `Snapshot` from the local fleet (status scan + worktrees) |
| `cmd/wb/remote_publish.go` | `wb remote publish` |
| `cmd/wb/remote_status.go` | `wb remote status` |
| `cmd/wb/remote_machines.go` | `wb remote machines` |
| `cmd/wb/sync.go` (modify) | `--publish` flag |
| `spec/features/remote-state/README.md`, `spec/features/README.md` | SpecScore feature |
| `ai/skills/wb-fleet/references/remote.md`, `ai/skills/wb-fleet/SKILL.md`, `ai/skills/commands.json`, `ai/capabilities.json` | agent-skill surface |
| `README.md`, `docs/cli-flag-matrix.md` | user docs |

---

### Task 1: Shared config path

**Files:**
- Create: `internal/wbconfig/path.go`, `internal/wbconfig/path_test.go`
- Modify: `cmd/wb/run.go:46-51` (replace `defaultConfigPath`)

**Interfaces:**
- Produces: `func wbconfig.DefaultPath() string`

- [ ] **Step 1: Write the failing test**

```go
// internal/wbconfig/path_test.go
package wbconfig

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathLivesUnderUserConfigDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/wbconfig-home")
	got := DefaultPath()
	want := filepath.Join("/tmp/wbconfig-home", ".config", "wb", "wb.yaml")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/wbconfig/ -run TestDefaultPath -v`
Expected: FAIL — package does not exist / `DefaultPath` undefined.

- [ ] **Step 3: Implement**

```go
// internal/wbconfig/path.go
// Package wbconfig resolves the user-level WB configuration file that several
// commands share (recipes for wb run, the remote section for wb remote).
package wbconfig

import (
	"os"
	"path/filepath"
)

// DefaultPath returns ~/.config/wb/wb.yaml.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "wb", "wb.yaml")
}
```

In `cmd/wb/run.go` delete `defaultConfigPath` (lines 46-51) and replace its single call in `runRun` with `wbconfig.DefaultPath()`; add the import `"github.com/sneat-dev/wb/internal/wbconfig"`. Remove the now-unused `os`/`filepath` imports only if the compiler says they are unused.

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go test ./internal/wbconfig/ ./cmd/wb/ -run 'TestDefaultPath|TestRun' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/wbconfig cmd/wb/run.go
git commit -m "refactor(config): share the wb.yaml default path"
```

---

### Task 2: Snapshot model

**Files:**
- Create: `internal/remotestate/snapshot.go`, `internal/remotestate/snapshot_test.go`

**Interfaces:**
- Consumes: `gitops.RepoStatus`, `gitops.TrackingState`, `worktrees.ListResult`
- Produces:
  ```go
  const SchemaVersion = 1
  type Snapshot struct { SchemaVersion int; Login, Machine string; PublishedAt time.Time; WBVersion, ProjectsRoot string; RepositoriesScanned int; Repositories []RepositoryState; Worktrees []WorktreeState }
  type RepositoryState struct { Repository, Path, Status, Summary, Branch, Upstream string; Ahead, Behind int; Modified, Untracked, Conflicted, Unpushed []string; UnpushedCount int; Stashed []string; Error string }
  type WorktreeState struct { Task, Repository, Branch, HeadSHA, Dir string }
  type RepositoryInput struct { Repository, Path string; Status gitops.RepoStatus; Tracking gitops.TrackingState; Err error }
  type Redaction string; const RedactNone Redaction = "subjects"; const RedactUnpushed Redaction = "counts"
  func (s Snapshot) Key() string   // "<login>/<machine>"
  func Build(identity Snapshot, repos []RepositoryInput, worktrees []worktrees.ListResult, redaction Redaction) Snapshot
  func Encode(s Snapshot) ([]byte, error)
  func Decode(data []byte) (Snapshot, error)  // errors on schema_version > SchemaVersion
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/remotestate/snapshot_test.go
package remotestate

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func identity() Snapshot {
	return Snapshot{Login: "alice", Machine: "laptop", PublishedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC), WBVersion: "v1.2.3", ProjectsRoot: "/home/alice/projects"}
}

func TestBuildListsOnlyNonCleanRepositoriesAndCountsAll(t *testing.T) {
	repos := []RepositoryInput{
		{Repository: "acme/clean", Path: "/p/clean", Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main"}},
		{Repository: "acme/dirty", Path: "/p/dirty", Status: gitops.RepoStatus{Modified: []string{"a.go"}}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main"}},
		{Repository: "acme/ahead", Path: "/p/ahead", Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 2}},
		{Repository: "acme/noup", Path: "/p/noup", Tracking: gitops.TrackingState{Branch: "feature", Configured: true}},
		{Repository: "acme/broken", Path: "/p/broken", Err: errOops},
	}
	snap := Build(identity(), repos, nil, RedactNone)

	if snap.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", snap.SchemaVersion, SchemaVersion)
	}
	if snap.RepositoriesScanned != 5 {
		t.Fatalf("RepositoriesScanned = %d, want 5", snap.RepositoriesScanned)
	}
	got := make([]string, 0, len(snap.Repositories))
	for _, r := range snap.Repositories {
		got = append(got, r.Repository+":"+r.Status)
	}
	want := "acme/ahead:attention acme/broken:error acme/dirty:attention acme/noup:attention"
	if strings.Join(got, " ") != want {
		t.Fatalf("repositories = %q, want %q (sorted by slug, clean omitted)", strings.Join(got, " "), want)
	}
	if snap.Key() != "alice/laptop" {
		t.Fatalf("Key() = %q", snap.Key())
	}
}

func TestBuildRedactsUnpushedSubjectsToCounts(t *testing.T) {
	repos := []RepositoryInput{{Repository: "acme/x", Path: "/p/x", Status: gitops.RepoStatus{Unpushed: []string{"abc feat", "def fix"}}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 2}}}

	full := Build(identity(), repos, nil, RedactNone)
	if len(full.Repositories[0].Unpushed) != 2 || full.Repositories[0].UnpushedCount != 2 {
		t.Fatalf("subjects mode: Unpushed=%v UnpushedCount=%d", full.Repositories[0].Unpushed, full.Repositories[0].UnpushedCount)
	}
	redacted := Build(identity(), repos, nil, RedactUnpushed)
	if redacted.Repositories[0].Unpushed != nil || redacted.Repositories[0].UnpushedCount != 2 {
		t.Fatalf("counts mode: Unpushed=%v UnpushedCount=%d", redacted.Repositories[0].Unpushed, redacted.Repositories[0].UnpushedCount)
	}
}

func TestBuildCarriesWorktrees(t *testing.T) {
	wts := []worktrees.ListResult{{Task: "task-7", Repository: "acme/x", Branch: "agent/task-7", HeadSHA: "abc123", WorktreeDir: "/wt/task-7/acme/x"}}
	snap := Build(identity(), nil, wts, RedactNone)
	if len(snap.Worktrees) != 1 || snap.Worktrees[0] != (WorktreeState{Task: "task-7", Repository: "acme/x", Branch: "agent/task-7", HeadSHA: "abc123", Dir: "/wt/task-7/acme/x"}) {
		t.Fatalf("Worktrees = %+v", snap.Worktrees)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	snap := Build(identity(), []RepositoryInput{{Repository: "acme/x", Path: "/p/x", Status: gitops.RepoStatus{Stashed: []string{"stash@{0}"}}}}, nil, RedactNone)
	data, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Key() != snap.Key() || !back.PublishedAt.Equal(snap.PublishedAt) || len(back.Repositories) != 1 || back.Repositories[0].Stashed[0] != "stash@{0}" {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}

func TestDecodeRejectsNewerSchema(t *testing.T) {
	_, err := Decode([]byte("schema_version: 99\nlogin: a\nmachine: b\n"))
	if err == nil || !strings.Contains(err.Error(), "schema_version 99") {
		t.Fatalf("err = %v, want newer-schema error", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte("{not yaml")); err == nil {
		t.Fatal("expected YAML error")
	}
}

var errOops = errString("git status: boom")

type errString string

func (e errString) Error() string { return string(e) }
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/remotestate/ -v`
Expected: FAIL — undefined `Build`, `Snapshot`, etc.

- [ ] **Step 3: Implement**

```go
// internal/remotestate/snapshot.go
// Package remotestate publishes one machine's WB fleet state to a shared
// store and reads every machine's state back. The store is pluggable; the
// snapshot format is not.
package remotestate

import (
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// SchemaVersion is the snapshot format this binary writes and the newest it
// can read.
const SchemaVersion = 1

// Redaction selects how unpushed commits are published.
type Redaction string

const (
	// RedactNone publishes `git log --oneline` subjects of unpushed commits.
	RedactNone Redaction = "subjects"
	// RedactUnpushed publishes only the number of unpushed commits.
	RedactUnpushed Redaction = "counts"
)

// Snapshot is one machine's published fleet state.
type Snapshot struct {
	SchemaVersion       int               `yaml:"schema_version" json:"schema_version"`
	Login               string            `yaml:"login" json:"login"`
	Machine             string            `yaml:"machine" json:"machine"`
	PublishedAt         time.Time         `yaml:"published_at" json:"published_at"`
	WBVersion           string            `yaml:"wb_version" json:"wb_version"`
	ProjectsRoot        string            `yaml:"projects_root" json:"projects_root"`
	RepositoriesScanned int               `yaml:"repositories_scanned" json:"repositories_scanned"`
	Repositories        []RepositoryState `yaml:"repositories" json:"repositories"`
	Worktrees           []WorktreeState   `yaml:"worktrees" json:"worktrees"`
}

// Key identifies the machine inside a store: "<login>/<machine>".
func (s Snapshot) Key() string { return s.Login + "/" + s.Machine }

// RepositoryState is one non-clean repository on the publishing machine.
type RepositoryState struct {
	Repository    string   `yaml:"repository" json:"repository"`
	Path          string   `yaml:"path" json:"path"`
	Status        string   `yaml:"status" json:"status"` // attention | error
	Summary       string   `yaml:"summary,omitempty" json:"summary,omitempty"`
	Branch        string   `yaml:"branch,omitempty" json:"branch,omitempty"`
	Upstream      string   `yaml:"upstream,omitempty" json:"upstream,omitempty"`
	Ahead         int      `yaml:"ahead,omitempty" json:"ahead,omitempty"`
	Behind        int      `yaml:"behind,omitempty" json:"behind,omitempty"`
	Modified      []string `yaml:"modified,omitempty" json:"modified,omitempty"`
	Untracked     []string `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Conflicted    []string `yaml:"conflicted,omitempty" json:"conflicted,omitempty"`
	Unpushed      []string `yaml:"unpushed,omitempty" json:"unpushed,omitempty"`
	UnpushedCount int      `yaml:"unpushed_count,omitempty" json:"unpushed_count,omitempty"`
	Stashed       []string `yaml:"stashed,omitempty" json:"stashed,omitempty"`
	Error         string   `yaml:"error,omitempty" json:"error,omitempty"`
}

// WorktreeState is one live WB task worktree on the publishing machine.
type WorktreeState struct {
	Task       string `yaml:"task" json:"task"`
	Repository string `yaml:"repository" json:"repository"`
	Branch     string `yaml:"branch" json:"branch"`
	HeadSHA    string `yaml:"head_sha" json:"head_sha"`
	Dir        string `yaml:"dir" json:"dir"`
}

// RepositoryInput is the per-repository scan result Build consumes. Err set
// means the scan itself failed; Status and Tracking are then ignored.
type RepositoryInput struct {
	Repository string
	Path       string
	Status     gitops.RepoStatus
	Tracking   gitops.TrackingState
	Err        error
}

// needsAttention mirrors the spec's definition of non-clean: dirty tree,
// stash, unpushed commits, ahead/behind, or a configured-but-unresolved or
// missing upstream on a named branch.
func (in RepositoryInput) needsAttention() bool {
	if in.Status.Dirty() || in.Tracking.Ahead > 0 || in.Tracking.Behind > 0 {
		return true
	}
	return in.Tracking.Branch != "" && in.Tracking.Upstream == ""
}

// Build assembles a snapshot. identity supplies Login, Machine, PublishedAt,
// WBVersion, and ProjectsRoot; everything else is derived here. Clean
// repositories are counted but not listed. Output is sorted by repository.
func Build(identity Snapshot, repos []RepositoryInput, wts []worktrees.ListResult, redaction Redaction) Snapshot {
	snap := identity
	snap.SchemaVersion = SchemaVersion
	snap.RepositoriesScanned = len(repos)
	snap.Repositories = make([]RepositoryState, 0)
	snap.Worktrees = make([]WorktreeState, 0, len(wts))
	for _, in := range repos {
		if in.Err != nil {
			snap.Repositories = append(snap.Repositories, RepositoryState{Repository: in.Repository, Path: in.Path, Status: "error", Error: in.Err.Error()})
			continue
		}
		if !in.needsAttention() {
			continue
		}
		state := RepositoryState{
			Repository:    in.Repository,
			Path:          in.Path,
			Status:        "attention",
			Summary:       in.Status.Summary(),
			Branch:        in.Tracking.Branch,
			Upstream:      in.Tracking.Upstream,
			Ahead:         in.Tracking.Ahead,
			Behind:        in.Tracking.Behind,
			Modified:      in.Status.Modified,
			Untracked:     in.Status.Untracked,
			Conflicted:    in.Status.Conflicted,
			UnpushedCount: len(in.Status.Unpushed),
			Stashed:       in.Status.Stashed,
		}
		if redaction != RedactUnpushed {
			state.Unpushed = in.Status.Unpushed
		}
		snap.Repositories = append(snap.Repositories, state)
	}
	sort.Slice(snap.Repositories, func(i, j int) bool { return snap.Repositories[i].Repository < snap.Repositories[j].Repository })
	for _, wt := range wts {
		snap.Worktrees = append(snap.Worktrees, WorktreeState{Task: wt.Task, Repository: wt.Repository, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Dir: wt.WorktreeDir})
	}
	sort.Slice(snap.Worktrees, func(i, j int) bool {
		if snap.Worktrees[i].Task != snap.Worktrees[j].Task {
			return snap.Worktrees[i].Task < snap.Worktrees[j].Task
		}
		return snap.Worktrees[i].Repository < snap.Worktrees[j].Repository
	})
	return snap
}

// Encode renders a snapshot as YAML.
func Encode(s Snapshot) ([]byte, error) { return yaml.Marshal(s) }

// Decode parses a snapshot, refusing formats newer than this binary knows.
func Decode(data []byte) (Snapshot, error) {
	var s Snapshot
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parse snapshot: %w", err)
	}
	if s.SchemaVersion > SchemaVersion {
		return Snapshot{}, fmt.Errorf("snapshot schema_version %d is newer than supported %d; update wb", s.SchemaVersion, SchemaVersion)
	}
	return s, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go test ./internal/remotestate/ -v`
Expected: all 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remotestate/snapshot.go internal/remotestate/snapshot_test.go
git commit -m "feat(remotestate): snapshot model, build, and YAML codec"
```

---

### Task 3: Config and provider interface

**Files:**
- Create: `internal/remotestate/config.go`, `internal/remotestate/config_test.go`, `internal/remotestate/provider.go`

**Interfaces:**
- Produces:
  ```go
  type PublishConfig struct { Unpushed Redaction }
  type Config struct { Provider, Repo, Machine string; Publish PublishConfig }
  type UnconfiguredError struct{ Missing []string }  // Error() includes the YAML snippet
  func LoadConfig(path string) (Config, error)       // missing file ⇒ UnconfiguredError
  func (c Config) RepoOwner() string; func (c Config) RepoName() string
  type PublishResult struct{ Location string }
  type Entry struct { Snapshot Snapshot; Error string }
  type Provider interface { Publish(ctx, Snapshot) (PublishResult, error); Fetch(ctx) error; List(ctx) ([]Entry, error) }
  ```
  `Open` is added in Task 5 (it needs the gitrepo package).

- [ ] **Step 1: Write the failing tests**

```go
// internal/remotestate/config_test.go
package remotestate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigReadsRemoteSectionAndDefaults(t *testing.T) {
	path := writeConfig(t, "recipes: {}\nremote:\n  repo: sneat-dev/wb-state\n  machine: vm-1\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "git" || cfg.Repo != "sneat-dev/wb-state" || cfg.Machine != "vm-1" || cfg.Publish.Unpushed != RedactNone {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.RepoOwner() != "sneat-dev" || cfg.RepoName() != "wb-state" {
		t.Fatalf("owner/name = %q/%q", cfg.RepoOwner(), cfg.RepoName())
	}
}

func TestLoadConfigMissingFileIsUnconfigured(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"))
	var unconfigured *UnconfiguredError
	if !errors.As(err, &unconfigured) {
		t.Fatalf("err = %v, want UnconfiguredError", err)
	}
	if !strings.Contains(err.Error(), "remote:\n  provider: git\n  repo: <owner>/<name>\n  machine: <unique-name-for-this-machine>") {
		t.Fatalf("snippet missing from %q", err.Error())
	}
}

func TestLoadConfigRequiresRepoAndMachine(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "remote:\n  repo: a/b\n"))
	var unconfigured *UnconfiguredError
	if !errors.As(err, &unconfigured) || strings.Join(unconfigured.Missing, ",") != "machine" {
		t.Fatalf("err = %v, want missing machine", err)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"provider": "remote:\n  provider: ftp\n  repo: a/b\n  machine: m\n",
		"repo":     "remote:\n  repo: just-a-name\n  machine: m\n",
		"machine":  "remote:\n  repo: a/b\n  machine: 'has space'\n",
		"unpushed": "remote:\n  repo: a/b\n  machine: m\n  publish:\n    unpushed: maybe\n",
	} {
		if _, err := LoadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/remotestate/ -run TestLoadConfig -v`
Expected: FAIL — undefined `LoadConfig`.

- [ ] **Step 3: Implement config**

```go
// internal/remotestate/config.go
package remotestate

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigSnippet is printed whenever the remote section is absent or
// incomplete, so the fix is copy-paste rather than documentation lookup.
const ConfigSnippet = `remote:
  provider: git
  repo: <owner>/<name>
  machine: <unique-name-for-this-machine>
  publish:
    unpushed: subjects   # or: counts`

// PublishConfig tunes what a snapshot contains.
type PublishConfig struct {
	Unpushed Redaction `yaml:"unpushed"`
}

// Config is the remote section of ~/.config/wb/wb.yaml.
type Config struct {
	Provider string        `yaml:"provider"`
	Repo     string        `yaml:"repo"`
	Machine  string        `yaml:"machine"`
	Publish  PublishConfig `yaml:"publish"`
}

// RepoOwner returns the part of Repo before the slash.
func (c Config) RepoOwner() string { owner, _, _ := strings.Cut(c.Repo, "/"); return owner }

// RepoName returns the part of Repo after the slash.
func (c Config) RepoName() string { _, name, _ := strings.Cut(c.Repo, "/"); return name }

// UnconfiguredError reports a missing or incomplete remote section. Commands
// map it to the usage exit code.
type UnconfiguredError struct {
	Path    string
	Missing []string
}

func (e *UnconfiguredError) Error() string {
	return fmt.Sprintf("wb remote is not configured (missing %s in %s); add:\n\n%s", strings.Join(e.Missing, ", "), e.Path, ConfigSnippet)
}

var machineName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type configFile struct {
	Remote *Config `yaml:"remote"`
}

// LoadConfig reads the remote section from path. A missing file or section
// is an UnconfiguredError; a present but invalid value is a plain error.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, &UnconfiguredError{Path: path, Missing: []string{"remote section"}}
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var file configFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if file.Remote == nil {
		return Config{}, &UnconfiguredError{Path: path, Missing: []string{"remote section"}}
	}
	cfg := *file.Remote
	if cfg.Provider == "" {
		cfg.Provider = "git"
	}
	if cfg.Publish.Unpushed == "" {
		cfg.Publish.Unpushed = RedactNone
	}
	var missing []string
	if strings.TrimSpace(cfg.Repo) == "" {
		missing = append(missing, "repo")
	}
	if strings.TrimSpace(cfg.Machine) == "" {
		missing = append(missing, "machine")
	}
	if len(missing) > 0 {
		return Config{}, &UnconfiguredError{Path: path, Missing: missing}
	}
	if cfg.Provider != "git" {
		return Config{}, fmt.Errorf("remote.provider %q is not supported; only \"git\" is available", cfg.Provider)
	}
	if cfg.RepoOwner() == "" || cfg.RepoName() == "" || strings.Count(cfg.Repo, "/") != 1 {
		return Config{}, fmt.Errorf("remote.repo %q must be <owner>/<name>", cfg.Repo)
	}
	if !machineName.MatchString(cfg.Machine) {
		return Config{}, fmt.Errorf("remote.machine %q must match %s", cfg.Machine, machineName)
	}
	if cfg.Publish.Unpushed != RedactNone && cfg.Publish.Unpushed != RedactUnpushed {
		return Config{}, fmt.Errorf("remote.publish.unpushed %q must be %q or %q", cfg.Publish.Unpushed, RedactNone, RedactUnpushed)
	}
	return cfg, nil
}
```

- [ ] **Step 4: Implement the provider interface**

```go
// internal/remotestate/provider.go
package remotestate

import "context"

// PublishResult says where a snapshot landed: a commit SHA for a git store,
// a URL for a hub.
type PublishResult struct {
	Location string `json:"location"`
}

// Entry is one machine as read from the store. Error is set when the stored
// snapshot could not be decoded; Snapshot then carries only Login/Machine.
type Entry struct {
	Snapshot Snapshot `json:"snapshot"`
	Error    string   `json:"error,omitempty"`
}

// Provider is a shared store of machine snapshots. Implementations must be
// safe to call from several machines at once; the git provider relies on
// per-machine files plus rebase for that.
type Provider interface {
	// Publish overwrites the caller's own login/machine entry.
	Publish(ctx context.Context, snapshot Snapshot) (PublishResult, error)
	// Fetch refreshes the local view of the store. A hub provider may no-op.
	Fetch(ctx context.Context) error
	// List returns every machine currently in the store, including the
	// caller's own last-published entry, sorted by Key().
	List(ctx context.Context) ([]Entry, error)
}
```

- [ ] **Step 5: Run tests**

Run: `go fmt ./... && go test ./internal/remotestate/ -v`
Expected: PASS (10 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/remotestate/config.go internal/remotestate/config_test.go internal/remotestate/provider.go
git commit -m "feat(remotestate): remote config section and provider interface"
```

---

### Task 4: gitops primitives for the state repo

**Files:**
- Modify: `internal/gitops/gitops.go` (append at end)
- Create: `internal/gitops/staterepo_test.go`

**Interfaces:**
- Produces:
  ```go
  func PullRebase(repoPath string) error
  func Push(repoPath string) error
  func AddCommit(repoPath, message string, paths ...string) (committed bool, err error)
  func HeadSHA(repoPath string) (string, error)
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/gitops/staterepo_test.go
package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seededOriginAndClone returns a bare origin with one commit on main and a
// clone of it.
func seededOriginAndClone(t *testing.T) (origin, clone string) {
	t.Helper()
	origin = t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	clone = filepath.Join(t.TempDir(), "clone")
	gitIn(t, t.TempDir(), "clone", "-q", origin, clone)
	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "seed")
	gitIn(t, clone, "push", "-q", "origin", "main")
	return origin, clone
}

func TestAddCommitSkipsWhenNothingChanged(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if committed, err := AddCommit(clone, "add a", "a.txt"); err != nil || !committed {
		t.Fatalf("first AddCommit = (%v, %v), want (true, nil)", committed, err)
	}
	committed, err := AddCommit(clone, "nothing", "a.txt")
	if err != nil || committed {
		t.Fatalf("second AddCommit = (%v, %v), want (false, nil)", committed, err)
	}
}

func TestAddCommitPushAndHeadSHA(t *testing.T) {
	origin, clone := seededOriginAndClone(t)
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := AddCommit(clone, "add a", "a.txt")
	if err != nil || !committed {
		t.Fatalf("AddCommit = (%v, %v), want (true, nil)", committed, err)
	}
	if err := Push(clone); err != nil {
		t.Fatal(err)
	}
	sha, err := HeadSHA(clone)
	if err != nil || len(sha) != 40 {
		t.Fatalf("HeadSHA = %q, %v", sha, err)
	}
	if got := gitIn(t, origin, "rev-parse", "main"); got != sha {
		t.Fatalf("origin main = %s, want %s", got, sha)
	}
}

func TestPullRebaseReplaysLocalCommitOnTopOfRemote(t *testing.T) {
	origin, clone := seededOriginAndClone(t)
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "remote work")
	gitIn(t, other, "push", "-q", "origin", "main")

	gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "local work")
	if err := Push(clone); err == nil {
		t.Fatal("push should be rejected before rebase")
	}
	if err := PullRebase(clone); err != nil {
		t.Fatal(err)
	}
	if err := Push(clone); err != nil {
		t.Fatalf("push after rebase: %v", err)
	}
	if log := gitIn(t, origin, "log", "--format=%s", "main"); !strings.HasPrefix(log, "local work\nremote work") {
		t.Fatalf("origin log = %q", log)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/gitops/ -run 'TestAddCommit|TestPullRebase' -v`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement** (append to `internal/gitops/gitops.go`)

```go
// PullRebase replays local commits on top of the freshly fetched upstream.
// It is the retry primitive for shared state repositories where several
// machines push small non-conflicting commits.
func PullRebase(repoPath string) error {
	_, err := run(repoPath, "git", "pull", "--rebase", "--quiet")
	return err
}

// Push publishes the current branch to its upstream.
func Push(repoPath string) error {
	_, err := run(repoPath, "git", "push", "--quiet")
	return err
}

// AddCommit stages paths and commits them. It reports false without
// committing when the stage is empty, so callers can publish idempotently.
func AddCommit(repoPath, message string, paths ...string) (bool, error) {
	args := append([]string{"add", "--all", "--"}, paths...)
	if _, err := run(repoPath, "git", args...); err != nil {
		return false, err
	}
	if _, err := run(repoPath, "git", "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	if _, err := run(repoPath, "git", "commit", "--quiet", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// HeadSHA returns the full SHA of HEAD.
func HeadSHA(repoPath string) (string, error) {
	out, err := run(repoPath, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}
```

Note: `git add --all -- <path>` exits non-zero for a pathspec that matches nothing, so callers must always pass paths that exist (the provider writes both files before calling it).

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go test ./internal/gitops/ -v`
Expected: PASS, including the existing gitops tests.

- [ ] **Step 5: Commit**

```bash
git add internal/gitops/gitops.go internal/gitops/staterepo_test.go
git commit -m "feat(gitops): pull --rebase, push, add+commit, head sha primitives"
```

---

### Task 5: Git provider

**Files:**
- Create: `internal/remotestate/gitrepo/provider.go`, `internal/remotestate/gitrepo/provider_test.go`

**Interfaces:**
- Consumes: Task 3 types, Task 4 gitops functions, `gitops.Clone`.
- Produces:
  ```go
  // gitrepo
  type Options struct { ClonePath string; CloneURL string }   // ClonePath = <projects-root>/<owner>/<name>
  func New(opts Options) *Provider
  func (p *Provider) Publish(ctx, Snapshot) (remotestate.PublishResult, error)
  func (p *Provider) Fetch(ctx) error
  func (p *Provider) List(ctx) ([]remotestate.Entry, error)
  func SnapshotPath(login, machine string) string               // "machines/<login>/<machine>/snapshot.yaml"
  ```
  Provider selection (`openRemote`) lives in `cmd/wb/remote.go` (Task 6): `gitrepo` imports `remotestate`, so a constructor inside `remotestate` would be an import cycle.

- [ ] **Step 1: Write the failing tests**

```go
// internal/remotestate/gitrepo/provider_test.go
package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareOrigin creates an empty bare store with a seed commit on main, the way
// a freshly created team state repo looks after `git init` + first push.
func bareOrigin(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, t.TempDir(), "clone", "-q", origin, seed)
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, seed, "push", "-q", "origin", "main")
	return origin
}

func machine(t *testing.T, origin string) *Provider {
	t.Helper()
	return New(Options{ClonePath: filepath.Join(t.TempDir(), "projects", "team", "wb-state"), CloneURL: origin})
}

func snap(login, machine string, at time.Time) remotestate.Snapshot {
	return remotestate.Build(remotestate.Snapshot{Login: login, Machine: machine, PublishedAt: at, WBVersion: "test", ProjectsRoot: "/p"}, nil, nil, remotestate.RedactNone)
}

func TestPublishClonesWritesCommitsAndPushes(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	result, err := p.Publish(context.Background(), snap("alice", "laptop", at))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Location) != 40 {
		t.Fatalf("Location = %q, want commit sha", result.Location)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"README.md", "machines/alice/laptop/snapshot.yaml"} {
		if !strings.Contains(files, want) {
			t.Fatalf("origin tree %q lacks %s", files, want)
		}
	}
	if msg := gitIn(t, origin, "log", "-1", "--format=%s", "main"); msg != "wb: publish alice/laptop @ 2026-08-23T12:00:00Z" {
		t.Fatalf("commit message = %q", msg)
	}
}

func TestPublishUnchangedSnapshotMakesNoCommit(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	first, err := p.Publish(context.Background(), snap("alice", "laptop", at))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Publish(context.Background(), snap("alice", "laptop", at))
	if err != nil {
		t.Fatal(err)
	}
	if first.Location != second.Location {
		t.Fatalf("identical snapshot produced a new commit %s (was %s)", second.Location, first.Location)
	}
}

func TestTwoMachinesPublishConcurrentlyViaRebase(t *testing.T) {
	origin := bareOrigin(t)
	a := machine(t, origin)
	b := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	// Both fetch the same base, then publish in turn: b's push must rebase
	// over a's commit instead of failing.
	if err := a.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Publish(context.Background(), snap("alice", "laptop", at)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Publish(context.Background(), snap("bob", "vm", at.Add(time.Minute))); err != nil {
		t.Fatalf("second machine publish: %v", err)
	}

	entries, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Snapshot.Key())
	}
	if strings.Join(keys, " ") != "alice/laptop bob/vm" {
		t.Fatalf("List keys = %v (List must Fetch first and sort by key)", keys)
	}
}

func TestListSurfacesCorruptEntryAsError(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	// Corrupt another machine's file directly in a second clone.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	bad := filepath.Join(other, "machines", "carol", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "corrupt")
	gitIn(t, other, "push", "-q", "origin", "main")

	entries, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	carol := entries[1]
	if carol.Snapshot.Login != "carol" || carol.Snapshot.Machine != "desk" || !strings.Contains(carol.Error, "schema_version 99") {
		t.Fatalf("corrupt entry = %+v", carol)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/remotestate/gitrepo/ -v`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement the provider**

```go
// internal/remotestate/gitrepo/provider.go
// Package gitrepo stores machine snapshots in a git repository: one file per
// machine, history for free, no server.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// Options locate the state repository clone and its origin.
type Options struct {
	// ClonePath is <projects-root>/<owner>/<name>; created by cloning when absent.
	ClonePath string
	// CloneURL is what git clone receives when ClonePath does not exist yet.
	CloneURL string
}

// Provider implements remotestate.Provider over a git clone.
type Provider struct {
	opts Options
}

// New returns a provider; nothing touches disk until Publish/Fetch/List.
func New(opts Options) *Provider { return &Provider{opts: opts} }

// SnapshotPath is the store-relative path of one machine's snapshot.
func SnapshotPath(login, machine string) string {
	return path.Join("machines", login, machine, "snapshot.yaml")
}

const readme = `# WB remote state

Machine snapshots published by [wb remote publish](https://wb.sneat.dev).
One file per machine under machines/<login>/<machine>/snapshot.yaml.
Do not edit by hand; run wb remote status to read it.
`

// ensureClone clones the store when the clone path is missing.
func (p *Provider) ensureClone() error {
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, ".git")); err == nil {
		return nil
	}
	return gitops.Clone(p.opts.CloneURL, p.opts.ClonePath)
}

// Fetch clones if needed and rebases local state onto origin.
func (p *Provider) Fetch(_ context.Context) error {
	if err := p.ensureClone(); err != nil {
		return err
	}
	return gitops.PullRebase(p.opts.ClonePath)
}

// Publish writes the snapshot, commits, and pushes, rebasing once on a
// rejected push. An unchanged snapshot returns the current HEAD without a
// new commit.
func (p *Provider) Publish(ctx context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	if err := p.Fetch(ctx); err != nil {
		return remotestate.PublishResult{}, err
	}
	data, err := remotestate.Encode(snapshot)
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	rel := SnapshotPath(snapshot.Login, snapshot.Machine)
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return remotestate.PublishResult{}, err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return remotestate.PublishResult{}, err
	}
	readmePath := filepath.Join(p.opts.ClonePath, "README.md")
	if _, err := os.Stat(readmePath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
			return remotestate.PublishResult{}, err
		}
	}
	message := fmt.Sprintf("wb: publish %s @ %s", snapshot.Key(), snapshot.PublishedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	committed, err := gitops.AddCommit(p.opts.ClonePath, message, rel, "README.md")
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	if committed {
		if err := gitops.Push(p.opts.ClonePath); err != nil {
			if err := gitops.PullRebase(p.opts.ClonePath); err != nil {
				return remotestate.PublishResult{}, fmt.Errorf("push rejected and rebase failed; local commit kept: %w", err)
			}
			if err := gitops.Push(p.opts.ClonePath); err != nil {
				return remotestate.PublishResult{}, fmt.Errorf("push rejected twice; local commit kept for the next publish: %w", err)
			}
		}
	}
	sha, err := gitops.HeadSHA(p.opts.ClonePath)
	if err != nil {
		return remotestate.PublishResult{}, err
	}
	return remotestate.PublishResult{Location: sha}, nil
}

// List fetches and then reads every machines/<login>/<machine>/snapshot.yaml.
func (p *Provider) List(ctx context.Context) ([]remotestate.Entry, error) {
	if err := p.Fetch(ctx); err != nil {
		return nil, err
	}
	root := filepath.Join(p.opts.ClonePath, "machines")
	var entries []remotestate.Entry
	err := filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && file == root {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != "snapshot.yaml" {
			return nil
		}
		rel, _ := filepath.Rel(root, file)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
			return nil
		}
		entry := remotestate.Entry{Snapshot: remotestate.Snapshot{Login: parts[0], Machine: parts[1]}}
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			entry.Error = readErr.Error()
		} else if snapshot, decodeErr := remotestate.Decode(data); decodeErr != nil {
			entry.Error = decodeErr.Error()
		} else {
			snapshot.Login, snapshot.Machine = parts[0], parts[1]
			entry.Snapshot = snapshot
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Snapshot.Key() < entries[j].Snapshot.Key() })
	return entries, nil
}
```

- [ ] **Step 4: (no-op)** — provider selection is deliberately not in this package; see Task 6 `openRemote`.

- [ ] **Step 5: Run tests**

Run: `go fmt ./... && go test ./internal/remotestate/... -v`
Expected: PASS (4 gitrepo tests + earlier ones).

- [ ] **Step 6: Commit**

```bash
git add internal/remotestate/gitrepo
git commit -m "feat(remotestate): git repository provider with rebase-on-reject publish"
```

---

### Task 6: `wb remote` group, snapshot collection, and `wb remote publish`

**Files:**
- Create: `cmd/wb/remote.go`, `cmd/wb/remote_collect.go`, `cmd/wb/remote_publish.go`, `cmd/wb/remote_test.go`
- Modify: `cmd/wb/main.go:106` (add `root.AddCommand(newRemoteCmd())`)

**Interfaces:**
- Consumes: `qualityTargets` / `runTargets` (`cmd/wb/quality.go`), `gitops.Status`, `gitops.Tracking`, `worktrees.List`, `discover.AuthUser`, `collectVersion().Version`, `remotestate.*`, `gitrepo.*`.
- Produces:
  ```go
  func newRemoteCmd() *cobra.Command
  type remoteDeps struct { configPath string; login func() (string, error); open func(remotestate.Config, string) (remotestate.Provider, error); now func() time.Time }
  func defaultRemoteDeps() remoteDeps
  func loadRemote(deps remoteDeps) (remotestate.Config, remotestate.Provider, error)   // maps UnconfiguredError → exitError{code: exitUsage}
  func collectSnapshot(projectsRoot, filter string, parallel int, identity remotestate.Snapshot, redaction remotestate.Redaction) (remotestate.Snapshot, error)
  func runRemotePublish(deps remoteDeps, projectsRoot, filter string, parallel int, dryRun, jsonOut bool, out io.Writer) error
  ```
  Tests inject `deps` so no `gh` or GitHub access is needed.

- [ ] **Step 1: Write the failing tests**

```go
// cmd/wb/remote_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
)

func remoteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteFixture builds a projects root holding one dirty fleet repo, a bare
// state-repo origin, and a wb.yaml pointing at it.
type remoteFixture struct {
	projectsRoot, origin, configPath string
}

func newRemoteFixture(t *testing.T, machine string) remoteFixture {
	t.Helper()
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	repo := filepath.Join(projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, repo, "init", "-q", "-b", "main")
	remoteGit(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(base, "origin.git")
	remoteGit(t, base, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	remoteGit(t, base, "clone", "-q", origin, seed)
	remoteGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	remoteGit(t, seed, "push", "-q", "origin", "main")
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", filepath.Join(base, "wbhome"))
	return remoteFixture{projectsRoot: projectsRoot, origin: origin, configPath: configPath}
}

func (f remoteFixture) deps(login string, at time.Time) remoteDeps {
	return remoteDeps{
		configPath: f.configPath,
		login:      func() (string, error) { return login, nil },
		open: func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
			return gitrepo.New(gitrepo.Options{ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()), CloneURL: f.origin}), nil
		},
		now: func() time.Time { return at },
	}
}

func TestRemotePublishUnconfiguredIsUsageError(t *testing.T) {
	deps := defaultRemoteDeps()
	deps.configPath = filepath.Join(t.TempDir(), "none.yaml")
	var out bytes.Buffer
	err := runRemotePublish(deps, t.TempDir(), "", 2, false, false, &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("err = %v, want usage error with snippet", err)
	}
}

func TestRemotePublishWritesSnapshotToStore(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, true, &out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Key                 string `json:"key"`
		RepositoriesScanned int    `json:"repositories_scanned"`
		Attention           int    `json:"attention"`
		Worktrees           int    `json:"worktrees"`
		Location            string `json:"location"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("json: %v: %s", err, out.String())
	}
	if report.Key != "alice/laptop" || report.RepositoriesScanned != 1 || report.Attention != 1 || len(report.Location) != 40 {
		t.Fatalf("report = %+v", report)
	}
	stored := remoteGit(t, f.origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if !strings.Contains(stored, "repository: acme/widgets") || !strings.Contains(stored, "- dirty.txt") {
		t.Fatalf("stored snapshot = %s", stored)
	}
}

func TestRemotePublishDryRunTouchesNothing(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", time.Now()), f.projectsRoot, "", 2, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acme/widgets") {
		t.Fatalf("dry-run should print the snapshot: %s", out.String())
	}
	if files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "machines/") {
		t.Fatalf("dry-run published: %s", files)
	}
	if _, err := os.Stat(filepath.Join(f.projectsRoot, "team", "wb-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run must not clone the state repo")
	}
}

var _ = context.Background
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/wb/ -run TestRemotePublish -v`
Expected: FAIL — undefined `remoteDeps`, `runRemotePublish`.

- [ ] **Step 3: Implement the group and shared deps**

```go
// cmd/wb/remote.go
package main

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// remoteDeps are the seams tests replace: config location, GitHub login,
// provider construction, and the clock.
type remoteDeps struct {
	configPath string
	login      func() (string, error)
	open       func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error)
	now        func() time.Time
}

func defaultRemoteDeps() remoteDeps {
	return remoteDeps{
		configPath: wbconfig.DefaultPath(),
		login:      discover.AuthUser,
		open:       openRemote,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// openRemote selects the provider named by cfg. It lives here rather than in
// remotestate to keep the provider packages free of an import cycle.
func openRemote(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
	switch cfg.Provider {
	case "git":
		return gitrepo.New(gitrepo.Options{
			ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()),
			CloneURL:  "git@github.com:" + cfg.Repo + ".git",
		}), nil
	default:
		return nil, &exitError{code: exitUsage, message: "remote.provider " + cfg.Provider + " is not supported"}
	}
}

// loadRemote reads config and opens the provider, mapping "not configured"
// to the usage exit code so the snippet reaches the operator.
func loadRemote(deps remoteDeps, projectsRoot string) (remotestate.Config, remotestate.Provider, error) {
	cfg, err := remotestate.LoadConfig(deps.configPath)
	var unconfigured *remotestate.UnconfiguredError
	if errors.As(err, &unconfigured) {
		return cfg, nil, &exitError{code: exitUsage, message: err.Error()}
	}
	if err != nil {
		return cfg, nil, &exitError{code: exitUsage, message: err.Error()}
	}
	provider, err := deps.open(cfg, projectsRoot)
	return cfg, provider, err
}

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Publish this machine's fleet state and read other machines' state",
		Long: `wb remote shares WB fleet state across machines through a store
configured in ~/.config/wb/wb.yaml:

` + remotestate.ConfigSnippet + `

  wb remote publish    scan this machine and publish its snapshot
  wb remote status     cross-machine worklist from the store
  wb remote machines   one line per machine with publish age`,
	}
	cmd.AddCommand(newRemotePublishCmd())
	cmd.AddCommand(newRemoteStatusCmd())
	cmd.AddCommand(newRemoteMachinesCmd())
	return cmd
}
```

Leave `newRemoteStatusCmd` and `newRemoteMachinesCmd` out of `newRemoteCmd` until Task 7 adds them (otherwise this task does not compile). Add `root.AddCommand(newRemoteCmd())` after line 106 in `cmd/wb/main.go`.

- [ ] **Step 4: Implement snapshot collection**

```go
// cmd/wb/remote_collect.go
package main

import (
	"context"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// collectSnapshot scans the local fleet the way wb fleet status does and
// lists live task worktrees, then assembles the snapshot to publish.
func collectSnapshot(projectsRoot, filter string, parallel int, identity remotestate.Snapshot, redaction remotestate.Redaction) (remotestate.Snapshot, error) {
	targets, err := qualityTargets("", projectsRoot, filter, qualityOptions{fleet: true, parallel: parallel})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	inputs := make([]remotestate.RepositoryInput, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		input := remotestate.RepositoryInput{Repository: target.repository, Path: target.path}
		if input.Status, input.Err = gitops.Status(target.path); input.Err != nil {
			inputs[index] = input
			return
		}
		input.Tracking, input.Err = gitops.Tracking(target.path)
		inputs[index] = input
	})
	wts, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: projectsRoot, Filter: filter, OwnerState: "active"})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	identity.ProjectsRoot = projectsRoot
	return remotestate.Build(identity, inputs, wts, redaction), nil
}
```

Check `gitops.Tracking` on a repo with no remote (the fixture's `acme/widgets`): read `internal/gitops/gitops.go:321-367`. If it returns an error for "no upstream configured", treat that as `TrackingState{Branch: <current>}` rather than an `Err` — the spec lists missing upstream as attention, not as a scan failure. Adjust `collectSnapshot` accordingly and add a fixture assertion in `TestRemotePublishWritesSnapshotToStore` that the stored YAML has `status: attention`, not `status: error`.

- [ ] **Step 5: Implement publish**

```go
// cmd/wb/remote_publish.go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemotePublishCmd() *cobra.Command {
	var dryRun, jsonOut bool
	var parallel int
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Scan this machine's fleet and publish the snapshot to the remote store",
		Long: `Scans every clone under --projects-root (honouring --filter), lists live
task worktrees, and publishes one snapshot keyed <login>/<machine>.
--dry-run prints the snapshot and writes nothing, locally or remotely.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemotePublish(defaultRemoteDeps(), projectsRoot, filterFlag, parallel, dryRun, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the snapshot; publish nothing")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the publish report as JSON")
	cmd.Flags().IntVar(&parallel, "parallel", 8, "max concurrent repository scans")
	return cmd
}

type remotePublishReport struct {
	Key                 string `json:"key"`
	RepositoriesScanned int    `json:"repositories_scanned"`
	Attention           int    `json:"attention"`
	Worktrees           int    `json:"worktrees"`
	Location            string `json:"location,omitempty"`
	DryRun              bool   `json:"dry_run,omitempty"`
}

func runRemotePublish(deps remoteDeps, projectsRoot, filter string, parallel int, dryRun, jsonOut bool, out io.Writer) error {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	login, err := deps.login()
	if err != nil || login == "" {
		return &exitError{code: exitUsage, message: fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err)}
	}
	identity := remotestate.Snapshot{Login: login, Machine: cfg.Machine, PublishedAt: deps.now(), WBVersion: collectVersion().Version}
	snapshot, err := collectSnapshot(projectsRoot, filter, parallel, identity, cfg.Publish.Unpushed)
	if err != nil {
		return err
	}
	report := remotePublishReport{Key: snapshot.Key(), RepositoriesScanned: snapshot.RepositoriesScanned, Attention: len(snapshot.Repositories), Worktrees: len(snapshot.Worktrees), DryRun: dryRun}
	if dryRun {
		data, err := remotestate.Encode(snapshot)
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(out).Encode(snapshot)
		}
		_, err = out.Write(data)
		return err
	}
	result, err := provider.Publish(cmdContext(), snapshot)
	if err != nil {
		return &exitError{code: exitFindings, message: "publish: " + err.Error()}
	}
	report.Location = result.Location
	if jsonOut {
		return json.NewEncoder(out).Encode(report)
	}
	_, err = fmt.Fprintf(out, "published %s: %d repositories scanned, %d need attention, %d worktrees → %s\n",
		report.Key, report.RepositoriesScanned, report.Attention, report.Worktrees, report.Location)
	return err
}
```

`cmdContext()` — check `cmd/wb` for an existing context helper (`grep -n "context.Background\|func .*Context" cmd/wb/*.go`). If none, replace `cmdContext()` with `context.Background()` and import `context`.

- [ ] **Step 6: Run tests**

Run: `go fmt ./... && go build ./... && go test ./cmd/wb/ -run 'TestRemotePublish|TestAgentSkillsCoverPublicCommands' -v`
Expected: the three publish tests PASS; `TestAgentSkillsCoverPublicCommands` now FAILS with `public command "remote" has no Agent Skill coverage` — expected until Task 9. Do not skip or weaken that test.

- [ ] **Step 7: Commit**

```bash
git add cmd/wb/remote.go cmd/wb/remote_collect.go cmd/wb/remote_publish.go cmd/wb/remote_test.go cmd/wb/main.go
git commit -m "feat(remote): wb remote publish"
```

---

### Task 7: `wb remote status` and `wb remote machines`

**Files:**
- Create: `cmd/wb/remote_status.go`, `cmd/wb/remote_machines.go`, `cmd/wb/remote_render.go`
- Modify: `cmd/wb/remote.go` (register the two subcommands), `cmd/wb/remote_test.go` (append tests)

**Interfaces:**
- Consumes: Task 6 `remoteDeps`, `loadRemote`; `remotestate.Entry`.
- Produces:
  ```go
  func runRemoteStatus(deps remoteDeps, projectsRoot string, stale time.Duration, machine string, jsonOut bool, out io.Writer) error
  func runRemoteMachines(deps remoteDeps, projectsRoot string, stale time.Duration, jsonOut bool, out io.Writer) error
  type remoteMachineRow struct { Key string; PublishedAt time.Time; Age string; Stale bool; WBVersion string; Attention, Worktrees int; Error string }
  func machineRows(entries []remotestate.Entry, now time.Time, stale time.Duration) []remoteMachineRow
  ```

- [ ] **Step 1: Write the failing tests** (append to `cmd/wb/remote_test.go`)

```go
func publishTwo(t *testing.T) (remoteFixture, time.Time) {
	t.Helper()
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, false, &out); err != nil {
		t.Fatal(err)
	}
	// Second machine: same store, a different projects root and machine name.
	g := remoteFixture{projectsRoot: filepath.Join(t.TempDir(), "projects"), origin: f.origin, configPath: filepath.Join(t.TempDir(), "wb.yaml")}
	if err := os.MkdirAll(g.projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.configPath, []byte("remote:\n  repo: team/wb-state\n  machine: vm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRemotePublish(g.deps("bob", at.Add(-48*time.Hour)), g.projectsRoot, "", 2, false, false, &out); err != nil {
		t.Fatal(err)
	}
	return f, at
}

func TestRemoteMachinesFlagsStaleEntries(t *testing.T) {
	f, at := publishTwo(t)
	var out bytes.Buffer
	if err := runRemoteMachines(f.deps("alice", at.Add(time.Hour)), f.projectsRoot, 24*time.Hour, true, &out); err != nil {
		t.Fatal(err)
	}
	var rows []remoteMachineRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("json: %v: %s", err, out.String())
	}
	if len(rows) != 2 || rows[0].Key != "alice/laptop" || rows[0].Stale || rows[1].Key != "bob/vm" || !rows[1].Stale {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRemoteStatusRendersCrossMachineWorklist(t *testing.T) {
	f, at := publishTwo(t)
	var out bytes.Buffer
	if err := runRemoteStatus(f.deps("alice", at.Add(time.Hour)), f.projectsRoot, 24*time.Hour, "", false, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"alice/laptop", "acme/widgets", "1 untracked file", "bob/vm", "STALE"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status output lacks %q:\n%s", want, text)
		}
	}
}

func TestRemoteStatusMachineFilterAndErrorRowsDoNotFail(t *testing.T) {
	f, at := publishTwo(t)
	other := filepath.Join(t.TempDir(), "other")
	remoteGit(t, t.TempDir(), "clone", "-q", f.origin, other)
	bad := filepath.Join(other, "machines", "carol", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, other, "add", "-A")
	remoteGit(t, other, "commit", "-q", "-m", "corrupt")
	remoteGit(t, other, "push", "-q", "origin", "main")

	var out bytes.Buffer
	if err := runRemoteStatus(f.deps("alice", at), f.projectsRoot, 24*time.Hour, "", true, &out); err != nil {
		t.Fatalf("error rows must not fail the command: %v", err)
	}
	if !strings.Contains(out.String(), "schema_version 99") {
		t.Fatalf("error row missing: %s", out.String())
	}

	out.Reset()
	if err := runRemoteStatus(f.deps("alice", at), f.projectsRoot, 24*time.Hour, "bob/vm", false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "alice/laptop") || !strings.Contains(out.String(), "bob/vm") {
		t.Fatalf("--machine filter not applied: %s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/wb/ -run 'TestRemoteMachines|TestRemoteStatus' -v`
Expected: FAIL — undefined `runRemoteMachines`, `runRemoteStatus`, `remoteMachineRow`.

- [ ] **Step 3: Implement rendering helpers**

```go
// cmd/wb/remote_render.go
package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

type remoteMachineRow struct {
	Key         string    `json:"key"`
	PublishedAt time.Time `json:"published_at"`
	Age         string    `json:"age"`
	Stale       bool      `json:"stale"`
	WBVersion   string    `json:"wb_version,omitempty"`
	Attention   int       `json:"attention"`
	Worktrees   int       `json:"worktrees"`
	Error       string    `json:"error,omitempty"`
}

func machineRows(entries []remotestate.Entry, now time.Time, stale time.Duration) []remoteMachineRow {
	rows := make([]remoteMachineRow, 0, len(entries))
	for _, entry := range entries {
		row := remoteMachineRow{Key: entry.Snapshot.Key(), Error: entry.Error}
		if entry.Error == "" {
			age := now.Sub(entry.Snapshot.PublishedAt)
			row.PublishedAt = entry.Snapshot.PublishedAt
			row.Age = humanAge(age)
			row.Stale = stale > 0 && age > stale
			row.WBVersion = entry.Snapshot.WBVersion
			row.Attention = len(entry.Snapshot.Repositories)
			row.Worktrees = len(entry.Snapshot.Worktrees)
		}
		rows = append(rows, row)
	}
	return rows
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func writeMachinesTable(out io.Writer, rows []remoteMachineRow) {
	fmt.Fprintf(out, "%-32s %-10s %-6s %-10s %9s %9s\n", "MACHINE", "PUBLISHED", "STALE", "WB", "ATTENTION", "WORKTREES")
	for _, row := range rows {
		if row.Error != "" {
			fmt.Fprintf(out, "%-32s error: %s\n", row.Key, row.Error)
			continue
		}
		stale := ""
		if row.Stale {
			stale = "STALE"
		}
		fmt.Fprintf(out, "%-32s %-10s %-6s %-10s %9d %9d\n", row.Key, row.Age, stale, row.WBVersion, row.Attention, row.Worktrees)
	}
}

func writeStatusWorklist(out io.Writer, entries []remotestate.Entry, rows []remoteMachineRow) {
	for i, entry := range entries {
		row := rows[i]
		header := fmt.Sprintf("## %s", row.Key)
		if row.Stale {
			header += " STALE"
		}
		if row.Error != "" {
			fmt.Fprintf(out, "%s\n  error: %s\n\n", header, row.Error)
			continue
		}
		fmt.Fprintf(out, "%s (published %s ago, %d scanned)\n", header, row.Age, entry.Snapshot.RepositoriesScanned)
		for _, repo := range entry.Snapshot.Repositories {
			tracking := repo.Branch
			if repo.Ahead > 0 || repo.Behind > 0 {
				tracking += fmt.Sprintf(" +%d/-%d", repo.Ahead, repo.Behind)
			}
			if repo.Branch != "" && repo.Upstream == "" {
				tracking += " (no upstream)"
			}
			detail := repo.Summary
			if repo.Error != "" {
				detail = "error: " + repo.Error
			}
			fmt.Fprintf(out, "  %-40s %-28s %s\n", repo.Repository, strings.TrimSpace(tracking), detail)
		}
		for _, wt := range entry.Snapshot.Worktrees {
			fmt.Fprintf(out, "  worktree %-30s %-40s %s\n", wt.Task, wt.Repository, wt.Branch)
		}
		if len(entry.Snapshot.Repositories) == 0 && len(entry.Snapshot.Worktrees) == 0 {
			fmt.Fprintln(out, "  clean")
		}
		fmt.Fprintln(out)
	}
}
```

- [ ] **Step 4: Implement the two commands**

```go
// cmd/wb/remote_machines.go
package main

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newRemoteMachinesCmd() *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	cmd := &cobra.Command{
		Use:   "machines",
		Short: "List every machine in the remote store with its publish age",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemoteMachines(defaultRemoteDeps(), projectsRoot, stale, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print rows as JSON")
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	return cmd
}

func runRemoteMachines(deps remoteDeps, projectsRoot string, stale time.Duration, jsonOut bool, out io.Writer) error {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	entries, err := provider.List(context.Background())
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	rows := machineRows(entries, deps.now(), stale)
	if jsonOut {
		return json.NewEncoder(out).Encode(rows)
	}
	writeMachinesTable(out, rows)
	return nil
}
```

```go
// cmd/wb/remote_status.go
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func newRemoteStatusCmd() *cobra.Command {
	var jsonOut bool
	var stale time.Duration
	var machine string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Cross-machine worklist: every machine's attention repositories and worktrees",
		Long: `Reads the remote store and renders one section per machine. The local
machine is shown as last published, not re-scanned: wb status stays the live
local view. Entries that cannot be decoded are rendered as error rows and do
not change the exit code.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRemoteStatus(defaultRemoteDeps(), projectsRoot, stale, machine, jsonOut, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print entries as JSON")
	cmd.Flags().DurationVar(&stale, "stale", 24*time.Hour, "flag machines whose snapshot is older than this")
	cmd.Flags().StringVar(&machine, "machine", "", "only this <login>/<machine>")
	return cmd
}

type remoteStatusReport struct {
	Machines []remoteMachineRow  `json:"machines"`
	Entries  []remotestate.Entry `json:"entries"`
}

func runRemoteStatus(deps remoteDeps, projectsRoot string, stale time.Duration, machine string, jsonOut bool, out io.Writer) error {
	_, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return err
	}
	entries, err := provider.List(context.Background())
	if err != nil {
		return &exitError{code: exitFindings, message: "read remote store: " + err.Error()}
	}
	if machine != "" {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Snapshot.Key() == machine {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	rows := machineRows(entries, deps.now(), stale)
	if jsonOut {
		return json.NewEncoder(out).Encode(remoteStatusReport{Machines: rows, Entries: entries})
	}
	writeStatusWorklist(out, entries, rows)
	return nil
}
```

Add `"context"` to the imports of `remote_machines.go`. Register both in `newRemoteCmd` (`cmd/wb/remote.go`).

- [ ] **Step 5: Run tests**

Run: `go fmt ./... && go test ./cmd/wb/ -run 'TestRemote' -v`
Expected: all `TestRemote*` PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/wb/remote.go cmd/wb/remote_render.go cmd/wb/remote_status.go cmd/wb/remote_machines.go cmd/wb/remote_test.go
git commit -m "feat(remote): wb remote status and wb remote machines"
```

---

### Task 8: `wb sync --publish`

**Files:**
- Modify: `cmd/wb/sync.go:37-39` (flag), `cmd/wb/sync.go:73` (`runSync` signature and tail)
- Test: `cmd/wb/remote_test.go` (append)

**Interfaces:**
- Changes `runSync(projectsRoot, filter string, only []string, workers int, dryRun bool) int` to `runSync(projectsRoot, filter string, only []string, workers int, dryRun, publish bool, deps remoteDeps) int`. Update every caller (`grep -n "runSync(" cmd/wb/*.go`).

- [ ] **Step 1: Write the failing test**

```go
func TestSyncPublishFlagIsRegistered(t *testing.T) {
	cmd := newSyncCmd()
	flag := cmd.Flags().Lookup("publish")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("--publish flag = %+v, want bool default false", flag)
	}
}

func TestSyncPublishAfterSuccessfulSyncDoesNotChangeExitCode(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	deps := f.deps("alice", time.Now().UTC())
	deps.open = func(remotestate.Config, string) (remotestate.Provider, error) {
		return nil, errors.New("store unreachable")
	}
	// No repos discoverable without gh: runSync returns 0 with "no repos found",
	// and publish must then fail without turning that into a non-zero exit.
	if code := runSync(f.projectsRoot, "zzz-no-match", nil, 1, true, true, deps); code != 0 {
		t.Fatalf("exit = %d, want 0: a publish failure is reported, not fatal", code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/wb/ -run TestSyncPublish -v`
Expected: FAIL — flag missing / wrong `runSync` arity.

- [ ] **Step 3: Implement**

In `newSyncCmd` add:

```go
var publish bool
cmd.Flags().BoolVar(&publish, "publish", false, "after a successful sync, run wb remote publish")
```

and pass `publish, defaultRemoteDeps()` into `runSync`. In `runSync`, replace the final `if hasErrors { return 1 }; return 0` with:

```go
	if hasErrors {
		return 1
	}
	if publish {
		if err := runRemotePublish(deps, projectsRoot, filter, workers, false, false, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "remote publish failed (sync itself succeeded):", err)
		}
	}
	return 0
```

Also move the `if len(repos) == 0 { fmt.Println("no repos found"); return 0 }` early return so that it falls through to the same publish block (publishing an empty fleet is still a valid "this machine is clean" signal). Simplest: replace that `return 0` with a jump to a shared tail — restructure as:

```go
	var results []fleetsync.Result
	if len(repos) == 0 {
		fmt.Println("no repos found")
	} else {
		... existing TUI/plain branch, summary, results browser ...
	}
	hasErrors := false
	for _, res := range results { if res.Status == fleetsync.Failed { hasErrors = true } }
	if hasErrors { return 1 }
	if publish { ...as above... }
	return 0
```

Keep `needsReview`/results browser logic inside the `else`.

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go test ./cmd/wb/ -run 'TestSync' -v`
Expected: PASS, including pre-existing sync tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/wb/sync.go cmd/wb/remote_test.go
git commit -m "feat(sync): --publish runs wb remote publish after a clean sync"
```

---

### Task 9: Skill, feature spec, capability manifest, docs

This task makes `TestAgentSkillsCoverPublicCommands` and `TestCapabilityManifestKeepsImplementationHelpAndSkillsInOne` green again and documents the commands.

**Files:**
- Create: `spec/features/remote-state/README.md`, `ai/skills/wb-fleet/references/remote.md`
- Modify: `spec/features/README.md` (table row), `ai/skills/wb-fleet/SKILL.md` (table rows), `ai/skills/commands.json` (`"remote": ["wb-fleet"]`), `ai/capabilities.json` (three entries), `README.md` (Commands block + a short section), `docs/cli-flag-matrix.md` (row)

- [ ] **Step 1: Run the gating tests to see the exact failures**

Run: `go test ./cmd/wb/ -run 'TestAgentSkillsCoverPublicCommands|TestCapabilityManifest' -v`
Expected: FAIL with `public command "remote" has no Agent Skill coverage`.

- [ ] **Step 2: Feature spec**

Create `spec/features/remote-state/README.md`, copying the front matter and Studio link line from `spec/features/fleet-status/README.md` with the slug `remote-state`:

```markdown
---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Remote State

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb remote` shares fleet state across machines through a pluggable store:

- `wb remote publish` — scan this machine (attention repositories + live task
  worktrees) and publish one snapshot keyed `<login>/<machine>`
- `wb remote status` — cross-machine worklist from the store, with `STALE`
  flags for old snapshots and error rows for undecodable entries
- `wb remote machines` — one line per machine with publish age
- `wb sync --publish` — publish after a successful sync

The only provider is `git`: a team (or personal) repository holding
`machines/<login>/<machine>/snapshot.yaml`, cloned to the canonical fleet
location. Design: `docs/superpowers/specs/2026-08-23-remote-state-design.md`.

## Problem

WB sees one machine. Reconciliation across a laptop, a VM, and teammates'
machines — "is there unpushed work anywhere?", "who has a worktree on
task-7?" — needs every machine's state in one place with history.

## Acceptance criteria

- Publishing writes only the state repository clone; never another clone.
- An unchanged snapshot produces no commit.
- Two machines publishing concurrently both land (rebase on rejection, once).
- `machine` must be configured explicitly; there is no hostname fallback.
- `wb remote status` exits 0 when some entries are undecodable.
- No import of any synchestra module.
```

Add to the table in `spec/features/README.md`: `| [Remote State](remote-state/README.md) | Implementing | \`wb remote\` shares fleet state across machines through a pluggable store: |` (match the existing row format exactly — look at the line for Fleet Status).

- [ ] **Step 3: Skill reference**

Create `ai/skills/wb-fleet/references/remote.md`:

```markdown
# Share fleet state across machines

Configure once in `~/.config/wb/wb.yaml`:

```yaml
remote:
  provider: git
  repo: <owner>/wb-state        # team or personal private repository
  machine: <unique-name>        # required; unique per GitHub login
  publish:
    unpushed: subjects          # or counts, to hide commit subjects
```

| Need | Command |
|---|---|
| Publish this machine's attention repositories and worktrees | `wb remote publish` |
| Preview without publishing | `wb remote publish --dry-run` |
| Cross-machine worklist | `wb remote status` |
| One line per machine with publish age | `wb remote machines --json` |
| Publish after syncing | `wb sync --publish` |

The store is a git repository: one `machines/<login>/<machine>/snapshot.yaml`
per machine, so history is the audit trail. Snapshots older than `--stale`
(default 24h) are flagged `STALE`. Entries that cannot be decoded are shown as
error rows and do not change the exit code. Exit `2` means the `remote`
section is missing; the message includes the snippet to add.
```

Add two rows to the table in `ai/skills/wb-fleet/SKILL.md`:

```
| Publish this machine's state for other machines | `wb remote publish` | [remote.md](references/remote.md) |
| See every machine's attention worklist | `wb remote status` / `wb remote machines` | [remote.md](references/remote.md) |
```

In `ai/skills/commands.json` add `"remote": ["wb-fleet"]` after `"repo"`.

- [ ] **Step 4: Capability manifest**

Add three entries to `ai/capabilities.json`, **inserted so that IDs stay sorted** (`wb.remote.machines`, `wb.remote.publish`, `wb.remote.status` go between `wb.layout.*`/`wb.migrate.*` and `wb.repo.*` — check neighbours with `grep -n '"id": "wb\.' ai/capabilities.json`). Model each on the `wb.fleet.status` entry shown below; flags must match the real `--help` output exactly and `tests.references` must name real test functions from Tasks 6–7.

```json
{
  "id": "wb.remote.publish",
  "feature_refs": ["spec/features/remote-state/README.md"],
  "since": "unreleased",
  "surfaces": {
    "runtime": {
      "status": "Full",
      "commands": [{
        "path": "wb remote publish",
        "flags": ["--dry-run", "--json", "--parallel"],
        "modes": [
          "Scans the projects-root fleet plus live task worktrees and publishes one snapshot keyed <login>/<machine>.",
          "--dry-run prints the snapshot and writes nothing, locally or remotely."
        ]
      }],
      "limitation": null
    },
    "help": {
      "status": "Full",
      "anchors": [{"command": "wb remote publish --help", "contains": ["--dry-run", "--json", "<login>/<machine>"]}],
      "limitation": null
    },
    "ai_skill": {
      "status": "Full",
      "skills": [{"path": "ai/skills/wb-fleet/references/remote.md", "marker": "# Share fleet state across machines", "examples": ["wb remote publish --dry-run"]}],
      "limitation": null
    },
    "tests": {
      "status": "Full",
      "references": [{"path": "cmd/wb/remote_test.go", "name": "TestRemotePublishWritesSnapshotToStore", "kind": "integration"}],
      "limitation": null
    }
  },
  "notes": "Only the state repository clone is written; fleet clones are read."
}
```

Repeat for `wb.remote.status` (flags `--json`, `--machine`, `--stale`; help anchors `--stale`, `--machine`; example `wb remote status --stale 12h`; test `TestRemoteStatusRendersCrossMachineWorklist`) and `wb.remote.machines` (flags `--json`, `--stale`; example `wb remote machines --json`; test `TestRemoteMachinesFlagsStaleEntries`). Also add `--publish` to the existing `wb.sync` capability's flags list and a mode line `"--publish runs wb remote publish after a clean sync; a publish failure is reported and does not change the sync exit code."`

- [ ] **Step 5: User docs**

In `README.md` Commands block add, after the `wb status` line:

```
wb remote publish|status|machines # share fleet state across machines via a git state repo
```

and add `--publish` to the `wb sync` line's comment if space allows. Add a short section after "### `wb worktree` — isolated feature branches" titled `### \`wb remote\` — fleet state across machines` containing the YAML snippet from `remotestate.ConfigSnippet` and three sentences: what publish does, what status shows, that the store is a private git repo with one file per machine.

In `docs/cli-flag-matrix.md` add the row:

```
| `remote publish`, `remote status`, `remote machines` | yes | `publish` only | rejected | yes |
```

Verify the `--org` claim: if `wb remote publish --org x` is not explicitly rejected by `cmd/wb/main.go`'s persistent-flag matrix, either register the rejection the same way neighbouring commands do (`grep -n "rejected\|unsupportedRootFlags" cmd/wb/main.go`) or change the cell to `ignored` only if the matrix has such a value. The matrix's own test (`cmd/wb/main_test.go`) will tell you.

- [ ] **Step 6: Run the gating tests**

Run: `go fmt ./... && go test ./cmd/wb/ -run 'TestAgentSkills|TestCapabilityManifest|TestRootFlag|TestFlagMatrix' -v`
Expected: PASS. If `assertHelpAnchor` fails, the `contains` strings must appear verbatim in `--help`; adjust the Long text or the anchor.

- [ ] **Step 7: Commit**

```bash
git add spec/features ai README.md docs/cli-flag-matrix.md
git commit -m "docs(remote): feature spec, agent skill, capability manifest, README"
```

---

### Task 10: Full verification and PR

- [ ] **Step 1: Format, lint, build, test**

Run, in this order:

```bash
go fmt ./...
golangci-lint run ./...
go build ./...
go test ./... 2>&1 | tail -30
```

Expected: no lint findings, build ok, every package `ok`.

- [ ] **Step 2: Coverage check**

```bash
go test ./... -coverprofile=/tmp/claude-1000/-home-ai-projects/6a4db747-18dd-4ce5-94f5-a73b812ef7a2/scratchpad/profile.cov >/dev/null
go tool cover -func=/tmp/claude-1000/-home-ai-projects/6a4db747-18dd-4ce5-94f5-a73b812ef7a2/scratchpad/profile.cov | grep -E 'remotestate|gitrepo|wbconfig|total:'
```

Expected: `internal/remotestate` and `gitrepo` each ≥ 90% of statements; total ≥ 58.0%. If a function in the new packages is below, add the missing table case rather than lowering the bar.

- [ ] **Step 3: Smoke the real binary**

```bash
go run ./cmd/wb remote --help
go run ./cmd/wb remote publish --help
WB_HOME=$(mktemp -d) HOME=$(mktemp -d) go run ./cmd/wb remote publish; echo "exit=$?"
```

Expected: help renders; the last command prints the "not configured" message with the YAML snippet and `exit=2`.

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin feat/remote-state
gh pr create --base main --title "feat(remote): share fleet state across machines" --body "Implements docs/superpowers/specs/2026-08-23-remote-state-design.md (#133).

- internal/remotestate: snapshot model, config, provider interface
- internal/remotestate/gitrepo: git-backed provider (one file per machine, rebase-on-reject)
- wb remote publish | status | machines; wb sync --publish
- feature spec, agent skill, capability manifest

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_011p9CekV8Ao4aNtT75PDQP9"
```

Then watch CI (`gh pr checks --watch`) and, per the user's PR rule, merge when green, delete the branch, pull `main`, and `go install ./cmd/wb` to refresh the installed binary.

---

## Self-review against the spec

- Snapshot model, non-clean rule, `repositories_scanned`, redaction → Task 2. ✔
- Provider interface, `Entry` error rows → Task 3 (+ Task 5 List). ✔
- Git layout, README, commit message, rebase-retry-once, clone at canonical path → Task 5. ✔
- Config section, `machine` required, validation, snippet on unconfigured → Task 3; usage exit code → Task 6 `loadRemote`. ✔
- `publish --dry-run/--json`, `status --stale/--machine/--json`, `machines --json`, `sync --publish` → Tasks 6–8. ✔
- Failure table: unconfigured (2), no login (2), clone failure (1 via provider error), push rejected twice (1, commit kept), corrupt entry (row), per-repo scan error (row) → Tasks 5–7. ✔
- No synchestra import; no daemon. ✔
- Skill/capability/feature gating → Task 9. ✔
- Known gaps the implementer must resolve in place (called out inline): `gitops.Tracking` behaviour without a remote (Task 6 step 4), `cmdContext()` existence (Task 6 step 5), `--org` cell in the flag matrix (Task 9 step 5).
