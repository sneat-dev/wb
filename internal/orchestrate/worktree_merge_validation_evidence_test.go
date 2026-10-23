package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

func TestWorktreeMergeFileDigestTracksBytesAndReadFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "validator")
	if err := os.WriteFile(path, []byte("validator bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("validator bytes"))
	got, err := fileSHA256(path)
	if err != nil || got != hex.EncodeToString(digest[:]) {
		t.Fatalf("digest = %q, %v", got, err)
	}
	got, err = fileSHA256(path + "-missing")
	if !errors.Is(err, os.ErrNotExist) || got != "" {
		t.Fatalf("missing digest = %q, %v", got, err)
	}
}

func TestWorktreeMergeValidationFingerprintRejectsUnavailableInputs(t *testing.T) {
	t.Parallel()
	failure := errors.New("input unavailable")
	tests := []struct {
		name            string
		policyError     error
		executableError error
		lookupError     error
		hashFailure     string
		accepted        bool
	}{
		{name: "policy present", accepted: true},
		{name: "policy absent", policyError: os.ErrNotExist, accepted: true},
		{name: "policy unreadable", policyError: failure},
		{name: "WB executable unavailable", executableError: failure},
		{name: "WB executable unreadable", hashFailure: "wb"},
		{name: "validator unresolved", lookupError: failure},
		{name: "validator unreadable", hashFailure: "go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var lookedUp []string
			deps := worktreeMergeFingerprintDependencies{
				readFile: func(path string) ([]byte, error) {
					if path != filepath.Join("candidate", ".wb", "quality.yaml") {
						t.Fatalf("policy path %s", path)
					}
					return []byte("policy"), test.policyError
				},
				executable: func() (string, error) { return "wb", test.executableError },
				lookPath:   func(name string) (string, error) { lookedUp = append(lookedUp, name); return name, test.lookupError },
				hashFile: func(path string) (string, error) {
					if path == test.hashFailure {
						return "", failure
					}
					return path + "-digest", nil
				},
			}
			receipt := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{Worktree: "candidate", SHA: "candidate-sha"}, TargetSHA: "target-sha", Sources: []WorktreeMergeSource{{SHA: "source-one"}, {SHA: "source-two"}}, Validation: quality.VerificationReport{Results: []quality.VerificationEntry{
				{Language: "go", Command: "   "}, {Language: "shell", Command: "ignored-tool"},
				{Language: "go", Command: "go test ./..."}, {Language: "node", Command: "npm test"}, {Language: "specscore", Command: "specscore check"},
			}}}
			got, ok := worktreeMergeValidationIdentityWithDependencies(receipt, deps)
			if ok != test.accepted {
				t.Fatalf("fingerprint acceptance %t, identity %+v", ok, got)
			}
			if !ok {
				if !reflect.DeepEqual(got, WorktreeMergeValidationIdentity{}) {
					t.Fatalf("partial fingerprint escaped: %+v", got)
				}
				return
			}
			policy := []byte("policy")
			if test.policyError != nil {
				policy = []byte("absent")
			}
			digest := sha256.Sum256(policy)
			want := WorktreeMergeValidationIdentity{CandidateSHA: "candidate-sha", TargetSHA: "target-sha", SourceSHAs: []string{"source-one", "source-two"}, QualityPolicySHA: hex.EncodeToString(digest[:]), WBBuild: buildinfo.Version() + "@" + buildinfo.Revision(), WBExecutableSHA: "wb-digest", Validators: map[string]string{"go": "go-digest", "npm": "npm-digest", "specscore": "specscore-digest"}}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(lookedUp, []string{"go", "npm", "specscore"}) {
				t.Fatalf("identity %+v, lookups %v; want %+v", got, lookedUp, want)
			}
		})
	}
}

func TestWorktreeMergeValidationFingerprintUsesNativeWBIdentity(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{Worktree: t.TempDir(), SHA: "candidate"}, TargetSHA: "target"}
	identity, ok := worktreeMergeValidationIdentity(receipt)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || identity.WBExecutableSHA != digest || identity.WBBuild != buildinfo.Version()+"@"+buildinfo.Revision() || identity.Validators != nil {
		t.Fatalf("native identity %+v, accepted %t", identity, ok)
	}
}

