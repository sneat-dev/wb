package orchestrate

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// orchCovMergeGHState installs the scripted `gh` the exact-merge tests drive.
const orchCovMergeGHScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  if [ -f "$S/settings" ]; then cat "$S/settings"; fi
  exit "$(cat "$S/settings-exit")"
fi
if [ "$1" = pr ] && [ "$2" = merge ]; then
  printf '%s\n' "$*" >>"$S/merge-args"
  exit "$(cat "$S/merge-exit")"
fi
if [ "$1" = pr ] && [ "$2" = view ]; then
  if [ -f "$S/view" ]; then cat "$S/view"; fi
  exit "$(cat "$S/view-exit")"
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func orchCovMergeGH(t *testing.T, settings, view string) (orchCovGHState, string) {
	t.Helper()
	state := orchCovScriptState(t, orchCovMergeGHScript)
	state.answer(t, "settings", settings)
	state.answer(t, "settings-exit", "0")
	state.answer(t, "view", view)
	state.answer(t, "view-exit", "0")
	state.answer(t, "merge-exit", "0")
	state.answer(t, "merge-args", "")
	args := filepath.Join(state.dir, "merge-args")
	return state, args
}

func orchCovExactMergeReceipt(t *testing.T) WorktreeMergeReceipt {
	t.Helper()
	dir := orchCovGitRepo(t)
	unrelated := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(dir, "candidate.txt"), "candidate\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "candidate")
	head := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	// The unrelated commit is a real, valid commit that is neither the
	// candidate nor a descendant of it, which is the drift this receipt
	// verification exists to catch.
	t.Setenv("ORCHCOV_UNRELATED_SHA", unrelated)
	return WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main",
		PullRequest: "https://example.test/acme/app/pull/41",
		Candidate: WorktreeMergeCandidate{
			Task: "merge-task", Worktree: dir, Branch: "wb/cov/merge", SHA: head,
		},
	}
}

// orchCovUnrelatedSHA is a real commit from the fixture repository that is not
// the candidate head.
func orchCovUnrelatedSHA(t *testing.T) string {
	t.Helper()
	sha := os.Getenv("ORCHCOV_UNRELATED_SHA")
	if sha == "" {
		t.Fatal("the exact-merge fixture was not built")
	}
	return sha
}

// TestOrchCovRepositoryPullRequestMergeMethodPicksTheAllowedMethod covers the
// method-selection half of the retired mergeExactPullRequest, now
// repositoryPullRequestMergeMethod: the PR-land engine (mergePullRequest, via
// mergeOrAdoptAutoMerge) is exercised by pr_land_test.go / pr_land_coverage_test.go.
func TestOrchCovRepositoryPullRequestMergeMethodPicksTheAllowedMethod(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings string
		want     string
	}{
		{name: "merge", settings: `{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true}`, want: "merge"},
		{name: "squash", settings: `{"allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true}`, want: "squash"},
		{name: "rebase", settings: `{"allow_merge_commit":false,"allow_squash_merge":false,"allow_rebase_merge":true}`, want: "rebase"},
	} {
		t.Run(test.name, func(t *testing.T) {
			orchCovMergeGH(t, test.settings, `{}`)
			method, err := repositoryPullRequestMergeMethod(context.Background(), "acme/app")
			if err != nil {
				t.Fatal(err)
			}
			if method != test.want {
				t.Fatalf("method = %q, want %q", method, test.want)
			}
		})
	}
}

