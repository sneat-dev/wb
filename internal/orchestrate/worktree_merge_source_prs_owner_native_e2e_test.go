//go:build e2e

package orchestrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2ESourcePROwnerNativeHeadDiscoveryAndExactNegativeObservations(t *testing.T) {
	t.Parallel()
	f, r, source := sourcePROwnerNativeHeadsFixture(t)
	heads, err := absorbedSourceHeads(t.Context(), f.canonical, r, 0, 0)
	if err != nil || !reflect.DeepEqual(heads, []string{source}) {
		t.Fatalf("actual absorbed source=%v %v", heads, err)
	}
	for _, mode := range []string{"rev-list error", "ancestry error includes head", "missing target", "missing candidate"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			input := r
			sentinel := errors.New("selected source head observation")
			args := []string{"rev-list", "--merges", "--parents", r.TargetSHA + ".." + r.Candidate.SHA}
			if mode == "ancestry error includes head" {
				args = []string{"merge-base", source, r.TargetSHA}
			}
			run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: args, ordinal: 1, sentinel: sentinel}
			switch mode {
			case "missing target":
				input.TargetSHA = ""
			case "missing candidate":
				input.Candidate.SHA = ""
			}
			got, e := absorbedSourceHeadsWithRunner(t.Context(), run, f.canonical, input, 0, 0)
			switch mode {
			case "rev-list error":
				if !errors.Is(e, sentinel) || run.seen != 1 || got != nil {
					t.Fatalf("rev-list=%v %v seen=%d", got, e, run.seen)
				}
			case "ancestry error includes head":
				if e != nil || run.seen != 1 || !reflect.DeepEqual(got, []string{source}) {
					t.Fatalf("best effort native head=%v %v seen=%d", got, e, run.seen)
				}
			default:
				if e == nil || !strings.Contains(e.Error(), "lacks target or candidate identity") || run.seen != 0 {
					t.Fatalf("missing identity=%v seen=%d", e, run.seen)
				}
			}
		})
	}
}

func TestE2ESourcePROwnerNativeOuterNoopsAndDiscoveryRefusals(t *testing.T) {
	t.Parallel()
	for _, r := range []*WorktreeMergeReceipt{nil, {}, {Repository: "acme/app"}, {LandingSHA: "landing"}} {
		if err := reconcileAbsorbedSourcePullRequests(t.Context(), t.TempDir(), r, 0, 0, nil); err != nil {
			t.Fatalf("no-op=%v", err)
		}
	}
	bad := WorktreeMergeReceipt{Repository: "invalid", LandingSHA: "landing"}
	if err := reconcileAbsorbedSourcePullRequests(t.Context(), t.TempDir(), &bad, 0, 0, nil); err == nil || !strings.Contains(err.Error(), "resolve canonical clone for absorbed source heads") {
		t.Fatalf("canonical refusal=%v", err)
	}
	f := newExplicitRootEngineFixture(t)
	target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	r := WorktreeMergeReceipt{Repository: "acme/app", TargetSHA: target, LandingSHA: target, Candidate: WorktreeMergeCandidate{SHA: "not-a-native-revision"}}
	if err := reconcileAbsorbedSourcePullRequests(t.Context(), f.githubDir, &r, 0, 0, nil); err == nil || !strings.Contains(err.Error(), "discover absorbed source heads") {
		t.Fatalf("actual revision refusal=%v", err)
	}
	r.Candidate.SHA = target
	if err := reconcileAbsorbedSourcePullRequests(t.Context(), f.githubDir, &r, 0, 0, nil); err != nil {
		t.Fatalf("actual native no-head completion=%v", err)
	}
}

//nolint:paralleltest // Native gh transport bindings use a private scripted provider installed through process PATH and XDG_STATE_HOME; no live hosted mutation.
func TestE2ESourcePROwnerNativeHostedDefaultBindings(t *testing.T) {
	f, r, source := sourcePROwnerNativeHeadsFixture(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$WB_TEST_SOURCE_PR_CALLS"
case "$*" in
 'api repos/acme/app/commits/'*'/pulls?per_page=100 --include') printf 'HTTP/1.1 200 OK\n\n[]\n' ;;
 'api repos/acme/app/issues/7/comments?per_page=100 --include') printf 'HTTP/1.1 200 OK\n\n[]\n' ;;
 'api --method POST repos/acme/app/issues/7/comments -f body=transport contract'|'api --method PATCH repos/acme/app/pulls/7 -f state=closed') printf '{}\n' ;;
 *) printf 'unexpected private gh command: %s\n' "$*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_SOURCE_PR_CALLS", calls)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	remote := githubSourcePullRequestRemote{}
	views, err := remote.associated(t.Context(), "acme/app", source)
	if err != nil || len(views) != 0 {
		t.Fatalf("native transport association=%v %v", views, err)
	}
	found, err := remote.hasComment(t.Context(), "acme/app", 7, "marker")
	if err != nil || found {
		t.Fatalf("native transport comments=%t %v", found, err)
	}
	if err := remote.comment(t.Context(), "acme/app", 7, "transport contract"); err != nil {
		t.Fatal(err)
	}
	if err := remote.close(t.Context(), "acme/app", 7); err != nil {
		t.Fatal(err)
	}
	if err := reconcileAbsorbedSourcePullRequests(t.Context(), f.githubDir, &r, 0, 0, nil); err != nil {
		t.Fatalf("actual native owner/default provider=%v", err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repos/acme/app/commits/" + source + "/pulls?per_page=100", "repos/acme/app/issues/7/comments?per_page=100", "api --method POST repos/acme/app/issues/7/comments -f body=transport contract", "api --method PATCH repos/acme/app/pulls/7 -f state=closed"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("default binding lost %q: %s", want, raw)
		}
	}
	// The scripted provider observes empty association/comment sets and records
	// exact transport writes. It supplies no positive hosted custody/DAG proof.
}
