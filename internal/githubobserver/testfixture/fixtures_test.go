package testfixture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fixtureError struct{ value any }
type failingEnvironment struct {
	paths []string
	next  int
}

func (*failingEnvironment) Helper()             {}
func (*failingEnvironment) Fatal(values ...any) { panic(fixtureError{values[0]}) }
func (env *failingEnvironment) TempDir() string { path := env.paths[env.next]; env.next++; return path }
func (*failingEnvironment) Setenv(string, string) {
	panic("failed fixture must not publish environment")
}

func expectFixtureError(t *testing.T, call func(), wantOp string) {
	t.Helper()
	defer func() {
		failure, ok := recover().(fixtureError)
		if !ok {
			t.Fatal("fixture did not report the expected fatal error")
		}
		err, ok := failure.value.(error)
		if !ok {
			t.Fatalf("fatal value=%v, want error", failure.value)
		}
		if wantOp != "" {
			var pathErr *os.PathError
			var linkErr *os.LinkError
			if errors.As(err, &pathErr) {
				if pathErr.Op != wantOp {
					t.Fatalf("filesystem operation=%q want %q", pathErr.Op, wantOp)
				}
			} else if errors.As(err, &linkErr) {
				if linkErr.Op != wantOp {
					t.Fatalf("filesystem operation=%q want %q", linkErr.Op, wantOp)
				}
			} else {
				t.Fatalf("error=%v, want filesystem error", err)
			}
		}
	}()
	call()
}

func TestScriptFixturesReportFilesystemFailuresBeforePublishingEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		kind    int
		install func(Environment)
	}{
		{"state directory", 0, func(env Environment) { InstallGH(env, "#!/bin/sh\n") }},
		{"bin directory", 1, func(env Environment) { InstallGH(env, "#!/bin/sh\n") }},
		{"script write", 2, func(env Environment) { InstallGH(env, "#!/bin/sh\n") }},
		{"transient bin directory", 0, func(env Environment) { InstallTransientReadGH(env, "#!/bin/sh\n") }},
		{"transient script write", 3, func(env Environment) { InstallTransientReadGH(env, "#!/bin/sh\n") }},
		{"direct CI script write", 4, InstallDirectCIGH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := t.TempDir()
			second := t.TempDir()
			blocked := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			env := &failingEnvironment{paths: []string{first, second}}
			switch tc.kind {
			case 0:
				env.paths[0] = blocked
			case 1:
				env.paths[1] = blocked
			case 2:
				if err := os.MkdirAll(filepath.Join(second, "bin", "gh"), 0o755); err != nil {
					t.Fatal(err)
				}
			case 3:
				if err := os.MkdirAll(filepath.Join(first, "bin", "gh"), 0o755); err != nil {
					t.Fatal(err)
				}
			case 4:
				if err := os.Mkdir(filepath.Join(first, "gh"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			wantOp := "rename"
			if tc.kind == 0 || tc.kind == 1 {
				wantOp = "mkdir"
			}
			expectFixtureError(t, func() { tc.install(env) }, wantOp)
		})
	}
}

func TestStateAnswerReportsMissingParent(t *testing.T) {
	t.Parallel()
	state := State{Dir: filepath.Join(t.TempDir(), "missing")}
	expectFixtureError(t, func() { state.Answer(&failingEnvironment{}, "body", "receipt") }, "open")
}

func TestPullRequestViewReportsInvalidJSONOrDestination(t *testing.T) {
	t.Parallel()
	t.Run("unescaped head", func(t *testing.T) {
		t.Parallel()
		expectFixtureError(t, func() { PullRequestView[map[string]any](&failingEnvironment{}, `invalid"head`) }, "")
	})
	t.Run("incompatible destination", func(t *testing.T) {
		t.Parallel()
		expectFixtureError(t, func() { PullRequestView[int](&failingEnvironment{}, "head") }, "")
	})
}
