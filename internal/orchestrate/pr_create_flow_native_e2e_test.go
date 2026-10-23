//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// createFlowRunner delegates every successful Git observation/mutation to the
// native runner. Only explicit negative argv and hosted mutation replies are
// controlled; no Git/custody success is synthesized.
type createFlowRunner struct {
	runner.Runner
	t                             *testing.T
	worktree                      string
	fail                          []string
	cause                         error
	consumed, publications        int
	calls                         [][]string
	url                           string
	afterStatus, afterPublication func()
	expectedPublication           []string
}

func (r *createFlowRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	argv := append([]string{name}, args...)
	r.calls = append(r.calls, argv)
	if dir != r.worktree {
		r.t.Fatalf("runner cwd=%q want=%q argv=%q", dir, r.worktree, argv)
	}
	status := reflect.DeepEqual(argv, []string{"git", "status", "--porcelain=v1", "-z"})
	if status {
		if _, ok := ctx.Deadline(); ok {
			r.t.Fatal("dirty status acquired owner timeout")
		}
		if opts.CaptureCombined {
			r.t.Fatal("dirty status capture changed")
		}
	}
	if reflect.DeepEqual(argv, r.fail) {
		r.consumed++
		return runner.Result{ExitCode: 1}, r.cause
	}
	if name == "gh" {
		if !reflect.DeepEqual(argv, r.expectedPublication) {
			r.t.Fatalf("hosted mutation argv=%q want=%q", argv, r.expectedPublication)
		}
		r.publications++
		if len(args) < 2 || args[0] != "pr" || (args[1] != "create" && args[1] != "edit") {
			r.t.Fatalf("unexpected hosted mutation %q", argv)
		}
		if r.afterPublication != nil {
			r.afterPublication()
		}
		return runner.Result{CombinedOutput: r.url + "\n"}, nil
	}
	if name != "git" {
		r.t.Fatalf("unexpected command %q", argv)
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if err == nil && status && r.afterStatus != nil {
		r.afterStatus()
	}
	return result, err
}
func createFlowReader(t *testing.T, worktree, repository, branch, reply string, reads *int) context.Context {
	t.Helper()
	expected := []string{"pr", "list", "--repo", repository, "--head", branch, "--state", "open", "--json", "url,baseRefName,body", "--jq", `.[0] | (.url + "\t" + .baseRefName + "\t" + (.body // ""))`}
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
			*reads++
			if dir != worktree || !reflect.DeepEqual(args, expected) {
				t.Fatalf("hosted list cwd=%q argv=%q", dir, args)
			}
			return []byte(reply), nil
		},
		Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
			t.Fatal("unexpected hosted Get")
			return githubobserver.Response{}, errors.New("unexpected Get")
		},
		Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
			t.Fatal("unexpected hosted Execute")
			return githubobserver.CommandResponse{}
		},
	})
}
func createFlowManaged(t *testing.T, hosted bool) (engineFixture, worktrees.CreateResult, worktrees.WorkLogView) {
	t.Helper()
	fixture := newExplicitRootEngineFixture(t)
	if hosted {
		path := filepath.Join(fixture.githubDir, "github.com", "acme", "app")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fixture.canonical, path); err != nil {
			t.Fatal(err)
		}
		fixture.canonical = path
		fixture.repository.Path = path
	}
	created := createMergeSource(t, fixture, "pr-create-flow", "feature/pr-create-flow", "flow.txt", "native flow change\n")
	guard, err := worktrees.Guard(context.Background(), created.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir})
	if err != nil || guard.Kind != "linked" || guard.Path != created.WorktreeDir {
		t.Fatalf("actual managed Guard=%+v err=%v", guard, err)
	}
	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: created.WorktreeDir})
	if err != nil || view.Manifest == nil || view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.ClaimPath == "" || view.Manifest.Repository != "acme/app" {
		t.Fatalf("actual managed WorkLog=%+v err=%v", view, err)
	}
	return fixture, created, view
}

