package session

import (
	"github.com/sneat-dev/wb/internal/sessionterminal"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeBindingAcceptsOnlyExactIdentityAndBoundedMetadataEnrichment(t *testing.T) {
	t.Parallel()
	root, expected, _ := currentTransitionFixture(t)
	expected.Terminal = sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(root, "herdr.sock"), WorkspaceID: "w1", PaneID: "w1:p7"}
	var err error
	expected, err = Register(root, expected)
	if err != nil {
		t.Fatal(err)
	}
	current := expected
	current.Model = "known-model"
	current.NativeHarnessID = "native-current"
	if _, err := Register(root, current); err != nil {
		t.Fatal(err)
	}
	bound, err := BindNativeHarness(root, expected, "native-current")
	if err != nil || bound.Model != "known-model" {
		t.Fatalf("legitimate startup registration not accepted: %#v %v", bound, err)
	}
	for _, field := range []string{"wbid", "generation", "runtime", "terminal", "lineage", "native", "model", "scope"} {
		t.Run(field, func(t *testing.T) {
			changed := current
			switch field {
			case "wbid":
				changed.WBSessionID = "wbs-foreign"
			case "generation":
				changed.ProcessStartedAt = changed.ProcessStartedAt.Add(-1)
			case "runtime":
				changed.Runtime = "claude-code"
			case "terminal":
				changed.Terminal.PaneID = "foreign"
			case "lineage":
				changed.HandoffID = "foreign"
			case "native":
				changed.NativeHarnessID = "foreign"
			case "model":
				expected.Model = "requested-model"
				changed.Model = "other-model"
			case "scope":
				changed.Scope = "foreign"
			}
			if nativeBindingCompatible(expected, changed, "native-current") {
				t.Fatalf("startup enrichment changed immutable %s", field)
			}
		})
	}
}

func TestExecutableProvenanceAcceptsStableAliasesAndExactCopiesOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	original := filepath.Join(root, "wb")
	copyPath := filepath.Join(root, "copy-wb")
	alias := filepath.Join(root, "alias-wb")
	data := []byte("official WB binary fixture")
	for _, path := range []string{original, copyPath} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !ExecutableIdentityMatches(original, original) || !ExecutableIdentityMatches(original, copyPath) {
		t.Fatal("same executable inode or exact official copy refused")
	}
	if err := os.Symlink(original, alias); err == nil && !ExecutableIdentityMatches(original, alias) {
		t.Fatal("same executable symlink alias refused")
	}
	if err := os.WriteFile(copyPath, []byte("untrusted WB binary bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ExecutableIdentityMatches(original, copyPath) {
		t.Fatal("different executable bytes accepted by provenance")
	}
	for _, path := range []string{"relative", root, filepath.Join(root, "missing")} {
		if ExecutableIdentityMatches(original, path) || ExecutableIdentityMatches(path, original) {
			t.Fatalf("invalid executable accepted: %s", path)
		}
	}
	if err := os.Chmod(copyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if ExecutableIdentityMatches(original, copyPath) {
		t.Fatal("non-executable provenance accepted")
	}
}
