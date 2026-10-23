//go:build e2e

package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2EAbsorbedConflictLineEvidenceUsesCommittedObjects(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	repo := fixture.canonical
	commit := func(files map[string]string) string {
		t.Helper()
		for name, contents := range files {
			if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		runEngineGit(t, repo, "add", "-A")
		runEngineGit(t, repo, "commit", "-m", "committed line evidence")
		return strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	}
	base := commit(map[string]string{"evidence.jsonl": "old\ncontext\n", "empty.jsonl": "", "multiline.jsonl": "a\n\nb\n", "newline.jsonl": "\n", "unterminated.jsonl": "last"})
	source := commit(map[string]string{"evidence.jsonl": "context\nnew\n\nsame\nsame\n+kept\n++excluded\n+++excluded\n"})
	absorbed := commit(map[string]string{"evidence.jsonl": "same\ncontext\n+kept\nnew\n\n"})
	missingLine := commit(map[string]string{"evidence.jsonl": "same\ncontext\n+kept\n\n"})
	if err := os.Remove(filepath.Join(repo, "evidence.jsonl")); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, repo, "add", "-A")
	runEngineGit(t, repo, "commit", "-m", "target without evidence path")
	absent := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	frozenHead := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	t.Cleanup(func() {
		if got := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD")); got != frozenHead {
			t.Errorf("read-only corpus HEAD=%s want %s", got, frozenHead)
		}
		if got := runEngineGit(t, repo, "status", "--porcelain"); got != "" {
			t.Errorf("corpus mutated: %s", got)
		}
	})
	t.Run("diff preserves order and membership parser", func(t *testing.T) {
		t.Parallel()
		got, err := gitDiffAddedLines(context.Background(), repo, base, source, "evidence.jsonl")
		want := []string{"new", "", "same", "same", "+kept"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("added=%q want=%q err=%v", got, want, err)
		}
	})
	t.Run("diff refuses invalid revision", func(t *testing.T) {
		t.Parallel()
		got, err := gitDiffAddedLines(context.Background(), repo, "absent-revision", source, "evidence.jsonl")
		if err == nil || got != nil {
			t.Fatalf("added=%q err=%v", got, err)
		}
	})
	for _, tc := range []struct {
		name, revision, path string
		present              bool
		lines                []string
	}{
		{"empty blob", base, "empty.jsonl", true, []string{}},
		{"newline blob", base, "newline.jsonl", true, []string{}},
		{"embedded blank and trailing newline", base, "multiline.jsonl", true, []string{"a", "", "b"}},
		{"unterminated blob", base, "unterminated.jsonl", true, []string{"last"}},
		{"absent path", base, "absent.jsonl", false, nil},
		{"invalid revision", "absent-revision", "multiline.jsonl", false, nil},
	} {
		t.Run("file "+tc.name, func(t *testing.T) {
			t.Parallel()
			got, present := gitFileLines(context.Background(), repo, tc.revision, tc.path)
			if present != tc.present || !reflect.DeepEqual(got, tc.lines) {
				t.Fatalf("present=%v lines=%q want present=%v lines=%q", present, got, tc.present, tc.lines)
			}
		})
	}
	for _, tc := range []struct {
		name, base, source, target string
		want                       int
		errorText                  string
	}{
		{"no added lines ignores missing target", base, base, "absent-revision", 0, ""},
		{"reordered target with one duplicate occurrence", base, source, absorbed, 5, ""},
		{"target path absent", base, source, absent, 0, fmt.Sprintf("path %q is not lines-absorbed: current target %s does not carry the file at all", "evidence.jsonl", absent)},
		{"target missing exact line", base, source, missingLine, 0, fmt.Sprintf("path %q is not lines-absorbed: an added line is not present verbatim in the current target %s", "evidence.jsonl", missingLine)},
		{"diff refusal", "absent-revision", source, absorbed, 0, "diff added lines for \"evidence.jsonl\":"},
	} {
		t.Run("proof "+tc.name, func(t *testing.T) {
			t.Parallel()
			added, matched, err := proveLinesAbsorbedPath(context.Background(), repo, tc.base, tc.source, tc.target, "evidence.jsonl")
			if added != tc.want || matched != tc.want {
				t.Fatalf("added=%d matched=%d want=%d", added, matched, tc.want)
			}
			if tc.errorText == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || (tc.name == "diff refusal" && !strings.HasPrefix(err.Error(), tc.errorText)) || (tc.name != "diff refusal" && err.Error() != tc.errorText) {
				t.Fatalf("err=%v want=%q", err, tc.errorText)
			}
		})
	}
}