func TestOrchCovRepositoryPullRequestMergeMethodRefusesEveryUnusableRoute(t *testing.T) {
	t.Run("no supported method", func(t *testing.T) {
		orchCovMergeGH(t, `{"allow_merge_commit":false,"allow_squash_merge":false,"allow_rebase_merge":false}`, `{}`)
		_, err := repositoryPullRequestMergeMethod(context.Background(), "acme/app")
		if err == nil || !strings.Contains(err.Error(), "no supported pull-request merge method") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("undecodable settings", func(t *testing.T) {
		orchCovMergeGH(t, "not json", `{}`)
		_, err := repositoryPullRequestMergeMethod(context.Background(), "acme/app")
		if err == nil || !strings.Contains(err.Error(), "decode repository merge methods") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unreadable settings", func(t *testing.T) {
		orchCovInstallGH(t, orchCovNotFound)
		_, err := repositoryPullRequestMergeMethod(context.Background(), "acme/app")
		if err == nil || !strings.Contains(err.Error(), "read repository merge methods") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestOrchCovPullRequestLandingReceiptVerifiesTheExactHandoff(t *testing.T) {
	receipt := orchCovExactMergeReceipt(t)
	merged := func(head, base, oid, mergedAt string) string {
		return `{"state":"MERGED","mergedAt":"` + mergedAt + `","headRefOid":"` + head + `","baseRefName":"` + base + `","mergeCommit":{"oid":"` + oid + `"}}`
	}
	for _, test := range []struct {
		name    string
		view    string
		wantIn  string
		wantOID string
		merged  bool
	}{
		{name: "exact merged head", view: merged(receipt.Candidate.SHA, "main", "abc123", "2026-09-01T00:00:00Z"), wantOID: "abc123", merged: true},
		{name: "open head", view: `{"state":"OPEN","headRefOid":"` + receipt.Candidate.SHA + `","baseRefName":"main"}`, merged: false},
		{name: "target mismatch", view: merged(receipt.Candidate.SHA, "release", "abc123", "2026-09-01T00:00:00Z"), wantIn: "does not match target main"},
		{name: "head mismatch", view: merged(orchCovUnrelatedSHA(t), "main", "abc123", "2026-09-01T00:00:00Z"), wantIn: "does not match exact candidate"},
		{name: "merged without a result commit", view: merged(receipt.Candidate.SHA, "main", "", "2026-09-01T00:00:00Z"), wantIn: "omitted its time or server merge-result commit"},
		{name: "merged without a time", view: merged(receipt.Candidate.SHA, "main", "abc123", ""), wantIn: "omitted its time or server merge-result commit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			orchCovMergeGH(t, `{"allow_merge_commit":true}`, test.view)
			oid, mergedResult, err := pullRequestLandingReceipt(context.Background(), receipt, WorktreeMergeLandOptions{Timeout: time.Minute})
			if test.wantIn != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantIn) {
					t.Fatalf("error = %v, want %q", err, test.wantIn)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if oid != test.wantOID || mergedResult != test.merged {
				t.Fatalf("receipt = %q/%t, want %q/%t", oid, mergedResult, test.wantOID, test.merged)
			}
		})
	}
}

func TestOrchCovPullRequestLandingReceiptFailsClosedOnUnusableReads(t *testing.T) {
	receipt := orchCovExactMergeReceipt(t)
	state, _ := orchCovMergeGH(t, `{}`, "not json")
	if _, _, err := pullRequestLandingReceipt(context.Background(), receipt, WorktreeMergeLandOptions{Timeout: time.Minute}); err == nil ||
		!strings.Contains(err.Error(), "decode pull-request landing receipt") {
		t.Fatalf("undecodable receipt error = %v", err)
	}
	state.answer(t, "view-exit", "1")
	if _, _, err := pullRequestLandingReceipt(context.Background(), receipt, WorktreeMergeLandOptions{Timeout: time.Minute}); err == nil ||
		!strings.Contains(err.Error(), "read pull-request landing receipt") {
		t.Fatalf("unreadable receipt error = %v", err)
	}
}

const orchCovCommitPullsScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  if [ -f "$S/pulls" ]; then cat "$S/pulls"; fi
  exit "$(cat "$S/pulls-exit")"
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func TestOrchCovFindExactOpenPullRequestMatchesEveryMutableIdentity(t *testing.T) {
	receipt := orchCovExactMergeReceipt(t)
	matching := func(sha, ref, repo, base, url string) string {
		return `[{"html_url":"` + url + `","state":"open","head":{"ref":"` + ref + `","sha":"` + sha + `","repo":{"full_name":"` + repo + `"}},"base":{"ref":"` + base + `"}}]`
	}
	for _, test := range []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{
			name: "matches",
			body: matching(receipt.Candidate.SHA, "wb/cov/merge", "acme/app", "main", "https://example.test/acme/app/pull/41"),
			want: "https://example.test/acme/app/pull/41",
		},
		{
			name: "closed pull request is ignored",
			body: `[{"html_url":"https://example.test/acme/app/pull/41","state":"closed","head":{"ref":"wb/cov/merge","sha":"` + receipt.Candidate.SHA + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}]`,
		},
		{
			name: "a different branch is ignored",
			body: matching(receipt.Candidate.SHA, "other", "acme/app", "main", "https://example.test/acme/app/pull/41"),
		},
		{
			name:    "missing URL",
			body:    matching(receipt.Candidate.SHA, "wb/cov/merge", "acme/app", "main", ""),
			wantErr: "omitted its URL",
		},
		{
			name: "two matching pull requests",
			body: matching(receipt.Candidate.SHA, "wb/cov/merge", "acme/app", "main", "https://example.test/acme/app/pull/41")[:len(matching(receipt.Candidate.SHA, "wb/cov/merge", "acme/app", "main", "https://example.test/acme/app/pull/41"))-1] +
				`,` + matching(receipt.Candidate.SHA, "wb/cov/merge", "acme/app", "main", "https://example.test/acme/app/pull/42")[1:],
			wantErr: "multiple matching open pull requests",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := orchCovScriptState(t, orchCovCommitPullsScript)
			state.answer(t, "pulls", test.body)
			state.answer(t, "pulls-exit", "0")
			url, err := findExactOpenWorktreeMergePullRequest(context.Background(), receipt)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || url != test.want {
				t.Fatalf("url = %q, err %v, want %q", url, err, test.want)
			}
		})
	}
}

func TestOrchCovFindExactOpenPullRequestFailsClosedOnUnusableReads(t *testing.T) {
	receipt := orchCovExactMergeReceipt(t)
	state := orchCovScriptState(t, orchCovCommitPullsScript)
	state.answer(t, "pulls", "not json")
	state.answer(t, "pulls-exit", "0")
	if _, err := findExactOpenWorktreeMergePullRequest(context.Background(), receipt); err == nil ||
		!strings.Contains(err.Error(), "decode pull requests for exact candidate") {
		t.Fatalf("undecodable query error = %v", err)
	}
	state.answer(t, "pulls-exit", "1")
	if _, err := findExactOpenWorktreeMergePullRequest(context.Background(), receipt); err == nil ||
		!strings.Contains(err.Error(), "query pull requests for exact candidate") {
		t.Fatalf("unreadable query error = %v", err)
	}
}

func TestOrchCovTerminalWorkLogExpectationsNamesEveryTerminalCheckout(t *testing.T) {
	valid := WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main",
		Candidate: WorktreeMergeCandidate{Task: "candidate-task", Worktree: "/candidate", Branch: "wb/merge", SHA: "aaa"},
		Sources: []WorktreeMergeSource{
			{Task: "source-b", Worktree: "/b", Branch: "wb/b", SHA: "bbb"},
			{Task: "source-a", Worktree: "/a", Branch: "wb/a", SHA: "ccc"},
		},
		RebatchedCandidates: []WorktreeMergeCandidate{
			{Task: "rebatch", Worktree: "/r", Branch: "wb/r", SHA: "ddd"},
			{Task: "rebatch", Worktree: "/r", Branch: "wb/r", SHA: "ddd"},
		},
	}
	expectations, err := terminalWorkLogExpectations(valid)
	if err != nil {
		t.Fatal(err)
	}
	tasks := make([]string, 0, len(expectations))
	for _, expectation := range expectations {
		tasks = append(tasks, expectation.Task)
	}
	if strings.Join(tasks, ",") != "candidate-task,rebatch,source-a,source-b" {
		t.Fatalf("expectations = %v, want them deduped and sorted by task", tasks)
	}
	for _, expectation := range expectations {
		if expectation.Repository != "acme/app" {
			t.Fatalf("expectation = %+v", expectation)
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
		wantIn string
	}{
		{name: "no candidate identity", mutate: func(r *WorktreeMergeReceipt) { r.Candidate.Branch = "" }, wantIn: "lacks exact candidate identity"},
		{name: "no source identity", mutate: func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = "" }, wantIn: "lacks exact source identity"},
		{name: "no rebatched candidate identity", mutate: func(r *WorktreeMergeReceipt) { r.RebatchedCandidates[0].Worktree = "" }, wantIn: "lacks exact rebatched candidate identity"},
		{name: "conflicting source identity", mutate: func(r *WorktreeMergeReceipt) {
			r.Sources = append(r.Sources, WorktreeMergeSource{Task: "source-a", Worktree: "/elsewhere", Branch: "wb/a", SHA: "ccc"})
		}, wantIn: "conflicting terminal cleanup identities for task source-a"},
		{name: "conflicting rebatched identity", mutate: func(r *WorktreeMergeReceipt) {
			r.RebatchedCandidates = append(r.RebatchedCandidates, WorktreeMergeCandidate{Task: "rebatch", Worktree: "/elsewhere", Branch: "wb/r", SHA: "ddd"})
		}, wantIn: "conflicting terminal cleanup identities for task rebatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := valid
			receipt.Sources = append([]WorktreeMergeSource(nil), valid.Sources...)
			receipt.RebatchedCandidates = append([]WorktreeMergeCandidate(nil), valid.RebatchedCandidates...)
			test.mutate(&receipt)
			if _, err := terminalWorkLogExpectations(receipt); err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error = %v, want %q", err, test.wantIn)
			}
		})
	}
}

// orchCovTar builds a tar archive in memory and returns its bytes.
func orchCovTar(t *testing.T, write func(*tar.Writer)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	write(writer)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func orchCovWriteArchive(t *testing.T, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.tar")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOrchCovExtractWorktreeMergeArchiveRestoresEveryEntryKind(t *testing.T) {
	t.Parallel()
	archive := orchCovTar(t, func(writer *tar.Writer) {
		for _, header := range []*tar.Header{
			{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "dir/file.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len("hello\n"))},
			{Name: "dir/link.txt", Typeflag: tar.TypeSymlink, Linkname: "file.txt", Mode: 0o777},
			{Name: "pax", Typeflag: tar.TypeXGlobalHeader},
		} {
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Typeflag == tar.TypeReg {
				if _, err := writer.Write([]byte("hello\n")); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	destination := t.TempDir()
	if err := extractWorktreeMergeArchive(orchCovWriteArchive(t, archive), destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "dir", "file.txt"))
	if err != nil || string(contents) != "hello\n" {
		t.Fatalf("extracted file = %q, err %v", contents, err)
	}
	target, err := os.Readlink(filepath.Join(destination, "dir", "link.txt"))
	if err != nil || target != "file.txt" {
		t.Fatalf("extracted symlink = %q, err %v", target, err)
	}
}

func TestOrchCovExtractWorktreeMergeArchiveRefusesUnsafeEntries(t *testing.T) {
	t.Parallel()
	destination := t.TempDir()
	write := func(header *tar.Header) string {
		return orchCovWriteArchive(t, orchCovTar(t, func(writer *tar.Writer) {
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
		}))
	}
	for _, test := range []struct {
		name   string
		header *tar.Header
		wantIn string
	}{
		{name: "absolute path", header: &tar.Header{Name: "/etc/passwd", Typeflag: tar.TypeReg}, wantIn: "unsafe archived path"},
		{name: "parent traversal", header: &tar.Header{Name: "../escape.txt", Typeflag: tar.TypeReg}, wantIn: "unsafe archived path"},
		{name: "absolute symlink", header: &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, wantIn: "unsafe archived symlink"},
		{name: "escaping symlink", header: &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"}, wantIn: "unsafe archived symlink"},
		{name: "unsupported entry", header: &tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}, wantIn: "unsupported archived entry"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := extractWorktreeMergeArchive(write(test.header), destination)
			if err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error = %v, want %q", err, test.wantIn)
			}
		})
	}
	if err := extractWorktreeMergeArchive(filepath.Join(t.TempDir(), "absent.tar"), destination); err == nil {
		t.Fatal("a missing archive was accepted")
	}
	// A truncated archive fails while reading the next header rather than
	// silently producing a partial tree.
	truncated := orchCovTar(t, func(writer *tar.Writer) {
		if err := writer.WriteHeader(&tar.Header{Name: "a.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
	})
	if err := extractWorktreeMergeArchive(orchCovWriteArchive(t, truncated[:len(truncated)-300]), destination); err == nil {
		t.Fatal("a truncated archive was accepted")
	}
}

func TestOrchCovSameLegacyIdentityComparesEveryField(t *testing.T) {
	t.Parallel()
	failure := WorktreeMergeLegacyValidationFailureIdentity{
		ID: "id", Status: "landed", ReceiptPath: "/r.json", AcknowledgementPath: "/a.json",
		ReceiptSHA256: "sha", ReceiptID: "receipt", Lane: "lane", Repository: "acme/app",
		Target: "main", ReceiptTargetSHA: "t1", CurrentTargetSHA: "t2",
		Candidate: WorktreeMergeCandidate{Task: "t", Worktree: "/w", Branch: "b", SHA: "s"}, ClaimBaseSHA: "b1",
		Sources: []WorktreeMergeSource{{Task: "t", Worktree: "/w", Branch: "b", SHA: "s"}},
		Actor:   "reviewer", Reason: "audited",
	}
	if !sameLegacyValidationFailureIdentity(failure, failure) {
		t.Fatal("an identity did not equal itself")
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeLegacyValidationFailureIdentity)
	}{
		{name: "ID", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.ID = "other" }},
		{name: "Status", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Status = "failed" }},
		{name: "ReceiptPath", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.ReceiptPath = "/other.json" }},
		{name: "AcknowledgementPath", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.AcknowledgementPath = "/other.json" }},
		{name: "ReceiptSHA256", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.ReceiptSHA256 = "other" }},
		{name: "ReceiptID", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.ReceiptID = "other" }},
		{name: "Lane", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Lane = "other" }},
		{name: "Repository", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Repository = "other/app" }},
		{name: "Target", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Target = "other" }},
		{name: "ReceiptTargetSHA", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.ReceiptTargetSHA = "other" }},
		{name: "CurrentTargetSHA", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.CurrentTargetSHA = "other" }},
		{name: "Candidate", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Candidate.SHA = "other" }},
		{name: "ClaimBaseSHA", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.ClaimBaseSHA = "other" }},
		{name: "Sources", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Sources = nil }},
		{name: "Actor", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Actor = "other" }},
		{name: "Reason", mutate: func(i *WorktreeMergeLegacyValidationFailureIdentity) { i.Reason = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := failure
			other.Sources = append([]WorktreeMergeSource(nil), failure.Sources...)
			test.mutate(&other)
			if sameLegacyValidationFailureIdentity(failure, other) {
				t.Fatalf("a different %s compared equal", test.name)
			}
		})
	}

	conflict := WorktreeMergeLegacyConflictIdentity{
		ID: "id", Status: "conflict", ReceiptPath: "/r.json", AcknowledgementPath: "/a.json",
		ReceiptSHA256: "sha", ReceiptID: "receipt", Lane: "lane", Repository: "acme/app",
		Target: "main", ReceiptTargetSHA: "t1", CurrentTargetSHA: "t2",
		Candidate: WorktreeMergeCandidate{Task: "t", Worktree: "/w", Branch: "b", SHA: "s"}, ClaimBaseSHA: "b1",
		Sources: []WorktreeMergeSource{{Task: "t", Worktree: "/w", Branch: "b", SHA: "s"}},
		Actor:   "reviewer", Reason: "audited",
	}
	if !sameLegacyConflictIdentity(conflict, conflict) {
		t.Fatal("a conflict identity did not equal itself")
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeLegacyConflictIdentity)
	}{
		{name: "ID", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.ID = "other" }},
		{name: "Status", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Status = "other" }},
		{name: "ReceiptPath", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.ReceiptPath = "other" }},
		{name: "AcknowledgementPath", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.AcknowledgementPath = "other" }},
		{name: "ReceiptSHA256", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.ReceiptSHA256 = "other" }},
		{name: "ReceiptID", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.ReceiptID = "other" }},
		{name: "Lane", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Lane = "other" }},
		{name: "Repository", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Repository = "other" }},
		{name: "Target", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Target = "other" }},
		{name: "ReceiptTargetSHA", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.ReceiptTargetSHA = "other" }},
		{name: "CurrentTargetSHA", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.CurrentTargetSHA = "other" }},
		{name: "Candidate", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Candidate.SHA = "other" }},
		{name: "ClaimBaseSHA", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.ClaimBaseSHA = "other" }},
		{name: "Sources", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Sources = nil }},
		{name: "Actor", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Actor = "other" }},
		{name: "Reason", mutate: func(i *WorktreeMergeLegacyConflictIdentity) { i.Reason = "other" }},
	} {
		t.Run("conflict "+test.name, func(t *testing.T) {
			other := conflict
			other.Sources = append([]WorktreeMergeSource(nil), conflict.Sources...)
			test.mutate(&other)
			if sameLegacyConflictIdentity(conflict, other) {
				t.Fatalf("a different %s compared equal", test.name)
			}
		})
	}
}

func TestOrchCovPersistAcknowledgementReportsAnUnusableDestination(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	writeEngineFile(t, file, "x")
	unusable := filepath.Join(file, "nested", "ack.json")

	if err := persistLandedFailureAcknowledgement(unusable, WorktreeMergeLandedFailureAcknowledgement{}); err == nil {
		t.Fatal("landed-failure acknowledgement accepted an unusable destination")
	}
	if err := persistValidationFailureSupersession(unusable, WorktreeMergeValidationFailureSupersession{}); err == nil {
		t.Fatal("validation-failure supersession accepted an unusable destination")
	}
	if err := persistSelfSupersessionCorrection(unusable, WorktreeMergeSelfSupersessionCorrection{}); err == nil {
		t.Fatal("self-supersession correction accepted an unusable destination")
	}
	if err := persistMissingCleanupAcknowledgement(unusable, WorktreeMergeMissingCleanupAcknowledgement{}); err == nil {
		t.Fatal("missing-cleanup acknowledgement accepted an unusable destination")
	}
	if err := persistPreparedWorktreeMergeRebatch(unusable, WorktreeMergePreparedRebatch{}); err == nil {
		t.Fatal("prepared rebatch accepted an unusable destination")
	}
	if err := persistReceiptCollisionAcknowledgement(unusable, WorktreeMergeReceiptCollisionAcknowledgement{}); err == nil {
		t.Fatal("receipt-collision acknowledgement accepted an unusable destination")
	}
	if err := persistConflictCandidateAdvance(unusable, WorktreeMergeConflictCandidateAdvance{}); err == nil {
		t.Fatal("conflict candidate advance accepted an unusable destination")
	}
	if err := persistLegacyValidationFailureIdentity(unusable, WorktreeMergeLegacyValidationFailureIdentity{}); err == nil {
		t.Fatal("legacy validation-failure identity accepted an unusable destination")
	}
	if err := persistLegacyConflictIdentity(unusable, WorktreeMergeLegacyConflictIdentity{}); err == nil {
		t.Fatal("legacy conflict identity accepted an unusable destination")
	}
}

func TestOrchCovWorktreeMergeReceiptSHA256FingerprintsTheFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	writeEngineFile(t, path, `{"v":1}`)
	digest, err := worktreeMergeReceiptSHA256(path)
	if err != nil || digest == "" {
		t.Fatalf("digest = %q, err %v", digest, err)
	}
	writeEngineFile(t, path, `{"v":2}`)
	other, err := worktreeMergeReceiptSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if other == digest {
		t.Fatal("a changed receipt produced the same digest")
	}
	if _, err := worktreeMergeReceiptSHA256(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("a missing receipt produced a digest")
	}
}
