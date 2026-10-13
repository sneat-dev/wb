package orchestrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandingCleanupRefusesUnverifiableLinkEvidence(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"malformed workspace", "corrupt stream"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			projects, checkout := t.TempDir(), t.TempDir()
			var evidence string
			if fault == "malformed workspace" {
				evidence = filepath.Join(checkout, "go.work")
			} else {
				store, err := streams.Open(projects)
				if err != nil {
					t.Fatal(err)
				}
				evidence = filepath.Join(store.Root, "broken", "stream.json")
				if err := os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
					t.Fatal(err)
				}
			}
			original := []byte("{malformed\n")
			if err := os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
			refusal := refuseLinkedWorktree(projects, worktrees.ListResult{Task: "candidate", Repository: "acme/app", WorktreeDir: checkout})
			if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, evidence) || !strings.Contains(refusal.command, "inspect") {
				t.Fatalf("unverifiable %s allowed cleanup: %+v", fault, refusal)
			}
			raw, err := os.ReadFile(evidence)
			if err != nil || string(raw) != string(original) {
				t.Fatalf("evidence changed: %q %v", raw, err)
			}
			if info, err := os.Stat(checkout); err != nil || !info.IsDir() {
				t.Fatal("checkout deleted", err)
			}
		})
	}
}

func TestLandingDiagnosticExclusionRequiresReadableGitIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"candidate read fails", "malformed response", "parent repository", "detached", "different branch", "same source", "different repository", "source read fails", "source empty", "candidate common missing", "source common missing", "invalid source repository"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			candidate, common, otherCommon := t.TempDir(), t.TempDir(), t.TempDir()
			root, branch, candidateCommon := candidate, "candidate", common
			var candidateErr, sourceErr error
			sourceCommon, repository := common, "acme/app"
			switch kind {
			case "candidate read fails":
				candidateErr = errors.New("unreadable candidate")
			case "parent repository":
				root = filepath.Dir(candidate)
			case "detached":
				branch = "HEAD"
			case "different branch":
				branch = "unrelated"
			case "different repository":
				sourceCommon = otherCommon
			case "source read fails":
				sourceErr = errors.New("unreadable source")
			case "source empty":
				sourceCommon = ""
			case "candidate common missing":
				candidateCommon = filepath.Join(candidate, "missing")
			case "source common missing":
				sourceCommon = filepath.Join(candidate, "missing")
			case "invalid source repository":
				repository = "invalid"
			}
			identity := root + "\n" + candidateCommon + "\n" + branch + "\n"
			if kind == "malformed response" {
				identity = "partial\n"
			}
			fake := runnertest.New(t)
			fake.ExpectArgv([]string{"git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD"}, runner.Result{Stdout: identity}, candidateErr)
			if kind == "same source" || kind == "different repository" || strings.HasPrefix(kind, "source ") || kind == "candidate common missing" {
				fake.ExpectArgv([]string{"git", "rev-parse", "--path-format=absolute", "--git-common-dir"}, runner.Result{Stdout: sourceCommon}, sourceErr)
			}
			view := orchCovPullRequestView(t, `{"head":{"ref":"candidate","repo":{"full_name":"`+repository+`"}}}`)
			got := landingDiagnosticIsUnrelated(t.Context(), PullRequestLandOptions{ProjectsRoot: t.TempDir(), run: fake}, view, worktrees.ListDiagnostic{Path: candidate}, "")
			want := kind == "different branch" || kind == "different repository"
			if got != want {
				t.Fatalf("diagnostic exclusion=%t want=%t", got, want)
			}
		})
	}
}

func TestLandingCleanupRefusesUnresolvableLinkStorage(t *testing.T) {
	t.Parallel()
	projects := filepath.Join(t.TempDir(), "cycle")
	if err := os.Symlink(projects, projects); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout})
	if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, "could not be resolved") {
		t.Fatalf("unreadable stream home allowed landing: %+v", refusal)
	}
}

