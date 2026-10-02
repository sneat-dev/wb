package orchestrate

import (
	"reflect"
	"strings"
	"testing"
)

// nul joins porcelain -z fields the way git writes them.
func nul(fields ...string) string { return strings.Join(fields, "\x00") + "\x00" }

func TestParsePorcelainStatusZKeepsEveryColumnAndTheWholePath(t *testing.T) {
	t.Parallel()
	got := parsePorcelainStatusZ(nul(
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

func TestParsePorcelainStatusZOfACleanWorktreeIsEmpty(t *testing.T) {
	t.Parallel()
	if got := parsePorcelainStatusZ(""); len(got) != 0 {
		t.Fatalf("clean status parsed as %#v", got)
	}
	if got := parsePorcelainStatusZ("xy"); len(got) != 0 {
		t.Fatalf("a truncated field parsed as %#v", got)
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
	entries := parsePorcelainStatusZ(nul(" M .gitignore", " M .github/x", "M  a/.b", "?? dir/", " M tree/deep/file.go", "R  moved.go", "was.go"))
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
	entries := parsePorcelainStatusZ(nul("M  staged.go", " M .gitignore", "MM both.go", "?? new.go", "R  moved.go", "was.go"))
	want := []string{".gitignore", "both.go", "new.go"}
	if got := leftoverAfterStagedCommit(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("leftover = %v, want %v", got, want)
	}
}
