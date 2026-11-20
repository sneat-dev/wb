package stalewb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stub writes a file named name under a new temp dir with the given mode.
func stub(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
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
	current := stub(t, "wb", "x", 0o755)
	older := stub(t, "wb", "x", 0o755)
	newer := stub(t, "wb", "x", 0o755)
	same := stub(t, "wb", "x", 0o755)
	broken := stub(t, "wb", "x", 0o755)
	notSemver := stub(t, "wb", "x", 0o755)
	scanner := Scanner{
		PathEnv: pathOf(current, older, newer, same, broken, notSemver),
		Skip:    current,
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
	older := stub(t, "wb", "x", 0o755)
	scanner := Scanner{PathEnv: pathOf(older), Probe: versions(map[string]string{older: "0.1.0"})}
	if got := scanner.Stale(context.Background(), "dev"); got != nil {
		t.Fatalf("stale = %+v", got)
	}
	if got := scanner.Stale(nil, "v0.2.0"); len(got) != 1 { //nolint:staticcheck // nil context is the caller bug Stale tolerates
		t.Fatalf("stale with a nil context = %+v", got)
	}
}

func TestFindProbesOnlyRegularExecutableFilesNamedWbOncePerRealFile(t *testing.T) {
	t.Parallel()
	real := stub(t, "wb", "x", 0o755)
	linkDir := t.TempDir()
	if err := os.Symlink(real, filepath.Join(linkDir, "wb")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	notExecutable := stub(t, "wb", "x", 0o644)
	otherName := stub(t, "wb-old", "x", 0o755)
	dirNamedWb := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirNamedWb, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	dangling := t.TempDir()
	if err := os.Symlink(filepath.Join(dangling, "missing"), filepath.Join(dangling, "wb")); err != nil {
		t.Fatal(err)
	}
	pathEnv := strings.Join([]string{"", filepath.Dir(real), linkDir, filepath.Dir(notExecutable), filepath.Dir(otherName), dirNamedWb, dangling, filepath.Join(dangling, "absent")}, string(os.PathListSeparator))
	found := Scanner{PathEnv: pathEnv}.Find()
	if len(found) != 1 || found[0].Path != real {
		t.Fatalf("found = %+v, want only %s (the symlink is the same real file)", found, real)
	}
	// Listed first, the symlink is the entry reported and names its target.
	scanner := Scanner{PathEnv: linkDir + string(os.PathListSeparator) + filepath.Dir(real), Probe: versions(map[string]string{filepath.Join(linkDir, "wb"): "0.1.0"})}
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
	kept := stub(t, "wb", "x", 0o755)
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

func TestExecProbeReadsTheVersionOfAStubAndMarksTheChildAsAProbe(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	ok := stub(t, "wb", "#!/bin/sh\n[ \"$1 $2\" = \"version --json\" ] && [ -n \"$"+SkipEnv+"\" ] && echo '{\"version\":\"0.198.2\"}'\n", 0o755)
	if version, err := ExecProbe(context.Background(), ok); err != nil || version != "0.198.2" {
		t.Fatalf("version = %q, err = %v", version, err)
	}
	failing := stub(t, "wb", "#!/bin/sh\nexit 3\n", 0o755)
	if _, err := ExecProbe(context.Background(), failing); err == nil {
		t.Fatal("a failing probe must be an error")
	}
	garbage := stub(t, "wb", "#!/bin/sh\necho not json\n", 0o755)
	if _, err := ExecProbe(context.Background(), garbage); err == nil {
		t.Fatal("unparseable output must be an error")
	}
	empty := stub(t, "wb", "#!/bin/sh\necho '{}'\n", 0o755)
	if _, err := ExecProbe(context.Background(), empty); err == nil {
		t.Fatal("a document without a version must be an error")
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
	// Not parallel: it sets the process environment.
	t.Setenv(SkipEnv, "")
	if Disabled() {
		t.Fatal("empty marker must not disable")
	}
	t.Setenv(SkipEnv, "1")
	if !Disabled() {
		t.Fatal("marker must disable")
	}
}