func TestLandingDiagnosticUsesValidatedCanonicalSpelling(t *testing.T) {
	t.Parallel()
	candidate, common, canonical := t.TempDir(), t.TempDir(), t.TempDir()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD"}, runner.Result{Stdout: candidate + "\n" + common + "\ncandidate\n"}, nil)
	fake.ExpectArgv([]string{"git", "rev-parse", "--path-format=absolute", "--git-common-dir"}, runner.Result{Stdout: common}, nil)
	view := orchCovPullRequestView(t, `{"head":{"ref":"candidate","repo":{"full_name":"Acme/App"}}}`)
	if landingDiagnosticIsUnrelated(t.Context(), PullRequestLandOptions{ProjectsRoot: t.TempDir(), run: fake}, view, worktrees.ListDiagnostic{Path: candidate}, canonical) {
		t.Fatal("source common directory was excluded")
	}
	if calls := fake.Calls(); len(calls) != 2 || calls[1].Dir != canonical {
		t.Fatalf("API casing changed the physical source path: %+v", calls)
	}
}

func TestLandingRetiredArtifactRequiresInspectionAndSourceRegistryProof(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"unregistered retirement", "unverified", "wrong kind", "staging", "task owned", "wrong disposition", "different path", "different root", "not sibling", "relative stage", "source unknown", "canonical unavailable", "registry error", "malformed registry", "missing path record", "relative registry path", "missing registered directory", "unreadable registered directory", "canonical missing", "registered source", "registered nested source", "unidentified nested producer", "malformed nested branch", "unrelated nested branch", "prefix sibling producer"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root, canonical := t.TempDir(), t.TempDir()
			path := filepath.Join(root, ".wb-retired-stage-fixture")
			for _, directory := range []string{filepath.Join(path, "retired-checkout"), path + "-other"} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			artifact := worktrees.LifecycleArtifact{Path: path, WorktreesRoot: root, Kind: "secure_worktree_stage", State: "quarantined", InspectionVerified: true, Disposition: "unscoped_local_stage"}
			diagnostic := worktrees.ListDiagnostic{Path: path, WorktreesRoot: root}
			registry := "worktree " + canonical + "\x00HEAD abc\x00branch refs/heads/main\x00\x00"
			var registryErr error
			expectRegistry := true
			switch kind {
			case "unverified":
				artifact.InspectionVerified, expectRegistry = false, false
			case "wrong kind":
				artifact.Kind, expectRegistry = "task_namespace", false
			case "staging":
				artifact.State, expectRegistry = "staging", false
			case "task owned":
				artifact.Task, expectRegistry = "other", false
			case "wrong disposition":
				artifact.Disposition, expectRegistry = "cleanup_backlog", false
			case "different path":
				diagnostic.Path, expectRegistry = path+"-other", false
			case "different root":
				diagnostic.WorktreesRoot, expectRegistry = root+"-other", false
			case "not sibling":
				artifact.Path = filepath.Join(path, "nested")
				diagnostic.Path, expectRegistry = artifact.Path, false
			case "relative stage":
				artifact.Path, artifact.WorktreesRoot = "relative-stage", "."
				diagnostic.Path, diagnostic.WorktreesRoot = artifact.Path, artifact.WorktreesRoot
			case "source unknown":
				canonical, expectRegistry = "", false
			case "canonical unavailable":
				canonical, expectRegistry = filepath.Join(canonical, "missing"), false
			case "registry error":
				registryErr = errors.New("source Git registry unavailable")
			case "malformed registry":
				registry = "partial"
			case "missing path record":
				registry = "HEAD abc\x00\x00"
			case "relative registry path":
				registry = "worktree relative\x00HEAD abc\x00\x00"
			case "missing registered directory":
				registry += "worktree " + filepath.Join(path, "missing") + "\x00HEAD abc\x00branch refs/heads/candidate\x00\x00"
			case "unreadable registered directory":
				if os.Geteuid() == 0 {
					t.Skip("root ignores directory access permissions")
				}
				directory := filepath.Join(path, "retired-checkout")
				if err := os.Chmod(directory, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
				registry += "worktree " + directory + "\x00HEAD abc\x00branch refs/heads/candidate\x00\x00"
			case "canonical missing":
				registry = "worktree " + t.TempDir() + "\x00HEAD abc\x00branch refs/heads/main\x00\x00"
			case "registered source":
				registry += "worktree " + path + "\x00HEAD abc\x00branch refs/heads/candidate\x00\x00"
			case "registered nested source":
				registry += "worktree " + filepath.Join(path, "retired-checkout") + "\x00HEAD abc\x00branch refs/heads/candidate\x00\x00"
			case "unidentified nested producer":
				registry += "worktree " + filepath.Join(path, "retired-checkout") + "\x00HEAD abc\x00detached\x00\x00"
			case "malformed nested branch":
				registry += "worktree " + filepath.Join(path, "retired-checkout") + "\x00HEAD abc\x00branch refs/heads/\x00\x00"
			case "unrelated nested branch":
				registry += "worktree " + filepath.Join(path, "retired-checkout") + "\x00HEAD abc\x00branch refs/heads/independent\x00\x00"
			case "prefix sibling producer":
				registry += "worktree " + path + "-other\x00HEAD abc\x00branch refs/heads/candidate\x00\x00"
			}
			fake := runnertest.New(t)
			if expectRegistry {
				fake.ExpectArgv([]string{"git", "worktree", "list", "--porcelain", "-z"}, runner.Result{Stdout: registry}, registryErr)
			}
			view := orchCovPullRequestView(t, `{"head":{"ref":"candidate","repo":{"full_name":"acme/app"}}}`)
			got := landingRetiredArtifactIsUnrelated(t.Context(), PullRequestLandOptions{run: fake}, view, diagnostic, []worktrees.LifecycleArtifact{artifact}, canonical)
			want := kind == "unregistered retirement" || kind == "unrelated nested branch" || kind == "prefix sibling producer"
			if got != want {
				t.Fatalf("artifact exclusion=%t want=%t", got, want)
			}
		})
	}
}

