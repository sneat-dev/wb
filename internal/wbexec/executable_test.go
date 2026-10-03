package wbexec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHookExecutableResolvesCurrentThenPATHThenFallback(t *testing.T) {
	t.Parallel()
	if got := HookExecutable(); got == "" {
		t.Fatal("running test process has no executable")
	}
	sentinel := errors.New("lookup failed")
	for _, tc := range []struct {
		current, path       string
		currentErr, pathErr error
		want                string
	}{{"/current", "/path", nil, nil, "/current"}, {"", "/path", sentinel, nil, "/path"}, {"", "", sentinel, sentinel, "wb"}} {
		calls := 0
		got := hookExecutableWith(func() (string, error) { return tc.current, tc.currentErr }, func(name string) (string, error) {
			calls++
			if name != "wb" {
				t.Fatal(name)
			}
			return tc.path, tc.pathErr
		})
		if got != tc.want {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
		if tc.currentErr == nil && calls != 0 {
			t.Fatal("PATH looked up before current process")
		}
	}
}
func TestResolveGovernorPreservesBarePermissionRuleOnlyForSameFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("provider"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := ResolveGovernorExecutable(""); got != "" {
		t.Fatal(got)
	}
	sentinel := errors.New("missing")
	for _, tc := range []struct {
		name, self, path string
		lookupErr        error
		failStat         string
		want             string
	}{{"empty", "", "", nil, "", ""}, {"no-path", first, "", sentinel, "", first}, {"self-stat", first, first, nil, first, first}, {"path-stat", first, second, nil, second, first}, {"same", first, first, nil, "", ""}, {"different", first, second, nil, "", first}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveGovernorWith(tc.self, func(string) (string, error) { return tc.path, tc.lookupErr }, func(p string) (os.FileInfo, error) {
				if p == tc.failStat {
					return nil, sentinel
				}
				return os.Stat(p)
			})
			if got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}
func TestQuoteShellWordPreservesLegacyBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ value, want string }{{"", "''"}, {"wb", "wb"}, {"/path/é/wb", "/path/é/wb"}, {"path with spaces", "'path with spaces'"}, {"a'b", `'a'\''b'`}, {"$HOME", "'$HOME'"}} {
		if got := QuoteShellWord(tc.value); got != tc.want {
			t.Fatalf("value=%q got=%q want=%q", tc.value, got, tc.want)
		}
	}
}