func TestE2EPRCreateFlowNativeStageRefusals(t *testing.T) {
	t.Parallel()
	fixture, created, view := createFlowManaged(t, false)
	worktree := created.WorktreeDir
	head := runEngineGit(t, worktree, "rev-parse", "HEAD")
	remote := runEngineGit(t, fixture.repository.CloneURL, "show-ref")
	claim, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("exact native stage refused")
	for stageIndex, tc := range []struct {
		name   string
		argv   []string
		prefix string
	}{
		{"status", []string{"git", "status", "--porcelain=v1", "-z"}, "read worktree status:"},
		{"fetch", []string{"git", "fetch", "--no-tags", "origin", "main"}, "fetch base branch main:"},
		{"subjects", []string{"git", "log", "--format=%s", "origin/main..HEAD"}, "read commits ahead of main:"},
		{"head", []string{"git", "rev-parse", "HEAD"}, "git rev-parse HEAD:"},
		{"push", []string{"git", "push", "--set-upstream", "origin", "HEAD:refs/heads/feature/pr-create-flow"}, "push feature/pr-create-flow:"},
	} {
		//nolint:paralleltest // Rows reuse one prepared native repository; preceding fetches touch the same refs sequentially.
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			r := &createFlowRunner{Runner: defaultRunner, t: t, worktree: worktree, fail: tc.argv, cause: sentinel}
			ctx := createFlowReader(t, worktree, "acme/app", created.Branch, "", &reads)
			result, err := createPullRequest(ctx, PullRequestCreateOptions{Worktree: worktree, ProjectsRoot: fixture.githubDir, run: r, Timeout: 10 * time.Second})
			if !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), tc.prefix) || r.consumed != 1 || reads != 0 || r.publications != 0 || len(r.calls) != stageIndex+1 || !reflect.DeepEqual(r.calls[len(r.calls)-1], tc.argv) || result.URL != "" {
				t.Fatalf("result=%+v err=%v consumed=%d reads=%d calls=%q", result, err, r.consumed, reads, r.calls)
			}
			if runEngineGit(t, worktree, "rev-parse", "HEAD") != head || runEngineGit(t, fixture.repository.CloneURL, "show-ref") != remote {
				t.Fatal("read-only refusal changed native local/remote heads")
			}
			after, err := os.ReadFile(view.Claim.ClaimPath)
			if err != nil || !bytes.Equal(after, claim) {
				t.Fatalf("claim changed: %v", err)
			}
		})
	}
}

