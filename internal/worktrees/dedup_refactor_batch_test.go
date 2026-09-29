package worktrees

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDedupRefactorBatchIdentifiersAndGitDirectories(t *testing.T) {
	t.Parallel()

	sha40 := strings.Repeat("a", 40)
	sha64 := strings.Repeat("f", 64)
	if !isGitObjectID(sha40) || !isGitObjectID(sha64) || isGitObjectID(strings.Repeat("a", 41)) || isGitObjectID(strings.Repeat("A", 40)) {
		t.Fatal("Git object ID validation accepted an invalid ID or rejected a valid one")
	}
	if !isGitRevisionID("09af") || isGitRevisionID("abc") || isGitRevisionID(strings.Repeat("a", 65)) || isGitRevisionID("abcdZ") {
		t.Fatal("Git revision ID validation accepted an invalid ID or rejected a valid one")
	}
	if !hasOnlyLowerHexCharacters("09af") || hasOnlyLowerHexCharacters("09aF") {
		t.Fatal("lower-hex character validation is inconsistent")
	}

	var calls [][]string
	gitDir, commonDir, err := resolveGitDirectories(func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return filepath.Join("root", "repo", "..", "repo", ".git"), nil
		}
		return filepath.Join("root", "repo", ".git", ".."), nil
	})
	if err != nil || gitDir != filepath.Join("root", "repo", ".git") || commonDir != filepath.Join("root", "repo") {
		t.Fatalf("resolved Git directories = %q, %q, %v", gitDir, commonDir, err)
	}
	wantCalls := [][]string{{"rev-parse", "--absolute-git-dir"}, {"rev-parse", "--path-format=absolute", "--git-common-dir"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("Git directory calls = %#v, want %#v", calls, wantCalls)
	}
	firstErr := errors.New("first")
	if _, _, err := resolveGitDirectories(func(...string) (string, error) { return "", firstErr }); !errors.Is(err, firstErr) {
		t.Fatalf("first Git directory error = %v", err)
	}
	secondErr := errors.New("second")
	call := 0
	if _, _, err := resolveGitDirectories(func(...string) (string, error) {
		call++
		if call == 1 {
			return "git-dir", nil
		}
		return "", secondErr
	}); !errors.Is(err, secondErr) {
		t.Fatalf("second Git directory error = %v", err)
	}
}

//nolint:paralleltest // newGitFixture changes process environment variables.
func TestDedupRefactorBatchGitDirectoryWrappers(t *testing.T) {
	fixture := newGitFixture(t)
	gitDir, commonDir, err := gitDirectories(context.Background(), fixture.canonical)
	if err != nil || gitDir == "" || commonDir == "" {
		t.Fatalf("worktree Git directories = %q, %q, %v", gitDir, commonDir, err)
	}
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	canonicalGitDir, canonicalCommonDir, err := gitDirectoriesCanonical(context.Background(), canonical)
	if err != nil || canonicalGitDir != gitDir || canonicalCommonDir != commonDir {
		t.Fatalf("canonical Git directories = %q, %q, %v; want %q, %q", canonicalGitDir, canonicalCommonDir, err, gitDir, commonDir)
	}
}

func TestDedupRefactorBatchMergedReceipt(t *testing.T) {
	t.Parallel()

	mergedAt := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	candidate := githubPullRequest{
		Number: 7, URL: "https://example.test/pull/7", State: "closed", MergedAt: &mergedAt,
		Head: githubRef{Ref: "feature", SHA: strings.Repeat("a", 40)},
		Base: githubRef{Ref: "main", SHA: strings.Repeat("b", 40)}, MergeCommitSHA: strings.Repeat("c", 40),
	}
	receipt := mergedPullRequestReceipt("acme/app", candidate)
	if receipt.Number != candidate.Number || receipt.Repository != "acme/app" || receipt.State != "MERGED" ||
		receipt.Base != candidate.Base.Ref || receipt.BaseSHA != candidate.Base.SHA || receipt.HeadSHA != candidate.Head.SHA ||
		receipt.MergeSHA != candidate.MergeCommitSHA || receipt.Merged != candidate.MergedAt {
		t.Fatalf("merged pull-request receipt = %#v", receipt)
	}
	selected, err := selectExactDeletedTargetDefaultBranchReceipt(
		context.Background(), "acme/app", []githubPullRequest{{}, candidate, candidate},
		"feature", "main", candidate.Head.SHA,
	)
	if err != nil || selected == nil || selected.Number != candidate.Number {
		t.Fatalf("selected exact receipt = %#v, %v", selected, err)
	}
	if selected, err := selectExactDeletedTargetDefaultBranchReceipt(
		context.Background(), "acme/app", nil, "feature", "main", candidate.Head.SHA,
	); err != nil || selected != nil {
		t.Fatalf("missing exact receipt = %#v, %v", selected, err)
	}
}
