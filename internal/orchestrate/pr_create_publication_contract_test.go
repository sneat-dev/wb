package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestPRCreatePublicationAdoptionContracts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("publication refused")
	for _, tc := range []struct {
		name, list, output string
		listErr, runErr    error
		draft, adopt       bool
		issues             []int
		edit, errText      string
	}{
		{name: "adopt absent body", list: "https://example/pull/41\tmain", adopt: true},
		{name: "adopt existing closes", list: "https://example/pull/41\tmain\tCloses #7", adopt: true, issues: []int{7}},
		{name: "edit missing closes", list: "https://example/pull/41\tmain\tbody", adopt: true, issues: []int{7}, edit: "Closes #7\n\nbody"},
		{name: "edit refusal", list: "https://example/pull/41\tmain\tbody", issues: []int{7}, edit: "Closes #7\n\nbody", runErr: sentinel},
		{name: "base mismatch", list: "https://example/pull/41\tdevelop\tbody", errText: "pull request https://example/pull/41 is already open against develop, not main"},
		{name: "list refusal falls back", listErr: sentinel, output: "notice\nhttps://example/pull/41\n\n"},
		{name: "blank falls back", list: " \n", output: "https://example/pull/41"},
		{name: "malformed falls back", list: "missing tabs", output: "https://example/pull/41"},
		{name: "create refusal", runErr: sentinel},
		{name: "create missing URL", output: " \n", errText: "gh pr create returned no pull request URL"},
		{name: "draft missing manifest", draft: true, output: "https://example/pull/41"},
		{name: "draft refusal", draft: true, runErr: sentinel},
		{name: "draft missing URL", draft: true, output: "\t\n", errText: "gh pr create --draft returned no pull request URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fake := runnertest.New(t)
			reads := 0
			argv := []string{"pr", "list", "--repo", "acme/app", "--head", "feature", "--state", "open", "--json", "url,baseRefName,body", "--jq", `.[0] | (.url + "\t" + .baseRefName + "\t" + (.body // ""))`}
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{
				Read: func(_ context.Context, gotDir string, args ...string) ([]byte, error) {
					reads++
					if gotDir != dir || !reflect.DeepEqual(args, argv) {
						t.Fatalf("list dir=%q argv=%q", gotDir, args)
					}
					return []byte(tc.list), tc.listErr
				},
				Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
					t.Fatal("unexpected Get")
					return githubobserver.Response{}, sentinel
				},
				Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
					t.Fatal("unexpected Execute")
					return githubobserver.CommandResponse{}
				},
			})
			mutation := []string(nil)
			if tc.edit != "" {
				mutation = []string{"gh", "pr", "edit", "https://example/pull/41", "--repo", "acme/app", "--body", tc.edit}
			} else if !tc.adopt && tc.errText != "pull request https://example/pull/41 is already open against develop, not main" {
				mutation = []string{"gh", "pr", "create", "--repo", "acme/app", "--base", "main", "--head", "feature", "--title", "title", "--body", "body"}
				if tc.draft {
					mutation = append(mutation, "--draft")
				}
			}
			if mutation != nil {
				fake.Expect(func(c runnertest.Call) bool {
					return c.Op == "RunOpts" && c.Dir == dir && c.Opts.CaptureCombined && reflect.DeepEqual(c.Argv(), mutation)
				}, runner.Result{CombinedOutput: tc.output}, tc.runErr)
			}
			url, adopted, err := openOrAdoptPullRequest(ctx, dir, "acme/app", "feature", "main", "title", "body", tc.draft, Options{run: fake, Timeout: time.Second}, tc.issues)
			if reads != 1 {
				t.Fatalf("reads=%d", reads)
			}
			if tc.runErr != nil {
				if !errors.Is(err, tc.runErr) || url != "" || adopted {
					t.Fatalf("url=%q adopted=%v err=%v", url, adopted, err)
				}
			} else if tc.errText != "" {
				if err == nil || err.Error() != tc.errText || url != "" || adopted {
					t.Fatalf("url=%q adopted=%v err=%v", url, adopted, err)
				}
				if strings.HasPrefix(tc.errText, "pull request") {
					var mismatch *pullRequestBaseMismatchError
					if !errors.As(err, &mismatch) {
						t.Fatalf("type=%T", err)
					}
				}
			} else if err != nil || url != "https://example/pull/41" || adopted != tc.adopt {
				t.Fatalf("url=%q adopted=%v err=%v", url, adopted, err)
			}
			wantCalls := 0
			if mutation != nil {
				wantCalls = 1
			}
			if fake.CallCount() != wantCalls {
				t.Fatalf("calls=%d want=%d", fake.CallCount(), wantCalls)
			}
		})
	}
}

