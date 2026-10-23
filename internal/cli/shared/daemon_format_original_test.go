package shared

import "testing"

func TestDaemonJSONShortcutRejectsConflictingFormat(t *testing.T) {
	t.Parallel()
	if _, err := SelectJSONFormat("yaml", true); err == nil {
		t.Fatal("expected conflicting format to fail")
	}
	format, err := SelectJSONFormat("text", true)
	if err != nil || format != "json" {
		t.Fatalf("shortcut = %q, %v", format, err)
	}
}