func TestLandingRegisteredDirectoryUsesPhysicalAncestry(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"inside", "same directory", "outside", "case alias", "distinct case directory", "symbolic parent alias", "symbolic escape", "missing", "unreadable", "regular file", "dangling alias"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			stage := filepath.Join(root, "retired-stage")
			nested := filepath.Join(stage, "producer")
			outside := filepath.Join(root, "outside")
			for _, directory := range []string{nested, outside} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			stageIdentity, err := os.Stat(stage)
			if err != nil {
				t.Fatal(err)
			}
			path, wantInside, wantError := nested, true, false
			switch kind {
			case "same directory":
				path = stage
			case "outside":
				path, wantInside = outside, false
			case "case alias":
				alias := filepath.Join(root, "RETIRED-STAGE")
				aliasIdentity, err := os.Stat(alias)
				if err != nil || !os.SameFile(stageIdentity, aliasIdentity) {
					t.Skip("filesystem does not support this case alias")
				}
				path = filepath.Join(alias, "producer")
			case "distinct case directory":
				alias := filepath.Join(root, "RETIRED-STAGE")
				if aliasIdentity, err := os.Stat(alias); err == nil && os.SameFile(stageIdentity, aliasIdentity) {
					t.Skip("filesystem treats case variants as one directory")
				}
				path, wantInside = filepath.Join(alias, "producer"), false
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symbolic parent alias":
				alias := filepath.Join(root, "symbolic-parent")
				if err := os.Symlink(stage, alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "producer")
			case "symbolic escape":
				path, wantInside = filepath.Join(stage, "escape"), false
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "missing":
				path, wantError = filepath.Join(root, "missing"), true
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("root ignores directory access permissions")
				}
				if err := os.Chmod(nested, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(nested, 0700) })
				wantError = true
			case "regular file":
				path, wantError = filepath.Join(root, "file"), true
				if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			case "dangling alias":
				path, wantError = filepath.Join(root, "dangling"), true
				if err := os.Symlink(filepath.Join(root, "missing"), path); err != nil {
					t.Fatal(err)
				}
			}
			identity, inside, err := landingRegisteredDirectoryRelation(stageIdentity, path)
			if (err != nil) != wantError || err == nil && (identity == nil || inside != wantInside) {
				t.Fatalf("physical relation: inside=%t identity=%v err=%v", inside, identity, err)
			}
		})
	}
}

func TestLandingCleanupRequiresAuthoritativeSourceIdentity(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"head":{"ref":"candidate"}}`, `{"head":{"ref":"candidate","repo":{"full_name":""}}}`, `{"head":{"repo":{"full_name":"acme/app"}}}`} {
		view := orchCovPullRequestView(t, raw)
		refusal := preflightLandingCleanup(t.Context(), PullRequestLandOptions{Repository: "acme/app", ProjectsRoot: t.TempDir()}, view, "7", true)
		if refusal == nil || refusal.code != "cleanup-unverifiable" || !strings.Contains(refusal.reason, "head repository and branch") {
			t.Fatalf("unknown source accepted: %+v", refusal)
		}
	}
}
