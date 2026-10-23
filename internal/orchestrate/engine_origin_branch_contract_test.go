package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

type originBranchContextKey struct{}

// originBranchObservedRunner observes only command context routing; its
// embedded exact-script Fake supplies controlled outputs, not native Git authority.
type originBranchObservedRunner struct {
	runner.Runner
	t      *testing.T
	marker *int
}

func (r originBranchObservedRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.t.Helper()
	if _, bounded := ctx.Deadline(); !bounded || ctx.Value(originBranchContextKey{}) != r.marker {
		r.t.Fatal("command lost the per-call timeout or caller context")
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestOriginHeadSymrefOutputAndFailureContracts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("controlled symbolic-ref failure")
	for _, row := range []struct {
		name, output, want, errorText string
		commandErr                    error
	}{
		{name: "trimmed expected prefix", output: " \trefs/remotes/origin/trunk\n", want: "trunk"},
		{name: "nested branch", output: "refs/remotes/origin/release/next", want: "release/next"},
		{name: "empty suffix", output: "refs/remotes/origin/", want: ""},
		{name: "unexpected local ref", output: "refs/heads/main\n", errorText: `unexpected origin/HEAD symref "refs/heads/main"`},
		{name: "empty output", errorText: `unexpected origin/HEAD symref ""`},
		{name: "command refusal", commandErr: sentinel, errorText: "controlled symbolic-ref failure"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			fake.ExpectArgv([]string{"git", "symbolic-ref", "refs/remotes/origin/HEAD"}, runner.Result{Stdout: row.output}, row.commandErr)
			marker := new(int)
			ctx := context.WithValue(t.Context(), originBranchContextKey{}, marker)
			run := originBranchObservedRunner{Runner: fake, t: t, marker: marker}
			got, err := readOriginHeadSymref(ctx, "private-canonical", Options{run: run, Timeout: time.Minute})
			if row.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), row.errorText) || got != "" {
					t.Fatalf("got %q error %v", got, err)
				}
			} else if err != nil || got != row.want {
				t.Fatalf("got %q error %v want %q", got, err, row.want)
			}
			if row.commandErr != nil && !errors.Is(err, sentinel) {
				t.Fatalf("lost command error identity: %v", err)
			}
			calls := fake.Calls()
			if len(calls) != 1 || calls[0].Op != "RunOpts" || calls[0].Dir != "private-canonical" || !calls[0].Opts.CaptureCombined || len(calls[0].Opts.Env) == 0 {
				t.Fatalf("unexpected command capture: %+v", calls)
			}
		})
	}
}

