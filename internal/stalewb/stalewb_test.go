package stalewb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// stub writes a file named name under a new temp dir, executable or not.
func stub(t *testing.T, name, content string, executable bool) string {
	t.Helper()
	if !executable {
		content = ""
	}
	return writeStub(t, name, content)
}

// anyFile stands for "has an execute bit": tests never set one (the repository
// guards against test-written executables), so a stub counts as executable
// when it has content, and the platform rule is tested on modes directly.
func anyFile(info os.FileInfo) bool { return info.Size() > 0 }

func writeStub(t *testing.T, name, content string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	// Never run: the probe is a seam.
	if err = os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func pathOf(paths ...string) string {
	dirs := make([]string, len(paths))
	for index, path := range paths {
		dirs[index] = filepath.Dir(path)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func versions(table map[string]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, path string) (string, error) {
		version, ok := table[path]
		if !ok {
			return "", errors.New("probe failed")
		}
		return version, nil
	}
}

func TestStaleReportsEveryOlderWbOnPathAndNothingElse(t *testing.T) {
	t.Parallel()
	current := stub(t, "wb", "x", true)
	older := stub(t, "wb", "x", true)
	newer := stub(t, "wb", "x", true)
	same := stub(t, "wb", "x", true)
	broken := stub(t, "wb", "x", true)
	notSemver := stub(t, "wb", "x", true)
	scanner := Scanner{
		Executable: anyFile,
		PathEnv:    pathOf(current, older, newer, same, broken, notSemver),
		Skip:       current,
		Probe: versions(map[string]string{
			current: "0.208.3", older: "0.198.2", newer: "0.300.0", same: "v0.208.3", notSemver: "dev",
		}),
	}
	got := scanner.Stale(context.Background(), "0.208.3")
	if len(got) != 1 || got[0].Path != older || got[0].Version != "0.198.2" || got[0].RealPath != "" {
		t.Fatalf("stale = %+v, want only %s at 0.198.2", got, older)
	}
}

func TestStaleDoesNotJudgeWithoutAReleaseVersionToCompareTo(t *testing.T) {
	t.Parallel()
	older := stub(t, "wb", "x", true)
	scanner := Scanner{Executable: anyFile, PathEnv: pathOf(older), Probe: versions(map[string]string{older: "0.1.0"})}
	if got := scanner.Stale(context.Background(), "dev"); got != nil {
		t.Fatalf("stale = %+v", got)
	}
	if got := scanner.Stale(nil, "v0.2.0"); len(got) != 1 { //nolint:staticcheck // nil context is the caller bug Stale tolerates
		t.Fatalf("stale with a nil context = %+v", got)
	}
}

func TestFindProbesOnlyRegularExecutableFilesNamedWbOncePerRealFile(t *testing.T) {
	t.Parallel()
	real := stub(t, "wb", "x", true)
	linkDir := t.TempDir()
	if err := os.Symlink(real, filepath.Join(linkDir, "wb")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	notExecutable := stub(t, "wb", "x", false)
	otherName := stub(t, "wb-old", "x", true)
	dirNamedWb := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirNamedWb, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	dangling := t.TempDir()
	if err := os.Symlink(filepath.Join(dangling, "missing"), filepath.Join(dangling, "wb")); err != nil {
		t.Fatal(err)
	}
	pathEnv := strings.Join([]string{"", filepath.Dir(real), linkDir, filepath.Dir(notExecutable), filepath.Dir(otherName), dirNamedWb, dangling, filepath.Join(dangling, "absent")}, string(os.PathListSeparator))
	found := Scanner{Executable: anyFile, PathEnv: pathEnv}.Find()
	if len(found) != 1 || found[0].Path != real {
		t.Fatalf("found = %+v, want only %s (the symlink is the same real file)", found, real)
	}
	// Listed first, the symlink is the entry reported and names its target.
	scanner := Scanner{Executable: anyFile, PathEnv: linkDir + string(os.PathListSeparator) + filepath.Dir(real), Probe: versions(map[string]string{filepath.Join(linkDir, "wb"): "0.1.0"})}
	got := scanner.Stale(context.Background(), "0.2.0")
	resolvedReal, _ := filepath.EvalSymlinks(real)
	if len(got) != 1 || got[0].Path != filepath.Join(linkDir, "wb") || got[0].RealPath != resolvedReal {
		t.Fatalf("stale = %+v", got)
	}
}

func TestWarningNamesEachStalePathItsVersionAndTheFixWithoutRemovingAnything(t *testing.T) {
	t.Parallel()
	if Warning("0.208.3", nil) != "" {
		t.Fatal("no stale copies must give no warning")
	}
	kept := stub(t, "wb", "x", true)
	text := Warning("v0.208.3", []Binary{
		{Path: kept, Version: "0.198.2"},
		{Path: "/home/a b/it's/wb", RealPath: "/opt/real/wb", Version: "0.1.0"},
	})
	for _, want := range []string{"than 0.208.3", kept + " is 0.198.2", "fix: rm " + kept, "(symlink to /opt/real/wb)", `rm '/home/a b/it'\''s/wb'`, "install method that owns it"} {
		if !strings.Contains(text, want) {
			t.Errorf("warning lacks %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the warning removed the file: %v", err)
	}
}

func TestProbeWithRunsVersionJSONThroughTheRunnerAndMarksTheChildAsAProbe(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		result  runner.Result
		err     error
		version string
		failed  bool
	}{
		"a version":      {result: runner.Result{Stdout: `{"version":"0.198.2"}`}, version: "0.198.2"},
		"a run failure":  {err: errors.New("exit 3"), failed: true},
		"not json":       {result: runner.Result{Stdout: "not json"}, failed: true},
		"no version key": {result: runner.Result{Stdout: "{}"}, failed: true},
	} {
		fake := runnertest.New(t).Expect(func(runnertest.Call) bool { return true }, test.result, test.err)
		version, err := ProbeWith(fake)(context.Background(), "/x/wb")
		if (err != nil) != test.failed || version != test.version {
			t.Errorf("%s: version = %q, err = %v", name, version, err)
		}
		calls := fake.Calls()
		if len(calls) != 1 || strings.Join(calls[0].Argv(), " ") != "/x/wb version --json" || calls[0].Op != "RunOpts" {
			t.Fatalf("%s: calls = %+v", name, calls)
		}
		marked := false
		for _, entry := range calls[0].Opts.Env {
			marked = marked || entry == SkipEnv+"=1"
		}
		if !marked {
			t.Errorf("%s: the child was not marked as a probe", name)
		}
	}
}

func TestNewScansTheProcessPathAndSkipsTheRunningBinary(t *testing.T) {
	t.Parallel()
	scanner := New()
	if scanner.PathEnv != os.Getenv("PATH") || scanner.Probe == nil || scanner.Skip == "" {
		t.Fatalf("scanner = %+v", scanner)
	}
}

func TestDisabledFollowsTheProbeMarker(t *testing.T) {
	t.Parallel()
	if disabledBy("") || !disabledBy("1") {
		t.Fatal("only a non-empty marker disables the scan")
	}
	if Disabled() != disabledBy(os.Getenv(SkipEnv)) {
		t.Fatal("Disabled must read the process environment")
	}
}

func TestExecutableModeNeedsAnExecuteBitExceptOnWindows(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		mode os.FileMode
		goos string
		want bool
	}{
		"owner execute":              {0o700, "linux", true},
		"group execute":              {0o610, "darwin", true},
		"no execute bit":             {0o644, "linux", false},
		"windows has no bit":         {0o666, "windows", true},
		"a directory is not a file":  {os.ModeDir | 0o755, "linux", false},
		"a windows directory either": {os.ModeDir | 0o666, "windows", false},
	} {
		if got := isExecutable(test.mode, test.goos); got != test.want {
			t.Errorf("%s: isExecutable = %t, want %t", name, got, test.want)
		}
	}
}

func TestFindWithTheDefaultRuleSkipsFilesWithoutAnExecuteBit(t *testing.T) {
	t.Parallel()
	plain := stub(t, "wb", "x", false)
	if got := (Scanner{PathEnv: filepath.Dir(plain)}).Find(); len(got) != 0 {
		t.Fatalf("found = %+v", got)
	}
}
