//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

// absorbedSourceProofRunner delegates all successes to native Git; only exact
// negative query argv are controlled. Resolver and content reads bypass it.
type absorbedSourceProofRunner struct {
	runner.Runner
	t     *testing.T
	repo  string
	fail  []string
	cause error
	calls [][]string
}

func (r *absorbedSourceProofRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	argv := append([]string{name}, args...)
	r.calls = append(r.calls, argv)
	if dir != r.repo || name != "git" {
		r.t.Fatalf("query cwd=%q argv=%q", dir, argv)
	}
	if _, ok := ctx.Deadline(); ok {
		r.t.Fatal("query acquired owner deadline")
	}
	if !opts.CaptureCombined {
		r.t.Fatal("query lost combined capture")
	}
	if reflect.DeepEqual(argv, r.fail) {
		return runner.Result{ExitCode: 1}, r.cause
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestE2EAbsorbedConflictSourceProofUsesOneNativeMergeBase(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	repo := f.canonical
	commit := func(base string, files map[string]string, message string) string {
		t.Helper()
		if base != "" {
			runEngineGit(t, repo, "checkout", "--detach", base)
		}
		for path, body := range files {
			full := filepath.Join(repo, path)
			if err := os.MkdirAll(filepath.Dir(full), 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		runEngineGit(t, repo, "add", "-A")
		runEngineGit(t, repo, "commit", "--allow-empty", "-m", message)
		return strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	}
	base := commit("", map[string]string{"plain.txt": "base\n", "events.jsonl": "old\n", "spec/plans/README.md": "old index\n"}, "source proof base")
	source := commit(base, map[string]string{"plain.txt": "absorbed\n", "events.jsonl": "old\nnew\n", "spec/plans/README.md": "source index\n"}, "source proof source")
	target := commit(base, map[string]string{"plain.txt": "absorbed\n", "events.jsonl": "new\nold\n", "spec/plans/README.md": "regenerated index\n"}, "source proof target")
	diverged := commit(target, map[string]string{"plain.txt": "different\n"}, "diverged blob")
	missingLine := commit(target, map[string]string{"events.jsonl": "old\n"}, "missing source line")
	emptySource := commit(base, nil, "empty source")
	emptyTarget := commit(base, nil, "empty target")
	const mod = "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.0.0\n"
	const sum = "example.test/a v1.0.0 h1:old\n"
	goSource := commit(base, map[string]string{"go.mod": mod, "go.sum": sum}, "Go source")
	goIdentical := commit(base, map[string]string{"go.mod": mod, "go.sum": sum}, "Go independently identical target")
	goUpgrade := commit(base, map[string]string{"go.mod": strings.Replace(mod, "v1.0.0", "v1.1.0", 1), "go.sum": "example.test/a v1.1.0 h1:new\n"}, "Go upgraded target")
	goDowngrade := commit(base, map[string]string{"go.mod": strings.Replace(mod, "v1.0.0", "v0.9.0", 1), "go.sum": "example.test/a v0.9.0 h1:older\n"}, "Go downgraded target")
	frozen := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD"))
	t.Cleanup(func() {
		if got := strings.TrimSpace(runEngineGit(t, repo, "rev-parse", "HEAD")); got != frozen {
			t.Errorf("HEAD=%s want=%s", got, frozen)
		}
		if got := runEngineGit(t, repo, "status", "--porcelain"); got != "" {
			t.Errorf("corpus mutated: %s", got)
		}
	})
	derived := map[string]bool{"spec/plans/README.md": true}
	mixed := absorbedConflictProof{method: "content_absorbed", mergeBaseSHA: base, pathCount: 3, pathProofs: []WorktreeMergeAbsorbedConflictPathProof{{Path: "events.jsonl", Method: "lines_absorbed", AddedLines: 1, MatchedLines: 1}, {Path: "plain.txt", Method: "blob_absorbed"}, {Path: "spec/plans/README.md", Method: "derived_excused"}}}
	goProof := func(method string) absorbedConflictProof {
		return absorbedConflictProof{method: "content_absorbed", mergeBaseSHA: base, pathCount: 2, pathProofs: []WorktreeMergeAbsorbedConflictPathProof{{Path: "go.mod", Method: method}, {Path: "go.sum", Method: method}}}
	}
	cases := []struct {
		name, source, target string
		derived              map[string]bool
		want                 absorbedConflictProof
		diagnostic           string
	}{
		{name: "ancestor", source: base, target: source, want: absorbedConflictProof{method: "ancestor"}},
		{name: "ordered mixed content", source: source, target: target, derived: derived, want: mixed},
		{name: "ordinary blob divergence", source: source, target: diverged, derived: derived, diagnostic: "path \"plain.txt\" is not content-absorbed"},
		{name: "JSONL missing line", source: source, target: missingLine, derived: derived, diagnostic: "not lines-absorbed"},
		{name: "empty independent source", source: emptySource, target: emptyTarget, diagnostic: "source changed no path relative to its merge base " + base},
		{name: "Go equal blobs", source: goSource, target: goIdentical, want: goProof("blob_absorbed")},
		{name: "Go strict upgrade", source: goSource, target: goUpgrade, want: goProof("go_dependency_upgrade")},
		{name: "Go downgrade refused", source: goSource, target: goDowngrade, diagnostic: "root Go dependency files are not safely absorbed:"},
		{name: "native invalid target", source: source, target: "missing-target-object", diagnostic: "verify source ancestry:"},
		{name: "native missing source", source: strings.Repeat("f", 40), target: target, diagnostic: "fetch origin missing-source-branch to resolve receipted source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := proveAbsorbedConflictSource(context.Background(), repo, tc.target, WorktreeMergeSource{SHA: tc.source, Branch: "missing-source-branch"}, tc.derived)
			if tc.diagnostic != "" {
				if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
					t.Fatalf("error=%v want %q", err, tc.diagnostic)
				}
				if !reflect.DeepEqual(got, absorbedConflictProof{}) {
					t.Fatalf("refusal leaked proof %+v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("proof=%+v want=%+v error=%v", got, tc.want, err)
			}
		})
	}
	for _, stage := range []string{"success content", "success ancestor", "merge-base refusal", "diff refusal"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			sourceSHA, targetSHA := source, target
			want := mixed
			if stage == "success ancestor" {
				sourceSHA, targetSHA = base, source
				want = absorbedConflictProof{method: "ancestor"}
			}
			cause := errors.New("owned query refusal")
			r := &absorbedSourceProofRunner{Runner: defaultRunner, t: t, repo: repo, cause: cause}
			mergeArgv := []string{"git", "merge-base", sourceSHA, targetSHA}
			diffArgv := []string{"git", "diff", "--name-only", base, sourceSHA}
			prefix := ""
			if stage == "merge-base refusal" {
				r.fail = mergeArgv
				prefix = "verify source ancestry:"
			}
			if stage == "diff refusal" {
				r.fail = diffArgv
				prefix = "diff source from its merge base with current target:"
			}
			got, err := proveAbsorbedConflictSourceWithRunner(context.Background(), r, repo, targetSHA, WorktreeMergeSource{SHA: sourceSHA}, derived)
			expected := [][]string{mergeArgv}
			if stage != "success ancestor" && stage != "merge-base refusal" {
				expected = append(expected, diffArgv)
			}
			if !reflect.DeepEqual(r.calls, expected) {
				t.Fatalf("queries=%q want exactly %q", r.calls, expected)
			}
			if prefix != "" {
				if !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), prefix) || !reflect.DeepEqual(got, absorbedConflictProof{}) {
					t.Fatalf("proof=%+v err=%v want wrapped %s", got, err, prefix)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("native proof=%+v want=%+v error=%v", got, want, err)
			}
		})
	}
	// Abbreviated source input must retain exact source-string equality semantics:
	// native merge-base emits the full ID, so this remains a content proof.
	t.Run("abbreviated source preserves exact comparison", func(t *testing.T) {
		t.Parallel()
		got, err := proveAbsorbedConflictSource(context.Background(), repo, source, WorktreeMergeSource{SHA: base[:12]}, nil)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("source changed no path relative to its merge base %s", base)) || !reflect.DeepEqual(got, absorbedConflictProof{}) {
			t.Fatalf("proof=%+v error=%v", got, err)
		}
	})
}
