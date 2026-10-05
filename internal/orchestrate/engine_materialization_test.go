package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestWorktreeEffortNormalizationPreservesASCIIAndRejectsUnicodeAliases(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name      string
		character rune
		want      bool
	}{
		{"lower start", 'a', true}, {"lower end", 'z', true},
		{"upper start", 'A', true}, {"upper end", 'Z', true},
		{"digit start", '0', true}, {"digit end", '9', true},
		{"punctuation", '-', false}, {"negative", -1, false},
		{"zero", 0, false}, {"Latin letter", 'é', false},
		{"Unicode lower low byte aliases ASCII a", '\u0161', false},
		{"Unicode upper low byte aliases ASCII A", '\u0141', false},
		{"replacement rune", '\ufffd', false},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := isASCIIAlphanumeric(row.character); got != row.want {
				t.Fatalf("isASCIIAlphanumeric(%U) = %t, want %t", row.character, got, row.want)
			}
		})
	}
	for _, row := range []struct {
		name, input, want string
	}{
		{"unchanged valid segment", "aA0._-zZ9", "aA0._-zZ9"},
		{"trimmed punctuation", ".-_A9_.-", "A9"},
		{"Unicode alias alone", "\u0161\u0141", ""},
		{"Unicode alias prefix", "\u0161name", "name"},
		{"Unicode alias interior", "a\u0161b", "a-b"},
		{"invalid UTF8 prefix and suffix", "\xffA\xfe", "A"},
		{"invalid UTF8 interior", "a\xffb", "a-b"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := worktreeEffortSegment(row.input); got != row.want {
				t.Fatalf("worktreeEffortSegment(%q) = %q, want %q", row.input, got, row.want)
			}
		})
	}
}

func TestWorktreeManifestRejectsInvalidRepositoryAfterBaseObservation(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "origin/main"}, runner.Result{Stdout: "observed-base\n"}, nil)
	root := t.TempDir()
	err := recordWorktreeManifest(context.Background(), root, root, root, Repository{Slug: "invalid"}, ResolvedBase{Ref: "main"}, Options{run: fake, Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "invalid repository") {
		t.Fatalf("repository validation after base read: %v", err)
	}
}