func TestOriginDefaultBranchResolutionCommandOrder(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("controlled origin observation refusal")
	symbol := []string{"git", "symbolic-ref", "refs/remotes/origin/HEAD"}
	refresh := []string{"git", "remote", "set-head", "origin", "--auto"}
	wire := []string{"git", "ls-remote", "--symref", "origin", "HEAD"}
	type step struct {
		argv   []string
		output string
		err    error
	}
	for _, row := range []struct {
		name            string
		steps           []step
		retry           int
		want, errorText string
		errorIdentity   bool
	}{
		{name: "cached", steps: []step{{symbol, "refs/remotes/origin/main", nil}}, want: "main"},
		{name: "refresh missing cache", steps: []step{{symbol, "", sentinel}, {refresh, "", nil}, {symbol, "refs/remotes/origin/trunk", nil}}, want: "trunk"},
		{name: "refresh empty suffix", steps: []step{{symbol, "refs/remotes/origin/", nil}, {refresh, "", nil}, {symbol, "refs/remotes/origin/trunk", nil}}, want: "trunk"},
		{name: "second read fails", steps: []step{{symbol, "refs/heads/wrong", nil}, {refresh, "", nil}, {symbol, "", sentinel}, {wire, "ref: refs/heads/next\tHEAD\n", nil}}, want: "next"},
		{name: "second read empty suffix", steps: []step{{symbol, "", sentinel}, {refresh, "", nil}, {symbol, "refs/remotes/origin/", nil}, {wire, "ref: refs/heads/next\tHEAD\n", nil}}, want: "next"},
		{name: "refresh refuses wire succeeds", steps: []step{{symbol, "", sentinel}, {refresh, "", sentinel}, {wire, "ref: refs/heads/release/next\tHEAD\n", nil}}, want: "release/next"},
		{name: "wire command failure", steps: []step{{symbol, "", sentinel}, {refresh, "", sentinel}, {wire, "", sentinel}}, errorText: "git ls-remote --symref origin HEAD:", errorIdentity: true},
		{name: "wire parse failure", steps: []step{{symbol, "", sentinel}, {refresh, "", sentinel}, {wire, "not a symref", nil}}, errorText: "origin HEAD symref not found in ls-remote output"},
		{name: "wire empty branch preserved", steps: []step{{symbol, "", sentinel}, {refresh, "", sentinel}, {wire, "ref: refs/heads/\tHEAD", nil}}, want: ""},
		{name: "retry passed through", retry: 1, steps: []step{{symbol, "", sentinel}, {symbol, "refs/remotes/origin/retried", nil}}, want: "retried"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			for _, observation := range row.steps {
				fake.ExpectArgv(observation.argv, runner.Result{Stdout: observation.output}, observation.err)
			}
			marker := new(int)
			ctx := context.WithValue(t.Context(), originBranchContextKey{}, marker)
			got, err := resolveOriginDefaultBranch(ctx, "private-canonical", Options{run: originBranchObservedRunner{Runner: fake, t: t, marker: marker}, Timeout: time.Minute, Retry: row.retry})
			if row.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), row.errorText) || got != "" {
					t.Fatalf("got %q error %v", got, err)
				}
			} else if err != nil || got != row.want {
				t.Fatalf("got %q error %v want %q", got, err, row.want)
			}
			if row.errorIdentity && !errors.Is(err, sentinel) {
				t.Fatalf("lost terminal command identity: %v", err)
			}
			calls := fake.Calls()
			if len(calls) != len(row.steps) {
				t.Fatalf("calls %d want %d", len(calls), len(row.steps))
			}
			for index, c := range calls {
				if c.Op != "RunOpts" || c.Dir != "private-canonical" || !c.Opts.CaptureCombined || len(c.Opts.Env) == 0 || !reflect.DeepEqual(c.Argv(), row.steps[index].argv) {
					t.Fatalf("command %d: %+v want %q", index, c, row.steps[index].argv)
				}
			}
		})
	}
}

func TestOriginRemoteSymrefParsingPreservesExistingPolicy(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name, output, want string
		wantError          bool
	}{
		{name: "empty", wantError: true},
		{name: "only object", output: "abc\tHEAD\n", wantError: true},
		{name: "empty record", output: "ref:   \n", wantError: true},
		{name: "wrong namespace", output: "ref: refs/tags/v1\tHEAD\n", wantError: true},
		{name: "later branch", output: "abc\tHEAD\nref: refs/tags/v1\tHEAD\nref: refs/heads/main\tHEAD\n", want: "main"},
		{name: "unicode whitespace", output: "\u2003ref: refs/heads/trunk\u2003HEAD\u2003\n", want: "trunk"},
		{name: "extra fields accepted", output: "ref: refs/heads/next OTHER extra\n", want: "next"},
		{name: "head marker optional", output: "ref: refs/heads/main\n", want: "main"},
		{name: "empty suffix accepted", output: "ref: refs/heads/\tHEAD\n", want: ""},
		{name: "first accepted record wins", output: "ref: refs/heads/one\tHEAD\nref: refs/heads/two\tHEAD\n", want: "one"},
		{name: "invalid utf8 not whitespace", output: "ref: \xff\n", wantError: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseLsRemoteSymref(row.output)
			if row.wantError {
				if err == nil || err.Error() != "origin HEAD symref not found in ls-remote output" || got != "" {
					t.Fatalf("got %q error %v", got, err)
				}
			} else if err != nil || got != row.want {
				t.Fatalf("got %q error %v want %q", got, err, row.want)
			}
		})
	}
}