func TestWorktreeMergeBaselineValidatorIdentityPreservesMissReasons(t *testing.T) {
	t.Parallel()
	failure := errors.New("validator unavailable")
	tests := []struct {
		name                   string
		lookupError, errorRead error
		checks                 []quality.Check
		want                   map[string]string
	}{
		{name: "no spec", checks: []quality.Check{quality.CheckLint}, want: nil},
		{name: "resolved", checks: []quality.Check{quality.CheckLint, quality.CheckSpec, quality.CheckSpec}, want: map[string]string{"specscore": "digest"}},
		{name: "unresolved", checks: []quality.Check{quality.CheckSpec}, lookupError: failure, want: map[string]string{"specscore": "unresolved"}},
		{name: "unreadable", checks: []quality.Check{quality.CheckSpec}, errorRead: failure, want: map[string]string{"specscore": "unreadable"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lookups := 0
			deps := worktreeMergeFingerprintDependencies{lookPath: func(name string) (string, error) {
				lookups++
				if name != "specscore" {
					t.Fatalf("validator %s", name)
				}
				return "installed-specscore", test.lookupError
			}, hashFile: func(path string) (string, error) {
				if path != "installed-specscore" {
					t.Fatalf("validator path %s", path)
				}
				return "digest", test.errorRead
			}}
			if got := validationCacheValidatorSHAsWithDependencies(test.checks, deps); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("validator identity %#v, want %#v", got, test.want)
			}
			wantLookups := 1
			if test.want == nil {
				wantLookups = 0
			}
			if lookups != wantLookups {
				t.Fatalf("lookups %d, want %d", lookups, wantLookups)
			}
		})
	}
}

func TestWorktreeMergeLintEvidenceRetainsSetupAndFailedLint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		entries    []quality.VerificationEntry
		wantStatus quality.Status
		wantCount  int
	}{
		{name: "empty", wantStatus: quality.StatusSkipped},
		{name: "setup and skipped", entries: []quality.VerificationEntry{{Check: "install", Status: quality.StatusSkipped}, {Check: "", Status: quality.StatusPassed}, {Check: quality.CheckTest, Status: quality.StatusFailed}}, wantStatus: quality.StatusPassed, wantCount: 2},
		{name: "failure wins", entries: []quality.VerificationEntry{{Check: quality.CheckLint, Status: quality.StatusFailed}, {Check: quality.CheckLint, Status: quality.StatusPassed}}, wantStatus: quality.StatusFailed, wantCount: 2},
		{name: "pass then failure", entries: []quality.VerificationEntry{{Check: quality.CheckLint, Status: quality.StatusPassed}, {Check: "install", Status: quality.StatusFailed}}, wantStatus: quality.StatusFailed, wantCount: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			original := quality.VerificationReport{Path: "git:target", Revision: "target", WorkspaceClean: true, Results: test.entries}
			before := append([]quality.VerificationEntry(nil), original.Results...)
			got := worktreeMergeLintEvidence(original)
			if got.Status != test.wantStatus || len(got.Results) != test.wantCount || got.Path != original.Path || got.Revision != original.Revision || !got.WorkspaceClean || !reflect.DeepEqual(original.Results, before) {
				t.Fatalf("lint evidence %+v", got)
			}
		})
	}
}

