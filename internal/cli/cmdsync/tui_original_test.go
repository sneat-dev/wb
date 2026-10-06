//go:build e2e

package cmdsync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/tui"
)

// TestE2ERunSyncTUITracksNativeReposAndPreservesCheckouts drives the real
// worker pool through Bubble Tea's headless program options. The event filter
// observes progress without replacing Git, Sync, or the progress model.
func TestE2ERunSyncTUITracksNativeReposAndPreservesCheckouts(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	origins := t.TempDir()
	for _, slug := range []string{"acme/one", "acme/two", "beta/three"} {
		fetchableFixtureAt(t, origins, strings.ReplaceAll(slug, "/", "-")+"-origin.git",
			filepath.Join(projects, filepath.FromSlash(slug)), slug+"\n")
	}
	repos, err := discover.ScanLocal(projects)
	if err != nil || len(repos) != 3 {
		t.Fatalf("scan local repositories = %+v, %v", repos, err)
	}
	type checkoutState struct {
		head      []byte
		headSHA   string
		refs      string
		gitConfig []byte
		status    gitops.RepoStatus
	}
	before := make(map[string]checkoutState, len(repos))
	orgTotal := make(map[string]int)
	for index := range repos {
		repos[index].Remote = true // The local origin is this fixture's GitHub inventory entry.
		repo := repos[index]
		head, err := os.ReadFile(filepath.Join(repo.Path, ".git", "HEAD"))
		if err != nil {
			t.Fatal(err)
		}
		headSHA, err := gitops.HeadSHA(repo.Path)
		if err != nil {
			t.Fatal(err)
		}
		refs := scratchGit(t, repo.Path, "for-each-ref", "--format=%(refname)=%(objectname)")
		gitConfig, err := os.ReadFile(filepath.Join(repo.Path, ".git", "config"))
		if err != nil {
			t.Fatal(err)
		}
		status, err := gitops.Status(repo.Path)
		if err != nil {
			t.Fatal(err)
		}
		before[repo.Slug()] = checkoutState{head: head, headSHA: headSHA, refs: refs, gitConfig: gitConfig, status: status}
		orgTotal[repo.Org]++
	}

	for _, workers := range []int{1, 2} {
		started := make(map[string]bool)
		active := make(map[string]bool)
		done := make(map[string]bool)
		var completionOrder []string
		var problems []string
		maxActive := 0
		syncDone := false
		filter := tea.WithFilter(func(_ tea.Model, message tea.Msg) tea.Msg {
			switch event := message.(type) {
			case tui.RepoStarted:
				slug := event.Org + "/" + event.Name
				if started[slug] || syncDone {
					problems = append(problems, "duplicate or late start: "+slug)
				}
				started[slug], active[slug] = true, true
				if len(active) > maxActive {
					maxActive = len(active)
				}
			case tui.RepoDone:
				slug := event.Result.Repo.Slug()
				if !active[slug] || done[slug] || syncDone {
					problems = append(problems, "completion without one active start: "+slug)
				}
				delete(active, slug)
				done[slug] = true
				completionOrder = append(completionOrder, slug)
			case tui.SyncDone:
				if syncDone || len(done) != len(repos) || len(active) != 0 {
					problems = append(problems, fmt.Sprintf("premature completion: done=%d active=%d", len(done), len(active)))
				}
				syncDone = true
			}
			return message
		})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var errOut bytes.Buffer
		results := runSyncTUI(ctx, repos, orgTotal, projects, workers, true, false, &errOut,
			tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(),
			tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=dumb"}),
			tea.WithContext(ctx), filter)
		cancel()
		if errOut.Len() != 0 || len(problems) != 0 {
			t.Fatalf("workers=%d: tui stderr=%q, events=%v", workers, errOut.String(), problems)
		}
		if !syncDone || len(started) != len(repos) || len(done) != len(repos) || maxActive > workers {
			t.Fatalf("workers=%d: started=%d done=%d maxActive=%d syncDone=%t", workers, len(started), len(done), maxActive, syncDone)
		}
		if len(results) != len(repos) {
			t.Fatalf("workers=%d: results=%+v", workers, results)
		}
		for i, result := range results {
			if result.Repo.Slug() != completionOrder[i] || result.Status != fleetsync.Pulled || !result.PullPlanned || result.Err != nil {
				t.Errorf("workers=%d result[%d]=%+v, completion=%v", workers, i, result, completionOrder)
			}
		}
	}
	for _, repo := range repos {
		head, err := os.ReadFile(filepath.Join(repo.Path, ".git", "HEAD"))
		if err != nil {
			t.Fatal(err)
		}
		headSHA, err := gitops.HeadSHA(repo.Path)
		if err != nil {
			t.Fatal(err)
		}
		refs := scratchGit(t, repo.Path, "for-each-ref", "--format=%(refname)=%(objectname)")
		gitConfig, err := os.ReadFile(filepath.Join(repo.Path, ".git", "config"))
		if err != nil {
			t.Fatal(err)
		}
		status, err := gitops.Status(repo.Path)
		if err != nil {
			t.Fatal(err)
		}
		initial := before[repo.Slug()]
		if !bytes.Equal(head, initial.head) || headSHA != initial.headSHA || refs != initial.refs ||
			!bytes.Equal(gitConfig, initial.gitConfig) || !reflect.DeepEqual(status, initial.status) {
			t.Errorf("dry-run sync changed %s: HEAD %s -> %s, refs %q -> %q, config changed=%t, status %+v -> %+v",
				repo.Slug(), initial.headSHA, headSHA, initial.refs, refs, !bytes.Equal(gitConfig, initial.gitConfig), initial.status, status)
		}
	}
}

// TestE2ERunSyncTUIReportsProgramCancellation covers the program-error path
// without requiring a real terminal or relying on a timing race.
func TestE2ERunSyncTUIReportsProgramCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var errOut bytes.Buffer
	results := runSyncTUI(ctx, []discover.Repo{{Org: "acme", Name: "one"}}, map[string]int{"acme": 1}, t.TempDir(), 1, true, false, &errOut,
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(),
		tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=dumb"}), tea.WithContext(ctx))
	if !strings.Contains(errOut.String(), "tui error:") || !strings.Contains(errOut.String(), context.Canceled.Error()) {
		t.Fatalf("canceled TUI diagnostic = %q", errOut.String())
	}
	if len(results) != 0 {
		t.Fatalf("canceled TUI results = %+v, want none", results)
	}
}

func fetchableFixtureAt(t *testing.T, root, originName, clonePath, content string) {
	t.Helper()
	origin := filepath.Join(root, originName)
	if _, statErr := os.Stat(origin); statErr != nil {
		testenv.InitBareRemoteForTest(t, origin)
	}
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(clonePath), "clone", origin, filepath.Base(clonePath))
	runGit(t, clonePath, "config", "user.email", "wb@example.test")
	runGit(t, clonePath, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(clonePath, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clonePath, "add", ".")
	runGit(t, clonePath, "commit", "-m", "init")
	runGit(t, clonePath, "push", "-u", "origin", "main")
}
func runGit(t *testing.T, dir string, args ...string) { t.Helper(); testenv.Git(t, dir, args...) }

func scratchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
