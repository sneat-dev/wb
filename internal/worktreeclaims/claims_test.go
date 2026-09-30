package worktreeclaims

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreejournal"
)

func testCanonicalTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testPorts() Ports {
	return Ports{
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "branch --show-current":
				return "feature/one", nil
			case "reflog show --date=iso-strict --format=%gd %gs %cI feature/one":
				return "feature/one@{0} branch: Created 2026-01-02T03:04:05Z", nil
			case "merge-base origin/main feature/one":
				return strings.Repeat("a", 40), nil
			default:
				return "", fmt.Errorf("unexpected git %v", args)
			}
		},
		OriginSlug:    func(context.Context, string) (string, error) { return "acme/app", nil },
		EnsureExclude: func(string, []string, string) error { return nil },
		ReadBytesAt:   filewrite.ReadAt,
		WriteBytesImmutableAt: func(dir *os.File, name string, data []byte, mode os.FileMode, idempotent bool) error {
			return filewrite.WriteBytesImmutableAt(dir, name, data, mode, idempotent, nil)
		},
		RecordOwner: func(string, string, string, string, int) error { return nil },
		CurrentPID:  func() int { return 42 },
	}
}
func createdManifest(root string) Manifest {
	return Manifest{Version: 1, EffortID: "feature.one", ParentEffort: "feature", EffortKind: EffortKindTask, Repository: "acme/app", Worktree: root, Branch: "feature/one", Base: "main", BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(), Provenance: ProvenanceCreated}
}
func TestClaimsManifestPromptReplayAndMalformedRefusal(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	p := testPorts()
	m := createdManifest(root)
	if _, err := p.ReadManifest(root); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("missing manifest: %v", err)
	}
	if err := p.WriteManifest(root, m); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureManifest(root, m); err != nil {
		t.Fatal(err)
	}
	if got, err := p.ReadManifest(root); err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("manifest replay: %+v %v", got, err)
	}
	body := []byte("exact private instruction\n")
	if err := p.EnsurePrompt(root, PromptHeader{Source: PromptSourceHuman, Slug: "initial"}, body); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsurePrompt(root, PromptHeader{Source: PromptSourceHuman, Slug: "other"}, []byte("different")); err != nil {
		t.Fatal(err)
	}
	prompts, err := p.ListPrompts(root)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompt replay: %+v %v", prompts, err)
	}
	digest := sha256.Sum256(body)
	if prompts[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("prompt digest differs")
	}
	if _, err := p.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	prompts, err = p.ListPrompts(root)
	if err != nil || len(prompts) != 2 || prompts[1].Seq != 1 {
		t.Fatalf("sequence: %+v %v", prompts, err)
	}
	journal, err := worktreejournal.OpenJournalDirectory(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wb", "local", "manifest.yaml"), []byte("[bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadManifest(root); err == nil {
		t.Fatal("accepted malformed manifest")
	}
	if _, err := p.ReconstructManifest(context.Background(), root); err == nil {
		t.Fatal("reconstructed over malformed manifest")
	}
}
func TestClaimsPromptSequenceRejectsMalformedAndConcurrentAppend(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	p := testPorts()
	const n = 8
	var wg sync.WaitGroup
	errorsCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := p.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte(fmt.Sprintf("prompt %d\n", i)))
			errorsCh <- err
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	prompts, err := p.ListPrompts(root)
	if err != nil || len(prompts) != n {
		t.Fatalf("concurrent sequence: %d %v", len(prompts), err)
	}
	for i, prompt := range prompts {
		if prompt.Seq != i {
			t.Fatalf("ordinal %d: %+v", i, prompt)
		}
	}
	path := filepath.Join(root, ".wb", "local", "prompts", "0000-prompt-0.md")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(root, ".wb", "local", "prompts", "0000-prompt-1.md")
	}
	if err := os.WriteFile(path, []byte("bad frontmatter"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ListPrompts(root); err == nil {
		t.Fatal("accepted malformed prompt")
	}
}
func TestClaimsBindingLookup(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(home, "claim.json")
	if err := os.WriteFile(claimPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	p := BindingPorts{
		Root: func(string) (string, error) { return home, nil },
		Active: func(string, string) (ActiveClaim, error) {
			return ActiveClaim{Task: "task", ClaimID: "claim", Path: claimPath}, nil
		},
		Homes: func(string) ([]string, error) { return []string{home, home}, nil },
		Walk: func(_ string, visit func(*os.File, string, string)) error {
			d, err := os.Open(home)
			if err != nil {
				return err
			}
			defer func() { _ = d.Close() }()
			visit(d, "claim", "task")
			visit(d, "absent", "other")
			return nil
		},
		ReadJSONAt: filewrite.ReadJSONAt,
	}
	task, id, err := p.RecordClaimPullRequestBinding(root, "wt", ClaimPullRequestBinding{Repository: "acme/app", PullRequest: 12, URL: "https://example.test/12"})
	if err != nil || task != "task" || id != "claim" {
		t.Fatalf("record: %s %s %v", task, id, err)
	}
	got, err := p.ListRegisteredPullRequestBindings(root)
	if err != nil || len(got) != 1 || got[0].PullRequest != 12 {
		t.Fatalf("lookup: %+v %v", got, err)
	}
}
func TestClaimsRecoveryUsesExactIdentity(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	manifest := createdManifest(root)
	manifest.DependencyCampaign = true
	manifest.RunID = ""
	manifest.Model = ""
	p := RecoveryPorts{
		RepositoryRootFor: func(context.Context, string) (string, error) { return root, nil },
		ReadManifest:      func(string) (Manifest, error) { return manifest, nil },
		ObserveGit: func(context.Context, string) LocalGit {
			return LocalGit{Branch: manifest.Branch, Head: strings.Repeat("b", 40)}
		},
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if strings.Join(args, " ") != "merge-base --is-ancestor "+manifest.BaseSHA+" "+strings.Repeat("b", 40) {
				return "", fmt.Errorf("wrong ancestry query %v", args)
			}
			return "", nil
		},
		ClaimID: func(_ string, r CreationResult) string {
			if r.BaseSHA != manifest.BaseSHA || r.Branch != manifest.Branch {
				t.Fatal("identity changed")
			}
			return strings.Repeat("c", 40)
		},
	}
	if got, err := p.ResolveWorktreeRoot(context.Background(), ""); err != nil || got != root {
		t.Fatalf("root: %s %v", got, err)
	}
	if got := p.ResolveLogBase(root, ""); got != "main" {
		t.Fatalf("base: %s", got)
	}
	got, err := RecoverBlankManifestClaim(context.Background(), "home", root, manifest, p, func(home, task string, r CreationResult, o Options) (string, error) {
		if task != "feature" || o.RunID != "recovery-"+strings.Repeat("c", 16) || o.Model != "unknown" {
			t.Fatalf("recovery options: %s %+v", task, o)
		}
		return r.Repository, nil
	})
	if err != nil || got != "acme/app" {
		t.Fatalf("recover: %s %v", got, err)
	}
	manifest.ClaimID = "existing"
	if _, err := p.RecoverableBlankManifestClaimID(context.Background(), root, manifest); err == nil {
		t.Fatal("accepted bound manifest")
	}
}
func TestClaimsOptionsSnapshotAndCredentialMarkers(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "prompt.txt")
	contents := []byte(" exact request\n")
	if err := os.WriteFile(file, contents, 0600); err != nil {
		t.Fatal(err)
	}
	p := OptionsPorts{Root: func(string) (string, error) { return t.TempDir(), nil }, OpenRun: func(string, string, string, bool) (*os.File, string, error) { return nil, "", os.ErrNotExist }, ValidateIdentity: func(ExecutionIdentity) error { return nil }}
	o, err := p.PrepareOptions("root", "task", Options{Model: "model", OriginalPrompt: file, TaskSummary: "  build thing "})
	if err != nil {
		t.Fatal(err)
	}
	if o.Snapshot.Digest == "" || string(o.Snapshot.Contents) != string(contents) {
		t.Fatal("snapshot changed")
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if string(o.Snapshot.Contents) != string(contents) {
		t.Fatal("snapshot followed file mutation")
	}
	if got, err := NormalizeTaskSummary("  build thing "); err != nil || got != "build thing" {
		t.Fatalf("summary: %q %v", got, err)
	}
	for _, s := range []string{"token=abc", "Bearer abcdefghijklmnop", "ghp_abcdefghijklmnop"} {
		if _, err := NormalizeTaskSummary(s); err == nil {
			t.Fatalf("accepted credential %q", s)
		}
	}
	stdin, err := o.WithOriginalPromptFromStdin([]byte("stdin\n"))
	if err != nil || stdin.OriginalPrompt != OriginalPromptStdinMarker {
		t.Fatalf("stdin: %+v %v", stdin, err)
	}
}

func TestClaimsBindingErrorsAndOrdering(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	claimPath := filepath.Join(root, "c.json")
	_ = os.WriteFile(claimPath, []byte("{}"), 0600)
	base := BindingPorts{Root: func(string) (string, error) { return root, nil }, Active: func(string, string) (ActiveClaim, error) {
		return ActiveClaim{Task: "z", ClaimID: "c", Path: claimPath}, nil
	}, Homes: func(string) ([]string, error) { return []string{root}, nil }, Walk: func(_ string, visit func(*os.File, string, string)) error {
		d, err := os.Open(root)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		visit(d, "c", "z")
		return nil
	}, ReadJSONAt: filewrite.ReadJSONAt}
	fail := base
	fail.Root = func(string) (string, error) { return "", errors.New("root failed") }
	if _, _, err := fail.RecordClaimPullRequestBinding("", " ", ClaimPullRequestBinding{}); err == nil {
		t.Fatal("root error lost")
	}
	fail = base
	fail.Active = func(string, string) (ActiveClaim, error) { return ActiveClaim{}, errors.New("active failed") }
	if _, _, err := fail.RecordClaimPullRequestBinding("", "wt", ClaimPullRequestBinding{}); err == nil {
		t.Fatal("active error lost")
	}
	// A malformed time reaches the encoder even though all other fields are scalar.
	if _, _, err := base.RecordClaimPullRequestBinding("", "wt", ClaimPullRequestBinding{RecordedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("encoder error lost")
	}
	missing := base
	missing.Active = func(string, string) (ActiveClaim, error) {
		return ActiveClaim{Task: "z", ClaimID: "c", Path: filepath.Join(root, "missing", "c.json")}, nil
	}
	if _, _, err := missing.RecordClaimPullRequestBinding("", "wt", ClaimPullRequestBinding{}); err == nil {
		t.Fatal("write error lost")
	}
	if _, _, err := base.RecordClaimPullRequestBinding("", "wt", ClaimPullRequestBinding{Repository: "b", RecordedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fail = base
	fail.Homes = func(string) ([]string, error) { return nil, errors.New("homes failed") }
	if _, err := fail.ListRegisteredPullRequestBindings(""); err == nil {
		t.Fatal("homes error lost")
	}
	fail = base
	fail.Walk = func(string, func(*os.File, string, string)) error { return errors.New("walk failed") }
	if _, err := fail.ListRegisteredPullRequestBindings(""); err == nil {
		t.Fatal("walk error lost")
	}
	fail = base
	fail.Homes = func(string) ([]string, error) { return []string{root, root}, nil }
	fail.Walk = func(_ string, visit func(*os.File, string, string)) error {
		d, err := os.Open(root)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		for _, id := range []string{"c", "a", "b"} {
			visit(d, id, id)
		}
		return nil
	}
	fail.ReadJSONAt = func(_ *os.File, id string, target any) error {
		b := target.(*ClaimPullRequestBinding)
		switch id {
		case "c" + PullRequestBindingSuffix:
			b.Repository = "b"
		case "a" + PullRequestBindingSuffix:
			b.Repository = "a"
		case "b" + PullRequestBindingSuffix:
			b.Repository = "a"
		default:
			return os.ErrNotExist
		}
		return nil
	}
	got, err := fail.ListRegisteredPullRequestBindings("")
	if err != nil || len(got) != 3 || got[0].Task != "a" || got[1].Task != "b" || got[2].Task != "c" {
		t.Fatalf("order: %+v %v", got, err)
	}
}

func TestClaimsOptionsErrorPaths(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	prompt := filepath.Join(root, "prompt")
	if err := os.WriteFile(prompt, []byte("exact"), 0600); err != nil {
		t.Fatal(err)
	}
	p := OptionsPorts{Root: func(string) (string, error) { return root, nil }, OpenRun: func(string, string, string, bool) (*os.File, string, error) { return nil, "", os.ErrNotExist }, ValidateIdentity: func(ExecutionIdentity) error { return nil }}
	invalid := Options{Model: "m", EffortID: "bad/path"}
	if _, err := p.PrepareOptions("", "task", invalid); err == nil {
		t.Fatal("prepare accepted effort")
	}
	if err := p.PreflightOptions("task", invalid); err == nil {
		t.Fatal("preflight accepted effort")
	}
	if err := p.PreflightOptions("task", Options{Model: "m", OriginalPrompt: prompt}); err != nil {
		t.Fatal(err)
	}
	if err := p.PreflightOptions("task", Options{Model: "m", OriginalPrompt: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("preflight accepted missing prompt")
	}
	if _, err := p.PrepareOptions("", "task", Options{Model: "m", RequireOriginalPrompt: true}); err == nil {
		t.Fatal("prepare accepted missing prompt")
	}
	fail := p
	fail.Root = func(string) (string, error) { return "", errors.New("home") }
	if _, err := fail.PrepareOptions("", "task", Options{Model: "m"}); err == nil {
		t.Fatal("home error lost")
	}
	fail = p
	fail.OpenRun = func(string, string, string, bool) (*os.File, string, error) { return nil, "", errors.New("run") }
	if _, err := fail.PrepareOptions("", "task", Options{Model: "m"}); err == nil {
		t.Fatal("run error lost")
	}
	invalid = Options{Model: "m", RunID: "bad/path"}
	if _, _, err := p.NormalizeOptions("task", invalid, time.Now()); err == nil {
		t.Fatal("run accepted")
	}
	invalid = Options{Model: "m", TaskSummary: "token=secret"}
	if _, _, err := p.NormalizeOptions("task", invalid, time.Now()); err == nil {
		t.Fatal("summary accepted")
	}
	fail = p
	fail.ValidateIdentity = func(ExecutionIdentity) error { return errors.New("identity") }
	if _, _, err := fail.NormalizeOptions("task", Options{}, time.Now()); err == nil {
		t.Fatal("identity error lost")
	}
	o := Options{OriginalPrompt: prompt, Snapshot: PromptSnapshot{Contents: []byte("exact"), Digest: "wrong"}}
	if err := p.SnapshotOriginalPrompt(&o); err == nil {
		t.Fatal("accepted corrupt snapshot")
	}
	o = Options{OriginalPrompt: prompt, Snapshot: PromptSnapshot{Contents: []byte("exact")}}
	if err := p.SnapshotOriginalPrompt(&o); err == nil {
		t.Fatal("accepted missing digest")
	}
	for _, text := range []string{"", "\n", strings.Repeat("x", MaxTaskSummaryRunes+1), "bad\x00control", string([]byte{0xff})} {
		_, err := NormalizeTaskSummary(text)
		if text == "" || text == "\n" {
			continue
		}
		if err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if _, err := (Options{}).WithOriginalPromptFromStdin([]byte(" \n")); err == nil {
		t.Fatal("accepted empty stdin")
	}
}

func TestClaimsRecoveryRefusals(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	m := createdManifest(root)
	m.DependencyCampaign = true
	p := RecoveryPorts{RepositoryRootFor: func(context.Context, string) (string, error) { return root, nil }, ReadManifest: func(string) (Manifest, error) { return m, nil }, ObserveGit: func(context.Context, string) LocalGit {
		return LocalGit{Branch: m.Branch, Head: strings.Repeat("b", 40)}
	}, Git: func(context.Context, string, ...string) (string, error) { return "", nil }, ClaimID: func(string, CreationResult) string { return strings.Repeat("c", 40) }}
	if got := p.ResolveLogBase(root, "  release "); got != "release" {
		t.Fatal(got)
	}
	p.ReadManifest = func(string) (Manifest, error) { return Manifest{}, errors.New("missing") }
	if got := p.ResolveLogBase(root, ""); got != "main" {
		t.Fatal(got)
	}
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{{"incomplete", func(x *Manifest) { x.BaseSHA = "bad" }}, {"wrong root", func(x *Manifest) { x.Worktree = root + "-other" }}, {"wrong branch", func(x *Manifest) { x.Branch = "other" }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bad := m
			tc.mutate(&bad)
			if _, err := p.RecoverableBlankManifestClaimID(context.Background(), root, bad); err == nil {
				t.Fatal("accepted bad identity")
			}
		})
	}
	badGit := p
	badGit.Git = func(context.Context, string, ...string) (string, error) { return "", errors.New("not ancestor") }
	if _, err := badGit.RecoverableBlankManifestClaimID(context.Background(), root, m); err == nil {
		t.Fatal("accepted non-descendant")
	}
	missingCampaign := m
	missingCampaign.DependencyCampaign = false
	if _, err := RecoverBlankManifestClaim(context.Background(), "home", root, missingCampaign, p, func(string, string, CreationResult, Options) (string, error) {
		t.Fatal("published bad campaign")
		return "", nil
	}); err == nil {
		t.Fatal("accepted noncampaign")
	}
	rootEffort := m
	rootEffort.EffortID = "feature"
	rootEffort.RunID = "run"
	rootEffort.Model = "m"
	_, err := RecoverBlankManifestClaim(context.Background(), "home", root, rootEffort, p, func(_, task string, _ CreationResult, o Options) (string, error) {
		if task != "feature" || o.RunID != "run" || o.Model != "m" {
			t.Fatalf("options: %s %+v", task, o)
		}
		return "", errors.New("publication failed")
	})
	if err == nil {
		t.Fatal("publication error lost")
	}
}

func TestClaimsInjectedJournalFailureBoundaries(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	p := testPorts()
	p.EncodeManifest = func(Manifest) ([]byte, error) { return nil, errors.New("encode") }
	if err := p.WriteManifest(root, createdManifest(root)); err == nil || !strings.Contains(err.Error(), "encode") {
		t.Fatalf("manifest encoder: %v", err)
	}
	p = testPorts()
	p.EncodePromptHeader = func(PromptHeader) ([]byte, error) { return nil, errors.New("encode") }
	if _, err := p.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("request")); err == nil || !strings.Contains(err.Error(), "encode") {
		t.Fatalf("prompt encoder: %v", err)
	}
	p = testPorts()
	p.WriteBytesImmutableAt = func(*os.File, string, []byte, os.FileMode, bool) error { return errors.New("write") }
	if _, err := p.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("request")); err == nil || err.Error() != "write" {
		t.Fatalf("prompt write: %v", err)
	}
	dir, err := worktreejournal.OpenJournalSubdirectory(root, promptsDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	p = testPorts()
	p.ReadNames = func(*os.File) ([]string, error) { return nil, errors.New("read names") }
	if _, err := p.ListPromptsIn(dir); err == nil || !strings.Contains(err.Error(), "read prompt sequence") {
		t.Fatalf("names: %v", err)
	}
	p = testPorts()
	p.Rewind = func(*os.File) error { return errors.New("seek") }
	if _, err := p.ListPromptsIn(dir); err == nil || !strings.Contains(err.Error(), "rewind") {
		t.Fatalf("rewind: %v", err)
	}
	lockDir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lockDir.Close() })
	if _, err := LockJournalSequenceWith(lockDir, ".lock-chmod", LockOps{Chmod: func(int, uint32) error { return errors.New("chmod") }}); err == nil || err.Error() != "chmod" {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LockJournalSequenceWith(lockDir, ".lock-flock", LockOps{Flock: func(int, int) error { return errors.New("flock") }}); err == nil || err.Error() != "flock" {
		t.Fatalf("flock: %v", err)
	}
}

func TestClaimsInjectedSnapshotAndArchiveReadFailures(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	file := filepath.Join(root, "prompt")
	if err := os.WriteFile(file, []byte("request"), 0600); err != nil {
		t.Fatal(err)
	}
	p := OptionsPorts{}
	p.AbsPath = func(string) (string, error) { return "", errors.New("abs") }
	if err := p.SnapshotOriginalPrompt(&Options{OriginalPrompt: file}); err == nil || !strings.Contains(err.Error(), "resolve original prompt") {
		t.Fatalf("abs: %v", err)
	}
	p = OptionsPorts{StatPrompt: func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat") }}
	if err := p.SnapshotOriginalPrompt(&Options{OriginalPrompt: file}); err == nil || !strings.Contains(err.Error(), "inspect original prompt") {
		t.Fatalf("stat: %v", err)
	}
	p = OptionsPorts{ReadPrompt: func(*os.File) ([]byte, error) { return nil, errors.New("read") }}
	if err := p.SnapshotOriginalPrompt(&Options{OriginalPrompt: file}); err == nil || !strings.Contains(err.Error(), "read original prompt") {
		t.Fatalf("read: %v", err)
	}
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	newPorts := func() OptionsPorts {
		return OptionsPorts{OpenRun: func(string, string, string, bool) (*os.File, string, error) {
			d, err := os.Open(runDir)
			return d, runDir, err
		}, ReadBytesAt: func(*os.File, string) ([]byte, error) { return nil, os.ErrNotExist }, ReadJSONAt: func(*os.File, string, any) error { return os.ErrNotExist }, OpenPrivateChild: func(*os.File, string, bool) (*os.File, error) { return nil, os.ErrNotExist }}
	}
	p = newPorts()
	p.ReadBytesAt = func(*os.File, string) ([]byte, error) { return nil, errors.New("archive unreadable") }
	if err := p.CorroborateExistingRunPrompt(root, "e", "r", Options{}); err == nil || !strings.Contains(err.Error(), "inspect existing original prompt") {
		t.Fatalf("archive: %v", err)
	}
	p = newPorts()
	p.ReadJSONAt = func(*os.File, string, any) error { return errors.New("metadata unreadable") }
	if err := p.CorroborateExistingRunPrompt(root, "e", "r", Options{}); err == nil || !strings.Contains(err.Error(), "inspect existing prompt metadata") {
		t.Fatalf("metadata: %v", err)
	}
	p = newPorts()
	p.ReadBytesAt = func(_ *os.File, name string) ([]byte, error) {
		if name == "run.json" {
			return nil, errors.New("run unreadable")
		}
		return nil, os.ErrNotExist
	}
	if err := p.CorroborateExistingRunPrompt(root, "e", "r", Options{}); err == nil || err.Error() != "run unreadable" {
		t.Fatalf("run: %v", err)
	}
	p = newPorts()
	p.OpenPrivateChild = func(*os.File, string, bool) (*os.File, error) { return nil, errors.New("claims unreadable") }
	if err := p.CorroborateExistingRunPrompt(root, "e", "r", Options{}); err == nil || err.Error() != "claims unreadable" {
		t.Fatalf("claims: %v", err)
	}
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("not dir"), 0600); err != nil {
		t.Fatal(err)
	}
	p = newPorts()
	p.OpenPrivateChild = func(*os.File, string, bool) (*os.File, error) { return os.Open(regular) }
	if err := p.CorroborateExistingRunPrompt(root, "e", "r", Options{}); err == nil {
		t.Fatal("accepted unreadable claims directory")
	}
}

func TestClaimsBindingTieBreak(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	p := BindingPorts{Homes: func(string) ([]string, error) { return []string{root}, nil }, Walk: func(_ string, visit func(*os.File, string, string)) error {
		d, err := os.Open(root)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		visit(d, "z", "task")
		visit(d, "a", "task")
		return nil
	}, ReadJSONAt: func(_ *os.File, _ string, target any) error {
		target.(*ClaimPullRequestBinding).Repository = "repo"
		return nil
	}}
	got, err := p.ListRegisteredPullRequestBindings("")
	if err != nil || len(got) != 2 || got[0].ClaimID != "a" {
		t.Fatalf("tie-break: %+v %v", got, err)
	}
}

func TestClaimsPromptOrdinalStopsAtFourDigits(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	p := testPorts()
	count := 9999
	p.ReadNames = func(*os.File) ([]string, error) {
		names := make([]string, count)
		for ordinal := range names {
			names[ordinal] = fmt.Sprintf("%04d-prompt.md", ordinal)
		}
		return names, nil
	}
	p.ReadBytesAt = func(_ *os.File, name string) ([]byte, error) {
		ordinal, err := strconv.Atoi(name[:4])
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("---\nseq: %d\nsource: agent_declared\n---\n\nbody\n", ordinal)), nil
	}
	written := ""
	p.WriteBytesImmutableAt = func(_ *os.File, name string, _ []byte, _ os.FileMode, _ bool) error {
		written = name
		return nil
	}
	name, err := p.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("last valid prompt"))
	if err != nil || !strings.HasPrefix(name, "9999-") || written != name {
		t.Fatalf("last valid ordinal: %q, written %q, err %v", name, written, err)
	}
	count = 10000
	written = ""
	if name, err := p.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("overflow")); err == nil || !strings.Contains(err.Error(), "derived prompt file name") || name != "" {
		t.Fatalf("accepted five-digit ordinal: %q, %v", name, err)
	}
	if written != "" {
		t.Fatalf("overflow wrote %q", written)
	}
}
