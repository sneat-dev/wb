//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EImportedMainOwnerNativeGraphBoundaryMatrix(t *testing.T) {
	t.Parallel()
	dir, target, imported, merge, candidate, _ := importedMainReceiptFixture(t)
	tree := gitMergeGraphTest(t, dir, "rev-parse", candidate+"^{tree}")
	commit := func(message string, parents ...string) string {
		t.Helper()
		args := []string{"commit-tree", tree, "-m", message}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		return gitMergeGraphTest(t, dir, args...)
	}
	root := commit("isolated root")
	octopus := commit("octopus", target, imported, root)
	long := target
	for i := 0; i < 257; i++ {
		long = commit("bounded suffix", long)
	}
	for _, row := range []struct {
		name, head, want string
		found            bool
	}{{"target", target, "", false}, {"linear", commit("linear", target), "", false}, {"exact imported", candidate, "", true}, {"root", root, "first-parent", false}, {"symbolic revision", "HEAD", "first-parent", false}, {"octopus", octopus, "octopus", false}, {"limit", long, "exceeds limit", false}, {"missing", "missing-native-object", "inspect candidate", false}, {"wrong order", commit("wrong merge", imported, target), "unexpected merge", false}, {"second merge", commit("second", merge, root), "unexpected merge", false}} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			m, i, found, err := worktreeMergeImportedMainGraph(t.Context(), defaultRunner, dir, row.head, target)
			if row.want != "" {
				if err == nil || !strings.Contains(err.Error(), row.want) {
					t.Fatalf("graph=(%s,%s,%t,%v) want=%s", m, i, found, err, row.want)
				}
			} else if err != nil || found != row.found || (found && (m != merge || i != imported)) {
				t.Fatalf("graph=(%s,%s,%t,%v)", m, i, found, err)
			}
		})
	}
	r := WorktreeMergeReceipt{TargetSHA: target, Candidate: WorktreeMergeCandidate{SHA: target, Worktree: dir}}
	if got, err := worktreeMergeImportedMainDeadcode(t.Context(), &r, 0, 0, 0); got != nil || err != nil {
		t.Fatalf("no import=%+v %v", got, err)
	}
}

func TestE2EImportedMainOwnerNativeLineageStageRefusals(t *testing.T) {
	t.Parallel()
	dir, target, imported, _, _, _ := importedMainReceiptFixture(t)
	for _, row := range []struct {
		name string
		args []string
	}{
		{"fetch", []string{"fetch", "--no-tags", "origin", "refs/heads/main"}},
		{"fetched revision", []string{"rev-parse", "--verify", "FETCH_HEAD^{commit}"}},
		{"fresh remote", []string{"ls-remote", "--heads", "origin", "refs/heads/main"}},
		{"ancestry", []string{"merge-base", "--is-ancestor", imported, imported}},
	} {
		//nolint:paralleltest // Rows share one native checkout and overwrite FETCH_HEAD while testing ordered lineage observations.
		t.Run(row.name, func(t *testing.T) {
			cause := errors.New("selected " + row.name)
			run := &importedMainOwnerRunner{Runner: defaultRunner, dir: dir, args: row.args, ordinal: 1, cause: cause}
			got, err := verifyImportedMainLineage(t.Context(), run, dir, imported, "", 0)
			if got != "" || !errors.Is(err, cause) || run.seen != 1 {
				t.Fatalf("lineage=%s %v consumed=%d", got, err, run.seen)
			}
		})
	}
	// Native recorded lineage initially contains imported; an unrelated recorded
	// target is refused independently of current remote ancestry.
	unrelated := target
	if _, err := verifyImportedMainLineage(t.Context(), defaultRunner, dir, imported, unrelated, 0); err == nil || !strings.Contains(err.Error(), "initially attested") {
		t.Fatalf("unrelated attestation=%v", err)
	}
	r := WorktreeMergeReceipt{TargetSHA: "target", Candidate: WorktreeMergeCandidate{SHA: "candidate", Worktree: dir}, ImportedMainDeadcode: &WorktreeMergeImportedMainDeadcode{}}
	if err := recheckWorktreeMergeImportedMainDeadcodeWithRunner(t.Context(), defaultRunner, r, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "does not bind") {
		t.Fatalf("negative evidence accepted: %v", err)
	}
}