// A stage trace tests native error ordering and cleanup ownership. It is unit
// evidence for dependency failures, never a substitute for the real Git archive
// and installed-validator fixtures.
func TestWorktreeMergeTargetBaselineStagesPreserveErrorsAndCacheRoutes(t *testing.T) {
	t.Parallel()
	failure := errors.New("stage failure")
	tests := []struct {
		name, failAt    string
		lint            bool
		fullHit, ownHit bool
		status          quality.Status
		wantError       string
		wantStages      string
	}{
		{name: "missing target", failAt: "empty", wantError: "target SHA is required for validation baseline"},
		{name: "temporary failure", failAt: "temporary", wantError: "create target validation snapshot", wantStages: "temporary"},
		{name: "archive failure", failAt: "archive", wantError: "archive target target", wantStages: "temporary archive cleanup"},
		{name: "materialize failure", failAt: "extract", wantError: "materialize target target", wantStages: "temporary archive extract cleanup"},
		{name: "remote failure", failAt: "remote", wantError: "stage failure", wantStages: "temporary archive extract remote cleanup"},
		{name: "policy failure", failAt: "options", wantError: "load target quality policy", wantStages: "temporary archive extract remote options cleanup"},
		{name: "key failure", failAt: "key", wantError: "fingerprint target validation baseline", wantStages: "temporary archive extract remote options key cleanup"},
		{name: "home failure", failAt: "home", wantError: "resolve WB validation cache", wantStages: "temporary archive extract remote options key home cleanup"},
		{name: "full key failure", failAt: "full-key", lint: true, wantError: "fingerprint full target validation baseline", wantStages: "temporary archive extract remote options key home full-key cleanup"},
		{name: "full cache failure", failAt: "full-load", lint: true, wantError: "read full target validation baseline cache", wantStages: "temporary archive extract remote options key home full-key full-load cleanup"},
		{name: "full cache hit", lint: true, fullHit: true, wantStages: "temporary archive extract remote options key home full-key full-load cleanup"},
		{name: "own cache failure", failAt: "load", wantError: "read target validation baseline cache", wantStages: "temporary archive extract remote options key home load cleanup"},
		{name: "lint own cache failure", failAt: "load", lint: true, wantError: "read target validation baseline cache", wantStages: "temporary archive extract remote options key home full-key full-load load cleanup"},
		{name: "own cache hit", ownHit: true, wantStages: "temporary archive extract remote options key home load cleanup"},
		{name: "lint own cache hit", lint: true, ownHit: true, wantStages: "temporary archive extract remote options key home full-key full-load load cleanup"},
		{name: "full verification passed", status: quality.StatusPassed, wantStages: "temporary archive extract remote options key home load verify save cleanup"},
		{name: "lint verification failed", lint: true, status: quality.StatusFailed, wantStages: "temporary archive extract remote options key home full-key full-load load verify save cleanup"},
		{name: "skipped verification", status: quality.StatusSkipped, wantStages: "temporary archive extract remote options key home load verify cleanup"},
		{name: "save failure", failAt: "save", status: quality.StatusFailed, wantError: "save target validation baseline cache", wantStages: "temporary archive extract remote options key home load verify save cleanup"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stages []string
			stage := func(name string) error {
				stages = append(stages, name)
				if name == test.failAt {
					return failure
				}
				return nil
			}
			root := t.TempDir()
			snapshot := filepath.Join(root, "tree")
			checks := []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec}
			if test.lint {
				checks = []quality.Check{quality.CheckLint}
			}
			options := quality.RunOptions{Timeout: time.Second, Retry: 2, CheckTimeout: 3 * time.Second, ShardAttemptTimeout: 4 * time.Second}
			keyCalls := 0
			cached := quality.VerificationReport{Path: "git:target", Revision: "target", WorkspaceClean: true, Status: quality.StatusFailed, Results: []quality.VerificationEntry{{Check: quality.CheckLint, Status: quality.StatusPassed}, {Check: quality.CheckTest, Status: quality.StatusFailed}}}
			deps := worktreeMergeBaselineDependencies{
				fingerprint: worktreeMergeFingerprintDependencies{lookPath: func(name string) (string, error) {
					if name != "specscore" {
						t.Fatalf("validator %s", name)
					}
					return name, nil
				}, hashFile: func(string) (string, error) { return "validator-digest", nil }},
				mkdirTemp: func(dir, pattern string) (string, error) {
					if dir != "" || pattern != "wb-worktree-merge-target-*" {
						t.Fatalf("temporary %s %s", dir, pattern)
					}
					return root, stage("temporary")
				},
				removeAll: func(path string) error {
					if path != root {
						t.Fatalf("cleanup path %s", path)
					}
					return stage("cleanup")
				},
				command: func(ctx context.Context, run runner.Runner, timeout time.Duration, retry int, dir, name string, args ...string) (string, int, error) {
					if ctx == nil || run == nil || timeout != options.Timeout || retry != options.Retry || dir != "candidate" || name != "git" || !reflect.DeepEqual(args, []string{"archive", "--format=tar", "--output=" + filepath.Join(root, "target.tar"), "target"}) {
						t.Fatalf("archive %s %s %v", dir, name, args)
					}
					return "", 0, stage("archive")
				},
				extract: func(path, destination string) error {
					if path != filepath.Join(root, "target.tar") || destination != snapshot {
						t.Fatalf("extract %s %s", path, destination)
					}
					return stage("extract")
				},
				remote: func(ctx context.Context, candidate, path string, timeout time.Duration, retry int) error {
					if ctx == nil || candidate != "candidate" || path != snapshot || timeout != options.Timeout || retry != options.Retry {
						t.Fatalf("remote %s %s", candidate, path)
					}
					return stage("remote")
				},
				options: func(path string, base quality.RunOptions) (quality.RunOptions, error) {
					if path != snapshot || !reflect.DeepEqual(base, options) {
						t.Fatalf("policy options %+v", base)
					}
					return base, stage("options")
				},
				key: func(repository, revision, path, wbRevision string, keyChecks []quality.Check, validators map[string]string, opts quality.RunOptions) (quality.ValidationCacheKey, error) {
					keyCalls++
					name := "key"
					if keyCalls == 2 {
						name = "full-key"
					}
					wantChecks := checks
					if keyCalls == 2 {
						wantChecks = []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec}
					}
					wantValidators := map[string]string{"specscore": "validator-digest"}
					if test.lint && keyCalls == 1 {
						wantValidators = nil
					}
					if repository != "owner/repo" || revision != "target" || path != snapshot || wbRevision != buildinfo.Revision() || !reflect.DeepEqual(keyChecks, wantChecks) || !reflect.DeepEqual(validators, wantValidators) || !reflect.DeepEqual(opts, options) {
						t.Fatalf("key inputs %s %s %s %s %v %v %+v", repository, revision, path, wbRevision, keyChecks, validators, opts)
					}
					return quality.ValidationCacheKey{TargetRevision: revision, Checks: keyChecks}, stage(name)
				},
				home: func() (string, error) { return root, stage("home") },
				load: func(dir string, key quality.ValidationCacheKey) (quality.VerificationReport, bool, error) {
					if dir != quality.ValidationCacheDir(filepath.Join(root, ".wb")) || key.TargetRevision != "target" {
						t.Fatalf("load %s %+v", dir, key)
					}
					if test.lint && len(key.Checks) > 1 {
						return cached, test.fullHit, stage("full-load")
					}
					return cached, test.ownHit, stage("load")
				},
				verify: func(ctx context.Context, repository, path string, gotChecks []quality.Check, opts quality.RunOptions) quality.VerificationReport {
					if ctx == nil || repository != "owner/repo" || path != snapshot || !reflect.DeepEqual(gotChecks, checks) || !reflect.DeepEqual(opts, options) {
						t.Fatalf("verify %s %s %v %+v", repository, path, gotChecks, opts)
					}
					_ = stage("verify")
					return quality.VerificationReport{Path: snapshot, Revision: "mutable", Status: test.status}
				},
				save: func(dir string, key quality.ValidationCacheKey, report quality.VerificationReport) error {
					if dir != quality.ValidationCacheDir(filepath.Join(root, ".wb")) || key.TargetRevision != "target" || report.Path != "git:target" || report.Revision != "target" || !report.WorkspaceClean || report.Status != test.status {
						t.Fatalf("durable evidence %s %+v %+v", dir, key, report)
					}
					return stage("save")
				},
			}
			target := " target "
			if test.failAt == "empty" {
				target = " \t "
			}
			got, err := verifyWorktreeMergeTargetChecksWithDependencies(context.Background(), "owner/repo", "candidate", target, options.Timeout, options.Retry, options.CheckTimeout, options.ShardAttemptTimeout, checks, deps)
			if strings.Join(stages, " ") != test.wantStages {
				t.Fatalf("stages %v, want %s", stages, test.wantStages)
			}
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || !reflect.DeepEqual(got, quality.VerificationReport{}) {
					t.Fatalf("failure report %+v, err %v", got, err)
				}
				if test.failAt != "empty" && !errors.Is(err, failure) {
					t.Fatalf("lost native error %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := quality.VerificationReport{Path: "git:target", Revision: "target", WorkspaceClean: true, Status: test.status}
			if test.ownHit {
				want = cached
			}
			if test.fullHit {
				want = quality.VerificationReport{Path: "git:target", Revision: "target", WorkspaceClean: true, Status: quality.StatusPassed, Results: []quality.VerificationEntry{{Check: quality.CheckLint, Status: quality.StatusPassed}}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("report %+v, want %+v", got, want)
			}
		})
	}
}
