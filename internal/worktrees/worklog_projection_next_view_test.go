package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkLogProjectionNextPromptSequenceRetainsNativeReadRefusals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"closed names", "closed rewind", "removed prompt", "malformed prompt"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			worktree := projectionNextTemp(t)
			directory, err := openJournalSubdirectory(worktree, promptsDirectory, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			prompt := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory, "0000-instruction.md")
			content := []byte("---\nsource: human_declared\nseq: 0\n---\nexact prompt body\n")
			projectionNextWrite(t, prompt, content)
			var observe workLogProjectionObservation
			var nativeCause error
			switch kind {
			case "closed names":
				observe = projectionNextCloseEnumeration(t, workLogPromptNamesRead, &nativeCause)
			case "closed rewind":
				observe = projectionNextClose(t, workLogPromptNamesRewind)
			case "removed prompt":
				observe = func(phase workLogProjectionBoundary, _ *os.File) {
					if phase == workLogPromptRecordRead {
						if err := os.Remove(prompt); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "malformed prompt":
				projectionNextWrite(t, prompt, []byte("---\nsource: human_declared\n"))
			}
			records, err := listPromptRecordsObserved(worktree, true, observe)
			if records != nil || err == nil {
				t.Fatalf("prompt refusal=%+v %v", records, err)
			}
			if kind == "closed names" && (nativeCause == nil || !errors.Is(err, nativeCause)) {
				t.Fatalf("owned native enumeration cause=%v probe=%v", err, nativeCause)
			}
			if kind == "closed rewind" && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("owned descriptor cause=%v", err)
			}
			if kind == "removed prompt" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native removed cause=%v", err)
			}
			if kind == "malformed prompt" && !strings.Contains(err.Error(), "unterminated YAML frontmatter") {
				t.Fatalf("malformed diagnostic=%v", err)
			}
			if strings.HasPrefix(kind, "closed") {
				if raw, err := os.ReadFile(prompt); err != nil || string(raw) != string(content) {
					t.Fatalf("prompt altered=%q %v", raw, err)
				}
			}
		})
	}
	worktree := projectionNextTemp(t)
	journal := filepath.Join(worktree, journalRootDirectory)
	projectionNextWrite(t, journal, []byte("repository-owned journal occupant"))
	if records, err := listPromptRecords(worktree, true); records != nil || err == nil {
		t.Fatalf("journal occupant refusal=%+v %v", records, err)
	}
	if raw, err := os.ReadFile(journal); err != nil || string(raw) != "repository-owned journal occupant" {
		t.Fatalf("journal occupant evidence=%q %v", raw, err)
	}

}
func TestWorkLogProjectionNextPromptParserPreservesGrammarAndBodies(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "---\nsource: human_declared\n", "---\nsource: invalid\n---\nbody\n", "---\nsource: [\n---\nbody\n"} {
		if _, _, err := parsePromptFile([]byte(content)); err == nil {
			t.Fatalf("invalid frontmatter accepted %q", content)
		}
	}
	for _, ordinal := range []int{0, 9999} {
		content := fmt.Sprintf("---\nsource: human_declared\nseq: %d\n---\n\nretained body\n", ordinal)
		header, body, err := parsePromptFile([]byte(content))
		if err != nil || header.Seq != ordinal || body != "retained body\n" {
			t.Fatalf("parsed=%+v %q %v", header, body, err)
		}
		worktree := projectionNextTemp(t)
		directory, err := openJournalSubdirectory(worktree, promptsDirectory, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory, fmt.Sprintf("%04d-instruction.md", ordinal))
		projectionNextWrite(t, path, []byte(content))
		records, err := listPromptRecords(worktree, true)
		if ordinal == 0 {
			if err != nil || len(records) != 1 || records[0].Body != "retained body\n" {
				t.Fatalf("record=%+v %v", records, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "not contiguous") {
			t.Fatalf("max four-digit ordinal diagnostic=%v", err)
		}
	}
	home, run, _ := projectionNextRun(t)
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOriginalPrompt(home, workLogClaim{EffortID: "task", RunID: "run"}, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native archive absence=%v", err)
	}
}