func TestE2EPRCreateFlowNativePublicationContracts(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"create refusal", "adopted edit refusal", "base mismatch", "missing body file", "valid binding", "binding collision", "nondecimal URL", "empty URL tail", "overflow URL", "land preflight"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture, created, view := createFlowManaged(t, false)
			wt := created.WorktreeDir
			reads := 0
			sentinel := errors.New("publication stage refused")
			reply := ""
			url := "https://github.com/acme/app/pull/42"
			opts := PullRequestCreateOptions{Worktree: wt, ProjectsRoot: fixture.githubDir, Body: "native flow body", Timeout: 10 * time.Second}
			r := &createFlowRunner{Runner: defaultRunner, t: t, worktree: wt, url: url}
			opts.run = r
			r.expectedPublication = []string{"gh", "pr", "create", "--repo", "acme/app", "--base", "main", "--head", created.Branch, "--title", "feat: add flow.txt", "--body", opts.Body}
			wantError := false
			sidecar := strings.TrimSuffix(view.Claim.ClaimPath, ".json") + worktreeclaims.PullRequestBindingSuffix
			before, err := os.ReadFile(view.Claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sidecar precondition=%v", err)
			}
			switch name {
			case "create refusal":
				r.fail = []string{"gh", "pr", "create", "--repo", "acme/app", "--base", "main", "--head", created.Branch, "--title", "feat: add flow.txt", "--body", opts.Body}
				r.cause = sentinel
				wantError = true
			case "adopted edit refusal":
				reply = url + "\tmain\tprior body"
				opts.Closes = []int{7}
				r.fail = []string{"gh", "pr", "edit", url, "--repo", "acme/app", "--body", "Closes #7\n\nprior body"}
				r.cause = sentinel
				r.expectedPublication = r.fail
				wantError = true
			case "base mismatch":
				reply = url + "\tdevelop\tprior body"
			case "missing body file":
				opts.Body = ""
				opts.BodyFile = filepath.Join(t.TempDir(), "absent")
				wantError = true
			case "binding collision":
				t.Cleanup(func() {
					if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Errorf("remove collision: %v", err)
					}
				})
				r.afterPublication = func() {
					if err := os.Mkdir(sidecar, 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "nondecimal URL":
				r.url = "https://github.com/acme/app/pull/not-a-number"
			case "empty URL tail":
				r.url = "https://github.com/acme/app/pull/"
			case "overflow URL":
				r.url = "https://github.com/acme/app/pull/99999999999999999999999999999999999"
			case "land preflight":
				opts.Land = true
				opts.AutoMerge = true
				calls := 0
				opts.LinkPreflight = func(repository string) error {
					calls++
					if repository != "acme/app" {
						t.Fatalf("preflight repository=%q", repository)
					}
					return sentinel
				}
				t.Cleanup(func() {
					if calls != 1 {
						t.Errorf("land preflight calls=%d", calls)
					}
				})
				wantError = true
			}
			ctx := createFlowReader(t, wt, "acme/app", created.Branch, reply, &reads)
			result, err := createPullRequest(ctx, opts)
			if wantError {
				if err == nil {
					t.Fatalf("expected refusal result=%+v", result)
				}
				if name == "missing body file" {
					var pe *os.PathError
					if !errors.As(err, &pe) || !strings.HasPrefix(err.Error(), "read --body-file "+opts.BodyFile+":") {
						t.Fatalf("body error=%v", err)
					}
				} else if !errors.Is(err, sentinel) {
					t.Fatalf("error identity=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if name == "base mismatch" {
				want := "pull request " + url + " is already open against develop, not main"
				if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalBaseMismatch || result.Reason != want || result.SanctionedCommand != "wb pr land "+url+", or close it, or pass --base develop" || r.publications != 0 {
					t.Fatalf("mismatch=%+v", result)
				}
			}
			if name == "create refusal" || name == "adopted edit refusal" {
				if r.consumed != 1 || result.URL != "" {
					t.Fatalf("routing consumed=%d result=%+v", r.consumed, result)
				}
			}
			if name == "missing body file" {
				if reads != 0 || r.publications != 0 {
					t.Fatalf("body refusal published reads=%d mutations=%d", reads, r.publications)
				}
			} else if reads != 1 {
				t.Fatalf("list reads=%d", reads)
			}
			nativeHead := strings.TrimSpace(runEngineGit(t, wt, "rev-parse", "HEAD"))
			remoteHead := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+created.Branch))
			if result.HeadSHA != nativeHead || remoteHead != nativeHead {
				t.Fatalf("native push result=%s local=%s remote=%s", result.HeadSHA, nativeHead, remoteHead)
			}
			after, err := os.ReadFile(view.Claim.ClaimPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("actual claim changed: %v", err)
			}
			if !wantError && name != "base mismatch" || name == "land preflight" {
				if result.Outcome != CreateSuccess || result.URL != r.url || result.AutoMergeArmed {
					t.Fatalf("created result=%+v", result)
				}
				if name == "binding collision" {
					info, err := os.Stat(sidecar)
					if err != nil || !info.IsDir() || !strings.Contains(result.Evidence["claim_binding_error"], "record task-to-pull-request binding") || result.Task != "" || result.ClaimID != "" {
						t.Fatalf("collision result=%+v info=%v err=%v", result, info, err)
					}
					if err := os.Remove(sidecar); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("collision cleanup=%v", err)
					}
				} else {
					raw, err := os.ReadFile(sidecar)
					if err != nil {
						t.Fatal(err)
					}
					var binding worktreeclaims.ClaimPullRequestBinding
					if err := json.Unmarshal(raw, &binding); err != nil {
						t.Fatal(err)
					}
					wantNumber := 42
					if name == "nondecimal URL" || name == "empty URL tail" || name == "overflow URL" {
						wantNumber = 0
					}
					if result.PullRequest != wantNumber || binding.PullRequest != wantNumber || binding.Repository != "acme/app" || binding.URL != r.url || binding.RecordedAt.IsZero() || result.ClaimID != view.Claim.ClaimID || result.Task == "" {
						t.Fatalf("actual binding=%+v result=%+v", binding, result)
					}
				}
			}
		})
	}
}