//nolint:paralleltest // This group installs a private scripted Go tool in process PATH; rows remain sequential and positive Git/archive/config are native.
func TestE2EImportedMainOwnerInitialAndResumeEvidenceStages(t *testing.T) {
	dir, target, imported, merge, candidate, bin := importedMainReceiptFixture(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
	r := WorktreeMergeReceipt{Repository: "example.test/wb", TargetSHA: target, Candidate: WorktreeMergeCandidate{SHA: candidate, Worktree: dir}}
	evidence, err := worktreeMergeImportedMainDeadcode(t.Context(), &r, 10*time.Second, 0, 5*time.Second)
	if err != nil || evidence == nil || evidence.MergeSHA != merge || evidence.ImportedSHA != imported || !validImportedMainDeadcodeReport(evidence.Validation) {
		t.Fatalf("native graph/archive and controlled tool evidence=%+v %v", evidence, err)
	}
	r.ImportedMainDeadcode = evidence
	r.ValidationTimeouts = &WorktreeMergeValidationTimeouts{Check: 5 * time.Second}
	if err := recheckWorktreeMergeImportedMainDeadcode(t.Context(), r, time.Minute, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name string
		args []string
		want string
	}{
		{"initial lineage", []string{"fetch", "--no-tags", "origin", "refs/heads/main"}, "attest imported main lineage"},
		{"resume graph", []string{"rev-list", "--parents", "-n", "1", candidate}, "inspect candidate ancestry"},
		{"resume lineage", []string{"fetch", "--no-tags", "origin", "refs/heads/main"}, "recheck imported main lineage"},
	} {
		//nolint:paralleltest // Process-wide environment changes in TestE2EImportedMainOwnerInitialAndResumeEvidenceStages; these rows share their parent environment and remain sequential.
		t.Run(row.name, func(t *testing.T) {
			cause := errors.New("selected " + row.name)
			run := &importedMainOwnerRunner{Runner: defaultRunner, dir: dir, args: row.args, ordinal: 1, cause: cause}
			var err error
			if row.name == "initial lineage" {
				_, err = worktreeMergeImportedMainDeadcodeWithRunner(t.Context(), run, &r, 10*time.Second, 0, 5*time.Second)
			} else {
				err = recheckWorktreeMergeImportedMainDeadcodeWithRunner(t.Context(), run, r, 0, 0, 0)
			}
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), row.want) || run.seen != 1 {
				t.Fatalf("error=%v consumed=%d", err, run.seen)
			}
		})
	}
	for _, initial := range []bool{true, false} {
		cause := errors.New("selected native archive refusal")
		consumed := false
		run := &importedMainArchiveRefusal{Runner: defaultRunner, dir: dir, sha: imported, cause: cause, consumed: &consumed}
		var err error
		if initial {
			_, err = worktreeMergeImportedMainDeadcodeWithRunner(t.Context(), run, &r, 10*time.Second, 0, 5*time.Second)
		} else {
			err = recheckWorktreeMergeImportedMainDeadcodeWithRunner(t.Context(), run, r, 5*time.Second, 0, 5*time.Second)
		}
		if !errors.Is(err, cause) || !consumed {
			t.Fatalf("owner archive refusal initial=%t: %v consumed=%t", initial, err, consumed)
		}
	}
	noImport := r
	noEvidence := *evidence
	noEvidence.CandidateSHA = target
	noImport.Candidate.SHA = target
	noImport.ImportedMainDeadcode = &noEvidence
	if err := recheckWorktreeMergeImportedMainDeadcodeWithRunner(t.Context(), defaultRunner, noImport, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "ancestry no longer matches") {
		t.Fatalf("native missing import=%v", err)
	}
	changed := r
	copyEvidence := *evidence
	changed.ImportedMainDeadcode = &copyEvidence
	copyEvidence.MergeSHA = target
	if err := recheckWorktreeMergeImportedMainDeadcodeWithRunner(t.Context(), defaultRunner, changed, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "ancestry no longer matches") {
		t.Fatalf("changed merge evidence=%v", err)
	}
	// Changing only the controlled external tool output must fail equality after
	// genuine archive/config/quality execution, not substitute a successful proof.
	script := filepath.Join(bin, "go")
	original, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testenv.WriteExecutableFile(script, original, 0755); err != nil {
			t.Error(err)
		}
	})
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := revalidateImportedMainDeadcodeEvidence(t.Context(), defaultRunner, r, evidence, 10*time.Second, 0, 5*time.Second); err == nil || !strings.Contains(err.Error(), "validation changed") {
		t.Fatalf("changed tool evidence=%v", err)
	}
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\necho 'incomplete controlled tool failure'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	report, err := verifyWorktreeMergeImportedMainDeadcode(t.Context(), defaultRunner, r.Repository, dir, imported, 10*time.Second, 0, 5*time.Second)
	if err == nil || len(report.Results) == 0 || report.Revision != imported || !report.WorkspaceClean {
		t.Fatalf("partial authentic quality report lost: %+v %v", report, err)
	}
	if err := testenv.WriteExecutableFile(script, original, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestE2EImportedMainOwnerNativeArchivePolicyRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"archive", "invalid tar", "missing remote", "malformed policy", "missing command", "foreign then exact"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir, _, imported, _, _, _ := importedMainReceiptFixture(t)
			cause := errors.New("selected archive")
			run := &importedMainOwnerRunner{Runner: defaultRunner, dir: dir, ordinal: 1, cause: nil}
			consumed := false
			var archive string
			run.after = func(ctx context.Context, path string, args []string, result runner.Result) {
				if path != dir || len(args) != 4 || args[0] != "archive" || args[3] != imported {
					return
				}
				consumed = true
				archive = strings.TrimPrefix(args[2], "--output=")
				switch mode {
				case "invalid tar":
					if err := os.WriteFile(archive, []byte("not a tar archive"), 0600); err != nil {
						t.Fatal(err)
					}
				case "missing remote":
					gitMergeGraphTest(t, dir, "remote", "remove", "origin")
				}
			}
			if mode == "archive" {
				run.args = []string{"archive", "--format=tar"}
				run.after = nil
				// Archive output includes a per-run temporary path. Match the exact native
				// request after observing it, with the specialized negative wrapper below.
			}
			if mode == "malformed policy" || mode == "missing command" || mode == "foreign then exact" {
				gitMergeGraphTest(t, dir, "checkout", "main")
				body := "version: 1\ngo_lint:\n  commands:\n    - [go, vet, ./...]\n"
				if mode == "foreign then exact" {
					body += "    - [go, run, ./cmd/wb, deadcode]\n"
				}
				if mode == "malformed policy" {
					body = "unknown_policy: true\n"
				}
				if err := os.WriteFile(filepath.Join(dir, ".wb", "quality.yaml"), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
				gitMergeGraphTest(t, dir, "add", ".wb/quality.yaml")
				gitMergeGraphTest(t, dir, "commit", "-m", mode)
				head := gitMergeGraphTest(t, dir, "rev-parse", "HEAD")
				_, err := verifyWorktreeMergeImportedMainDeadcode(t.Context(), defaultRunner, "example.test/wb", dir, head, 10*time.Second, 0, 0)
				if err == nil || (!strings.Contains(err.Error(), "quality policy") && !strings.Contains(err.Error(), "not configured") && !strings.Contains(err.Error(), "validation is incomplete")) {
					t.Fatalf("%s=%v", mode, err)
				}
				gitMergeGraphTest(t, dir, "reset", "--hard", imported)
				gitMergeGraphTest(t, dir, "checkout", "integration")
				return
			}
			var selected runner.Runner = run
			if mode == "archive" {
				selected = &importedMainArchiveRefusal{Runner: defaultRunner, dir: dir, sha: imported, cause: cause, consumed: &consumed}
			}
			_, err := verifyWorktreeMergeImportedMainDeadcode(t.Context(), selected, "example.test/wb", dir, imported, 10*time.Second, 0, 0)
			if err == nil || !consumed || (mode == "archive" && !errors.Is(err, cause)) {
				t.Fatalf("%s error=%v consumed=%t", mode, err, consumed)
			}
			if archive != "" {
				if _, e := os.Stat(filepath.Dir(archive)); !os.IsNotExist(e) {
					t.Fatalf("archive scratch leaked: %v", e)
				}
			}
			if mode == "missing remote" {
				gitMergeGraphTest(t, dir, "remote", "add", "origin", filepath.Join(filepath.Dir(dir), "origin.git"))
			}
		})
	}
}

