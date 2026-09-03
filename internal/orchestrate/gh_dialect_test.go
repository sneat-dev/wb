package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The land and merge verbs must work against the `gh` actually installed on
// this fleet — 2.45 — which has neither `gh api --slurp` nor
// `gh pr checks --json`. Both were on the merge verb's critical path, so its
// checks stage failed on every run, operators fell back to raw `gh pr merge`,
// and the opt-in cleanup that should have retired the worktree never ran.
//
// This is a source-level guard rather than a behavioural one on purpose: the
// regression is a *call* reappearing, and a behavioural test only catches it on
// the paths a test happens to exercise.
func TestNoSourceFileShellsOutToAGHDialectTheInstalledClientLacks(t *testing.T) {
	forbidden := []struct {
		fragment string
		why      string
	}{
		{`"--slurp"`, "gh api --slurp needs a newer client; follow the link header instead (githubobserver.GetPages)"},
		{`"checks",`, "gh pr checks --json needs a newer client; read the head commit's check runs and statuses instead"},
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, directory := range []string{"internal", "cmd"} {
		root := filepath.Join(repoRoot, directory)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			text := string(contents)
			for _, banned := range forbidden {
				index := strings.Index(text, banned.fragment)
				if index < 0 {
					continue
				}
				// Only an argument list actually handed to gh matters; the same
				// characters inside a comment or an unrelated call do not.
				line := lineAround(text, index)
				if !strings.Contains(line, `"pr"`) && !strings.Contains(line, `"api"`) {
					continue
				}
				t.Errorf("%s passes %s to gh: %s\n  %s", path, banned.fragment, banned.why, strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func lineAround(text string, index int) string {
	start := strings.LastIndexByte(text[:index], '\n') + 1
	end := strings.IndexByte(text[index:], '\n')
	if end < 0 {
		return text[start:]
	}
	return text[start : index+end]
}
