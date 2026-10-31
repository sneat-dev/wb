package worktreeclaims

import (
	"errors"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

func TestPromptScanRefreshesAPreopenedStreamBeforeChoosingTheNextOrdinal(t *testing.T) {
	t.Parallel()
	root, ports := testCanonicalTemp(t), testPorts()
	if _, err := ports.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("first instruction")); err != nil {
		t.Fatal(err)
	}
	stale, err := worktreejournal.OpenJournalSubdirectory(root, promptsDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stale.Close() }()
	if _, err := stale.Readdirnames(-1); err != nil {
		t.Fatal(err)
	}
	// Another lock holder publishes after this stream was opened/read. Its
	// committed prompt must be visible before any subsequent ordinal decision.
	if _, err := ports.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("second instruction")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		prompts, err := ports.ListPromptsIn(stale)
		if err != nil || len(prompts) != 2 || prompts[0].Seq != 0 || prompts[1].Seq != 1 {
			t.Fatalf("preopened stream lost committed instructions: %#v %v", prompts, err)
		}
	}
	if _, err := ports.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("third instruction")); err != nil {
		t.Fatal(err)
	}
	prompts, err := ports.ListPrompts(root)
	if err != nil || len(prompts) != 3 || prompts[2].Seq != 2 {
		t.Fatalf("next ordinal overwrote or duplicated existing instructions: %#v %v", prompts, err)
	}
}

func TestPromptScanRefusesFailedPreReadResetBeforeReadingOrPublishing(t *testing.T) {
	t.Parallel()
	root, ports := testCanonicalTemp(t), testPorts()
	failure := errors.New("directory stream reset failed")
	reads := 0
	ports.ReadNames = func(*os.File) ([]string, error) { reads++; return nil, nil }
	ports.Rewind = func(*os.File) error { return failure }
	if _, err := ports.AppendPrompt(root, PromptHeader{Source: PromptSourceAgent}, []byte("must not publish")); !errors.Is(err, failure) {
		t.Fatalf("failed stream reset accepted: %v", err)
	}
	if reads != 0 {
		t.Fatal("stale stream was read after reset failure")
	}
	prompts, err := testPorts().ListPrompts(root)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("failed reset published an instruction: %#v %v", prompts, err)
	}
}