func TestPRCreateTitleContracts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		subjects []string
		want     string
	}{
		{"empty", nil, "Open pull request"}, {"blank", []string{" ", "\t"}, "Open pull request"}, {"single", []string{" change "}, "change"}, {"single WIP", []string{" WIP: partial "}, "apply 1 commit"}, {"all WIP", []string{"wip", "WIP: second"}, "apply 2 commits"}, {"one usable", []string{"WIP", "useful"}, "useful"}, {"singular", []string{"newer", "older"}, "older and 1 related change"}, {"plural", []string{"newest", "middle", "oldest"}, "oldest and 2 related changes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := pullRequestCreateTitle(tc.subjects); got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestPRCreateBodyContracts(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"literal", "file", "missing", "directory", "commit", "blank commit", "commit refusal", "multiple", "empty"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fake := runnertest.New(t)
			opts := PullRequestCreateOptions{run: fake, Timeout: time.Second, Closes: []int{7}}
			subjects := []string{"subject"}
			want := ""
			wantError := false
			switch name {
			case "literal":
				opts.Body = " literal \n"
				opts.BodyFile = filepath.Join(dir, "absent")
				want = opts.Body
			case "file":
				opts.BodyFile = filepath.Join(dir, "body")
				want = " file \n"
				if err := os.WriteFile(opts.BodyFile, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				opts.BodyFile = filepath.Join(dir, "absent")
				wantError = true
			case "directory":
				opts.BodyFile = dir
				wantError = true
			case "commit", "blank commit", "commit refusal":
				output := "  meaningful \n"
				var refusal error
				if name == "blank commit" {
					output = " \n"
				}
				if name == "commit refusal" {
					refusal = errors.New("log refused")
				}
				fake.Expect(func(c runnertest.Call) bool {
					return c.Op == "RunOpts" && c.Dir == dir && c.Opts.CaptureCombined && reflect.DeepEqual(c.Argv(), []string{"git", "log", "-1", "--format=%b"})
				}, runner.Result{CombinedOutput: output}, refusal)
				if name == "commit" {
					want = "meaningful"
				}
			case "multiple":
				subjects = []string{"newest", "oldest"}
				want = "Mechanically prepared by `wb pr create` from the branch's own commits.\n\nCommits:\n\n- oldest\n- newest\n"
			case "empty":
				subjects = nil
				want = "Mechanically prepared by `wb pr create` from the branch's own commits.\n\nCommits:\n\n"
			}
			got, err := pullRequestCreateBody(opts, dir, subjects)
			if wantError {
				if err == nil || !strings.HasPrefix(err.Error(), "read --body-file "+opts.BodyFile+":") || got != "" {
					t.Fatalf("body=%q err=%v", got, err)
				}
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatalf("cause=%T", err)
				}
			} else if err != nil || got != "Closes #7\n\n"+want {
				t.Fatalf("body=%q want=%q err=%v", got, "Closes #7\n\n"+want, err)
			}
		})
	}
}

func TestPRCreatePinObservationContracts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("observation refused")
	for _, name := range []string{"empty pushed head", "already current", "first read refusal", "latest prior view"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var initial githubchecks.PullRequestView
			initial.Number = 41
			initial.Head.SHA = "initial"
			head := "pushed"
			if name == "empty pushed head" {
				head = ""
			}
			if name == "already current" {
				head = "initial"
			}
			reads, sleeps := 0, 0
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
				reads++
				if req.Dir != "" || req.Repository != "acme/app" || req.Endpoint != "repos/acme/app/pulls/41" || req.FreshWindow != 0 {
					t.Fatalf("request=%+v", req)
				}
				if name == "latest prior view" && reads == 1 {
					return githubobserver.Response{Body: []byte(`{"number":41,"head":{"sha":"latest"}}`)}, nil
				}
				return githubobserver.Response{}, sentinel
			}, Read: func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("unexpected Read")
				return nil, sentinel
			}, Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
				t.Fatal("unexpected Execute")
				return githubobserver.CommandResponse{}
			}})
			got, err := pinPullRequestViewToHead(ctx, "acme/app", "41", head, initial, func(delay time.Duration) {
				sleeps++
				if delay != 200*time.Millisecond {
					t.Fatalf("delay=%v", delay)
				}
			})
			wantSHA := "initial"
			wantReads := 0
			if name == "first read refusal" {
				wantReads = 1
			}
			if name == "latest prior view" {
				wantReads = 2
				wantSHA = "latest"
			}
			if got.Head.SHA != wantSHA || reads != wantReads || sleeps != wantReads {
				t.Fatalf("sha=%s reads=%d sleeps=%d", got.Head.SHA, reads, sleeps)
			}
			if wantReads == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "re-read pull request acme/app#41 to confirm its pushed head:") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
