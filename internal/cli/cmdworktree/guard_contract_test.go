package cmdworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestGuardAdapterPreservesLazyNativeOptionsAndDefaultPath(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "explicit"}[explicit], func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before", Quiet: true}
			ctx := context.WithValue(context.Background(), createContextKey{}, "guard")
			result := worktrees.GuardResult{Path: "checkout", Kind: "linked", Branch: "topic"}
			var called int
			c := NewGuard(shared.Runtime{Flags: func() shared.Flags { return flags }}, func(got context.Context, path string, o worktrees.GuardOptions) (worktrees.GuardResult, error) {
				called++
				want := worktrees.GuardOptions{ProjectsRoot: "after", CheckFreshness: true, Admission: worktrees.AdmissionMode("off")}
				wantPath := "."
				if explicit {
					want.Base = "base"
					want.Admission = worktrees.AdmissionMode("enforce")
					want.CheckPublication = true
					wantPath = "chosen"
				}
				if got != ctx || path != wantPath || !reflect.DeepEqual(o, want) {
					t.Fatalf("guard=%v/%s/%+v", got, path, o)
				}
				return result, nil
			}, func(any) bool { t.Fatal("terminal check without prepush flag"); return false })
			c.SetContext(ctx)
			flags.ProjectsRoot = "after"
			args := []string{}
			if explicit {
				args = []string{"chosen", "--base", "base", "--admission", "enforce", "--published", "--format", "json"}
			}
			var out bytes.Buffer
			if err := createExecute(c, strings.NewReader(""), &out, io.Discard, args...); err != nil || called != 1 {
				t.Fatalf("err=%v called=%d", err, called)
			}
			if explicit {
				var got worktrees.GuardResult
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, result) {
					t.Fatalf("JSON=%s/%v", out.String(), err)
				}
			} else if out.String() != "ok: linked checkout checkout on topic\n" {
				t.Fatalf("local quiet must not inherit root quiet: %q", out.String())
			}
		})
	}
}
func TestGuardAdapterValidatesBeforeReadingAndOnlyDeletionPushBypassesAuthority(t *testing.T) {
	t.Parallel()
	boom := errors.New("inspection failed")
	zero, sha := strings.Repeat("0", 40), strings.Repeat("a", 40)
	for _, tc := range []struct {
		name     string
		args     []string
		input    io.Reader
		terminal bool
		want     string
		calls    int
	}{
		{"too many", []string{"a", "b"}, strings.NewReader(""), false, "accepts at most 1 arg", 0},
		{"format", []string{"--format", "yaml", "--pre-push-stdin"}, strings.NewReader(""), false, "unsupported format", 0},
		{"admission", []string{"--admission", "invalid", "--pre-push-stdin"}, strings.NewReader(""), false, "unsupported admission mode \"invalid\"; use off, warn, or enforce", 0},
		{"delete only", []string{"--pre-push-stdin"}, strings.NewReader("(delete) " + zero + " refs/heads/x " + sha + "\n"), false, "", 0},
		{"terminal", []string{"--pre-push-stdin"}, strings.NewReader("(delete) " + zero + " refs/heads/x " + sha + "\n"), true, boom.Error(), 1},
		{"update", []string{"--pre-push-stdin"}, strings.NewReader("refs/heads/x " + sha + " refs/heads/x " + zero + "\n"), false, boom.Error(), 1},
		{"read error", []string{"--pre-push-stdin"}, createRejectedIO{boom}, false, boom.Error(), 1},
		{"malformed", []string{"--pre-push-stdin"}, strings.NewReader("malformed"), false, boom.Error(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls, terminalCalls int
			c := NewGuard(createRuntime(), func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error) {
				calls++
				return worktrees.GuardResult{}, boom
			}, func(any) bool { terminalCalls++; return tc.terminal })
			var out bytes.Buffer
			err := createExecute(c, tc.input, &out, io.Discard, tc.args...)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) || calls != tc.calls || out.Len() != 0 {
				t.Fatalf("err=%v calls=%d out=%q", err, calls, out.String())
			}
			if tc.calls == 1 && !errors.Is(err, boom) {
				t.Fatalf("operation identity=%v", err)
			}
			if tc.name == "format" || tc.name == "admission" || tc.name == "too many" {
				if terminalCalls != 0 {
					t.Fatal("input consulted before preflight")
				}
			}
			if tc.terminal {
				if tc.input.(*strings.Reader).Len() == 0 {
					t.Fatal("terminal read")
				}
			}
		})
	}
}
func TestGuardAdapterRendersTransientExternalFreshnessAndVerifiedPublication(t *testing.T) {
	t.Parallel()
	result := worktrees.GuardResult{Path: "checkout", Kind: "linked", Branch: "topic", Transient: true, TransientOperation: "rebase", External: true, Freshness: &worktrees.CanonicalFreshness{Status: worktrees.CanonicalFreshnessCurrent, RemoteRef: "origin/main", RemoteSHA: "base"}, Publication: &worktrees.CanonicalFreshness{Status: worktrees.PublicationPublished, RemoteRef: "origin/topic", RemoteSHA: "head"}}
	c := NewGuard(createRuntime(), func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error) {
		return result, nil
	}, func(any) bool { return false })
	var out, stderr bytes.Buffer
	if err := createExecute(c, strings.NewReader(""), &out, &stderr, "--published"); err != nil || out.String() != "ok: linked (adopted) checkout checkout on detached HEAD (active rebase) (fresh against origin/main at base) (published at origin/topic head)\n" || stderr.Len() != 0 {
		t.Fatalf("out=%q stderr=%q err=%v", out.String(), stderr.String(), err)
	}
}
func TestGuardAdapterWarningsIgnoreStderrFailureAndFindingsFollowSuccessfulOutput(t *testing.T) {
	t.Parallel()
	boom := errors.New("rejected writer")
	finding := errors.New("coded finding")
	for _, tc := range []struct {
		name, format            string
		quiet, failOut, failErr bool
	}{
		{"text", "text", false, false, false}, {"JSON", "json", false, false, false}, {"quiet", "text", true, false, false},
		{"stderr ignored", "text", false, false, true}, {"text writer wins", "text", false, true, false}, {"JSON writer wins", "json", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := worktrees.GuardResult{Path: "checkout", Kind: "linked", Branch: "topic", Admission: &worktrees.Admission{Reason: "missing instruction", Remedy: "record prompt"}, Freshness: &worktrees.CanonicalFreshness{Status: worktrees.CanonicalFreshnessOffline, RemoteRef: "origin/main", Error: "offline"}, Publication: &worktrees.CanonicalFreshness{Status: worktrees.PublicationUnpublished, LocalSHA: "head", RemoteRef: "origin/topic", Ahead: 1}}
			var out, stderr bytes.Buffer
			var outWriter, errWriter io.Writer = &out, &stderr
			if tc.failOut {
				outWriter = createRejectedIO{boom}
			}
			if tc.failErr {
				errWriter = createRejectedIO{boom}
			}
			coded := false
			environment := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }, ExitError: func(code int, message string) error {
				coded = true
				if code != shared.ExitFindings || message != "HEAD is not verified as published on origin; see the finding above" {
					t.Fatalf("coded=%d/%s", code, message)
				}
				if !tc.quiet && out.Len() == 0 {
					t.Fatal("finding before output")
				}
				return finding
			}}
			c := NewGuard(environment, func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error) {
				return result, nil
			}, func(any) bool { return false })
			args := []string{"--published", "--format", tc.format}
			if tc.quiet {
				args = append(args, "--quiet")
			}
			err := createExecute(c, strings.NewReader(""), outWriter, errWriter, args...)
			if tc.failOut {
				if !errors.Is(err, boom) || coded {
					t.Fatalf("writer precedence=%v coded=%t", err, coded)
				}
			} else if !errors.Is(err, finding) || !coded {
				t.Fatalf("finding=%v coded=%t", err, coded)
			}
			if tc.quiet && out.Len() != 0 {
				t.Fatal("quiet stdout")
			}
			if !tc.failErr {
				for _, want := range []string{"warning: missing instruction\n  record prompt", "warning: canonical freshness for checkout: status=offline target=origin/main: offline", "unpublished:", "git push origin HEAD:topic"} {
					if !strings.Contains(stderr.String(), want) {
						t.Fatalf("missing %q in %q", want, stderr.String())
					}
				}
			}
		})
	}
}
func TestGuardAdapterQuietSuccessfulInspectionWritesNothing(t *testing.T) {
	t.Parallel()
	c := NewGuard(createRuntime(), func(context.Context, string, worktrees.GuardOptions) (worktrees.GuardResult, error) {
		return worktrees.GuardResult{}, nil
	}, func(any) bool { return false })
	var out, stderr bytes.Buffer
	if err := createExecute(c, strings.NewReader(""), &out, &stderr, "--quiet"); err != nil || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("quiet=%q/%q/%v", out.String(), stderr.String(), err)
	}
}