func TestE2EAbsorbedConflictSourceRecoveryUsesPrivateOriginObjects(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	repo := fixture.canonical
	initial := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	receivers := map[string]string{}
	for _, name := range []string{"fetch published source", "missing origin branch", "fetched branch lacks receipted object"} {
		receiver := filepath.Join(t.TempDir(), "receiver")
		runEngineGit(t, filepath.Dir(receiver), "clone", "--no-hardlinks", "--single-branch", "--branch", "main", fixture.repository.CloneURL, receiver)
		receivers[name] = receiver
	}
	runEngineGit(t, repo, "checkout", "-b", "published-source")
	if err := os.WriteFile(filepath.Join(repo, "published.txt"), []byte("actual published source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, repo, "add", "-A")
	runEngineGit(t, repo, "commit", "-m", "published source")
	published := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	runEngineGit(t, repo, "push", "origin", "published-source")
	runEngineGit(t, repo, "checkout", "-b", "unpublished-source", initial)
	if err := os.WriteFile(filepath.Join(repo, "unpublished.txt"), []byte("unpublished private object\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, repo, "add", "-A")
	runEngineGit(t, repo, "commit", "-m", "unpublished source")
	unpublished := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	t.Run("local object needs no fetch", func(t *testing.T) {
		t.Parallel()
		err := resolveAbsorbedConflictSourceObject(context.Background(), repo, WorktreeMergeSource{SHA: unpublished, Branch: "nonexistent-branch"})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD")); got != unpublished {
			t.Fatalf("HEAD=%s want=%s", got, unpublished)
		}
	})
	for _, tc := range []struct {
		name, sha, branch string
		wantError         string
		present           bool
	}{
		{"fetch published source", published, "published-source", "", true},
		{"missing origin branch", published, "nonexistent-branch", "fetch origin nonexistent-branch to resolve receipted source " + published + ":", false},
		{"fetched branch lacks receipted object", unpublished, "published-source", "receipted source commit " + unpublished + " is reachable from neither the local object store nor the current origin published-source", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receiver := receivers[tc.name]
			if _, _, err := runCommand(context.Background(), defaultRunner, 0, 0, receiver, "git", "cat-file", "-e", tc.sha+"^{commit}"); err == nil {
				t.Fatal("receiver already has source object before recovery")
			}
			beforeRefs := runEngineGit(t, receiver, "show-ref")
			err := resolveAbsorbedConflictSourceObject(context.Background(), receiver, WorktreeMergeSource{SHA: tc.sha, Branch: tc.branch})
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || (tc.name == "missing origin branch" && !strings.HasPrefix(err.Error(), tc.wantError)) || (tc.name != "missing origin branch" && err.Error() != tc.wantError) {
				t.Fatalf("err=%v want=%q", err, tc.wantError)
			}
			_, _, objectErr := runCommand(context.Background(), defaultRunner, 0, 0, receiver, "git", "cat-file", "-e", tc.sha+"^{commit}")
			if (objectErr == nil) != tc.present {
				t.Fatalf("object present=%v want=%v err=%v", objectErr == nil, tc.present, objectErr)
			}
			if got := strings.TrimSpace(runEngineGit(t, receiver, "rev-parse", "HEAD")); got != initial {
				t.Fatalf("receiver HEAD=%s want=%s", got, initial)
			}
			if tc.name == "missing origin branch" {
				if got := runEngineGit(t, receiver, "show-ref"); got != beforeRefs {
					t.Fatalf("failed fetch changed refs: before=%q after=%q", beforeRefs, got)
				}
			} else if got := strings.TrimSpace(runEngineGit(t, receiver, "rev-parse", "FETCH_HEAD")); got != published {
				t.Fatalf("fetched=%s want authentic published=%s", got, published)
			}
		})
	}
}