func TestE2EPRCreateFlowNativeManifestFallback(t *testing.T) {
	t.Parallel()
	for _, hosted := range []bool{false, true} {
		t.Run(fmt.Sprint(hosted), func(t *testing.T) {
			t.Parallel()
			fixture, created, view := createFlowManaged(t, hosted)
			wt := created.WorktreeDir
			manifest := filepath.Join(wt, ".wb", "local", "manifest.yaml")
			before, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			held := manifest + ".held"
			heldOnce := 0
			restore := func() {
				if _, err := os.Stat(held); err == nil {
					if err := os.Rename(held, manifest); err != nil {
						t.Errorf("restore owned manifest: %v", err)
					}
				}
			}
			t.Cleanup(restore)
			r := &createFlowRunner{Runner: defaultRunner, t: t, worktree: wt, url: "https://github.com/acme/app/pull/42"}
			r.afterStatus = func() {
				heldOnce++
				if err := os.Rename(manifest, held); err != nil {
					t.Fatal(err)
				}
			}
			r.expectedPublication = []string{"gh", "pr", "create", "--repo", "acme/app", "--base", "main", "--head", created.Branch, "--title", "feat: add flow.txt", "--body", "fallback body"}
			reads := 0
			ctx := createFlowReader(t, wt, "acme/app", created.Branch, "", &reads)
			result, err := createPullRequest(ctx, PullRequestCreateOptions{Worktree: wt, ProjectsRoot: fixture.githubDir, Body: "fallback body", run: r, Timeout: 10 * time.Second})
			restore()
			after, readErr := os.ReadFile(manifest)
			if readErr != nil || !bytes.Equal(before, after) || heldOnce != 1 {
				t.Fatalf("manifest restoration=%v count=%d", readErr, heldOnce)
			}
			if hosted {
				if err != nil || result.Outcome != CreateSuccess || result.Repository != "acme/app" || result.BaseRef != "main" || reads != 1 || r.publications != 1 {
					t.Fatalf("native hosted fallback result=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.HasPrefix(err.Error(), "resolve repository for "+wt+":") || reads != 0 || r.publications != 0 {
				t.Fatalf("native legacy fallback result=%+v err=%v", result, err)
			}
			guard, guardErr := worktrees.Guard(context.Background(), wt, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir})
			post, postErr := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: wt})
			if guardErr != nil || guard.Path != wt || postErr != nil || post.Claim == nil || post.Claim.ClaimID != view.Claim.ClaimID {
				t.Fatalf("restored actual custody guard=%+v err=%v view=%+v err=%v", guard, guardErr, post, postErr)
			}
		})
	}
}

//nolint:paralleltest // Missing-task native inventory reads HOME/XDG; this TOP owns those process settings via t.Setenv.
func TestE2EPRCreateFlowNativeEarlyResolutionRefusals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	fixture := newExplicitRootEngineFixture(t)
	for _, argument := range []string{"missing-pr-flow-task", t.TempDir(), " "} {
		//nolint:paralleltest // Parent holds private HOME/XDG throughout each native inventory/Guard observation.
		t.Run(argument, func(t *testing.T) {
			r := &createFlowRunner{Runner: defaultRunner, t: t}
			result, err := createPullRequest(context.Background(), PullRequestCreateOptions{ProjectsRoot: fixture.githubDir, Worktree: argument, run: r})
			if err == nil || len(r.calls) != 0 || result.URL != "" {
				t.Fatalf("argument=%q result=%+v err=%v runnercalls=%q", argument, result, err, r.calls)
			}
		})
	}
}
