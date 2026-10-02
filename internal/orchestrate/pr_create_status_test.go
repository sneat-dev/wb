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

// nul joins porcelain -z fields the way git writes them.
func nul(fields ...string) string { return strings.Join(fields, "\x00") + "\x00" }

func TestParsePorcelainStatusZKeepsEveryColumnAndTheWholePath(t *testing.T) {
	t.Parallel()
	got := mustParseStatus(t, nul(
		" M .gitignore",          // unstaged change to a root dotfile: the blank first column must survive
		"M  .github/x",           // staged change under a dot directory
		"MM a/.b",                // staged and unstaged, dotfile in a subdirectory
		"?? new file with space", // untracked, path holding spaces
		"R  renamed.go",          // rename: new path, then the original
		"old.go",
		" D gone.go",
		"C  copy.go",
		"src.go",
	))
	want := []worktreeStatusEntry{
		{' ', 'M', ".gitignore"},
		{'M', ' ', ".github/x"},
		{'M', 'M', "a/.b"},
		{'?', '?', "new file with space"},
		{'R', ' ', "renamed.go"},
		{' ', 'D', "gone.go"},
		{'C', ' ', "copy.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %#v\nwant      %#v", got, want)
	}
}

func mustParseStatus(t *testing.T, output string) []worktreeStatusEntry {
	t.Helper()
	entries, err := parsePorcelainStatusZ(output)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestParsePorcelainStatusZOfACleanWorktreeIsEmpty(t *testing.T) {
	t.Parallel()
	if got := mustParseStatus(t, ""); len(got) != 0 {
		t.Fatalf("clean status parsed as %#v", got)
	}
}

// A status the parser cannot fully read must refuse, never look clean.
func TestParsePorcelainStatusZRefusesAnythingItCannotParse(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"stderr warning fused with the first entry": "warning: could not open directory 'priv/': Permission denied\n?? a.txt\x00",
		"field shorter than four bytes":             "xy\x00",
		"entry with an empty path":                  "?? \x00",
		"missing separator after the status":        "MMx.go\x00",
		"rename without its original path":          "R  new.go\x00",
		"rename with an empty original path":        "R  new.go\x00\x00",
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if entries, err := parsePorcelainStatusZ(output); err == nil {
				t.Fatalf("parsed %q as %#v, want an error", output, entries)
			}
		})
	}
}

func TestParsePorcelainStatusZKeepsAnUnknownStatusCodeAsADirtyEntry(t *testing.T) {
	t.Parallel()
	entries := mustParseStatus(t, "ZZ odd.go\x00")
	if len(entries) != 1 || entries[0].path != "odd.go" {
		t.Fatalf("entries = %#v, want the unknown-status entry kept", entries)
	}
}

// git can warn on stderr and still exit 0 (an unreadable directory); the
// status is then only partly read and must not pass as a clean tree.
func TestReadPorcelainStatusRefusesAWarningOnStderrEvenOnSuccess(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool { return c.Name == "git" && !c.Opts.CaptureCombined }, runner.Result{
		Stdout: "?? a.txt\x00", Stderr: "warning: could not open directory 'priv/': Permission denied\n",
	}, nil)
	entries, err := readPorcelainStatus(context.Background(), fake, 0, "wt")
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("entries=%#v err=%v, want a refusal carrying git's diagnostic", entries, err)
	}
}

func TestReadPorcelainStatusParsesStdoutAndReportsAFailedGit(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(runnertest.Call) bool { return true }, runner.Result{Stdout: " M .gitignore\x00R  b.go\x00a.go\x00"}, nil)
	entries, err := readPorcelainStatus(context.Background(), fake, time.Minute, "wt")
	if err != nil || len(entries) != 2 || entries[0].path != ".gitignore" || entries[1].path != "b.go" {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	fake = runnertest.New(t)
	fake.Expect(func(runnertest.Call) bool { return true }, runner.Result{Stderr: "fatal: not a git repository"}, errors.New("exit status 128"))
	if _, err := readPorcelainStatus(context.Background(), fake, 0, "wt"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v, want git's failure", err)
	}
}

func TestWorktreeStatusEntryRendersStatusAndWholePathWithoutPadding(t *testing.T) {
	t.Parallel()
	got := describeStatusEntries([]worktreeStatusEntry{{' ', 'M', ".gitignore"}, {'?', '?', "x.go"}, {'M', 'M', "a/.b"}})
	want := []string{"M .gitignore", "?? x.go", "MM a/.b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("described = %v, want %v", got, want)
	}
}

func TestLeftoverBeyondAddedPathsKeepsDotfilesWhole(t *testing.T) {
	t.Parallel()
	entries := mustParseStatus(t, nul(" M .gitignore", " M .github/x", "M  a/.b", "?? dir/", " M tree/deep/file.go", "R  moved.go", "was.go"))
	cases := []struct {
		name  string
		added []string
		want  []string
	}{
		{"root dotfile named in --add leaves nothing", []string{".gitignore", ".github/x", "a/.b", "dir", "tree", "moved.go"}, nil},
		{"unnamed dotfile is reported with its leading dot", []string{".github/x", "a/.b", "dir", "tree", "moved.go"}, []string{".gitignore"}},
		{"unnamed dot directory file is reported whole", []string{".gitignore", "a/.b", "dir", "tree", "moved.go"}, []string{".github/x"}},
		{"unnamed subdirectory dotfile is reported whole", []string{".gitignore", ".github/x", "dir", "tree", "moved.go"}, []string{"a/.b"}},
		{"a renamed path is matched by its new name", []string{".gitignore", ".github/x", "a/.b", "dir", "tree"}, []string{"moved.go"}},
		{"a directory covers an untracked dir and tracked files beneath it", []string{".gitignore", ".github/x", "a/.b", "moved.go", "dir/", "tree/"}, nil},
		{"a sibling with the same prefix is not covered", []string{".gitignore", ".github/x", "a/.b", "moved.go", "dir", "tre"}, []string{"tree/deep/file.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := leftoverBeyondAddedPaths(entries, tc.added); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("leftover = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLeftoverAfterStagedCommitSeesAnUnstagedChangeToADotfile(t *testing.T) {
	t.Parallel()
	entries := mustParseStatus(t, nul("M  staged.go", " M .gitignore", "MM both.go", "?? new.go", "R  moved.go", "was.go"))
	want := []string{".gitignore", "both.go", "new.go"}
	if got := leftoverAfterStagedCommit(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("leftover = %v, want %v", got, want)
	}
}

// A rename's original path is not an entry of its own: -z never emits " -> ",
// so the old engine parser turned it into a bogus status line.
func TestWorktreeStatusMapsARenameToItsNewPathOnly(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(runnertest.Call) bool { return true }, runner.Result{Stdout: "R  new.go\x00old.go\x00 M .gitignore\x00"}, nil)
	got, err := worktreeStatus(context.Background(), "wt", Options{run: fake})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"new.go": "R ", ".gitignore": " M"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %v, want %v", got, want)
	}
}

func TestWorktreeStatusFailsClosedOnAWarningOnStderr(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(runnertest.Call) bool { return true }, runner.Result{Stdout: "?? a.txt\x00", Stderr: "warning: could not open directory"}, nil)
	if _, err := worktreeStatus(context.Background(), "wt", Options{run: fake}); err == nil {
		t.Fatal("a status with a stderr warning passed")
	}
}
