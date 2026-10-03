package claudesettings

import (
	"testing"
) // TestAgentHookEntryPresentRejectsNonObjectEntry proves the top-level
// `entry.(map[string]any)` !ok branch returns false rather than panicking.
func TestAgentHookEntryPresentRejectsNonObjectEntry(t *testing.T) {
	t.Parallel()
	if EntryPresent("not-an-object", "wb hooks agent guard") {
		t.Fatal("want false for a non-object entry")
	}
} // TestAgentHookEntryPresentSkipsNonObjectHandlers proves a non-object
// handler element is skipped (continue), while a sibling matching handler
// still yields true.
func TestAgentHookEntryPresentSkipsNonObjectHandlers(t *testing.T) {
	t.Parallel()
	entry := map[string]any{
		"hooks": []any{
			"not-an-object-handler",
			map[string]any{"command": "wb hooks agent guard"},
		},
	}
	if !EntryPresent(entry, "wb hooks agent guard") {
		t.Fatal("want true: a later valid handler entry should still match")
	}
}
