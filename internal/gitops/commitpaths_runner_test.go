package gitops

import (
	"errors"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"reflect"
	"strings"
	"testing"
)

func TestParsedGitOutputKeepsWarningsSeparateAndPreservesEnvironment(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "warning", true: "failure"}[failed], func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			failure := errors.New("git refused")
			var commandErr error
			if failed {
				commandErr = failure
			}
			extra := []string{"GIT_INDEX_FILE=/private/test-index"}
			fake.Expect(func(call runnertest.Call) bool {
				if call.Dir != "/repo" || call.Name != "git" || !reflect.DeepEqual(call.Args, []string{"status", "--porcelain"}) {
					return false
				}
				if !reflect.DeepEqual(call.Opts.Env, append(console.Env(), extra...)) {
					t.Fatal("child environment changed")
				}
				if call.Opts.CaptureCombined {
					t.Fatal("parsed output must keep stderr separate")
				}
				return true
			}, runner.Result{Stdout: "data\n", Stderr: "warning\n"}, commandErr)
			output, err := runStdoutWithRunner(fake, "/repo", extra, "git", "status", "--porcelain")
			if output != "data\n" {
				t.Fatalf("parsed stdout=%q", output)
			}
			if failed {
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), "warning") {
					t.Fatalf("lost error diagnostic: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
