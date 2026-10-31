package sessionauthority

import "testing"

func TestSelectedModelKeepsExplicitSelectionAcrossHarnessChanges(t *testing.T) {
	for _, test := range []struct {
		name, source, sourceModel, target, requested, want string
	}{
		{"same runtime explicit", "codex", "old", "codex", " new ", "new"},
		{"cross runtime explicit", "codex", "old", "claude-code", "new", "new"},
		{"same runtime fallback", " codex ", " old ", "codex", " ", "old"},
		{"cross runtime unspecified", "codex", "old", "claude-code", "", ""},
		{"same runtime unknown", "codex", "unknown", "codex", "", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SelectedModel(test.source, test.sourceModel, test.target, test.requested); got != test.want {
				t.Fatalf("selected model = %q, want %q", got, test.want)
			}
		})
	}
}