//nolint:paralleltest // os.MkdirTemp uses process TMPDIR/TMP/TEMP. The exact environment is restored before native Git observations.
func TestE2EImportedMainOwnerScratchFailurePrecedesNativeArchive(t *testing.T) {
	dir, _, imported, _, _, _ := importedMainReceiptFixture(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	type pin struct {
		value string
		set   bool
	}
	old := map[string]pin{}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		v, ok := os.LookupEnv(key)
		old[key] = pin{v, ok}
		t.Setenv(key, blocker)
	}
	restore := func() {
		for key, p := range old {
			if p.set {
				if err := os.Setenv(key, p.value); err != nil {
					t.Error(err)
				}
			} else if err := os.Unsetenv(key); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(restore)
	consumed := false
	run := &importedMainArchiveRefusal{Runner: defaultRunner, dir: dir, sha: imported, cause: errors.New("must not archive"), consumed: &consumed}
	_, err := verifyWorktreeMergeImportedMainDeadcode(t.Context(), run, "example.test/wb", dir, imported, 0, 0, 3*time.Minute)
	restore()
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || consumed || !strings.Contains(pathErr.Path, blocker) {
		t.Fatalf("scratch error=%v archive=%t", err, consumed)
	}
	if head := gitMergeGraphTest(t, dir, "rev-parse", "HEAD"); head == "" {
		t.Fatal("native checkout lost")
	}
}
