package prwatch

import (
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"strings"
	"testing"
)

func TestClassifyChecksExplainsUnregisteredRequiredChecks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		snapshot prsnapshot.Snapshot
		want     string
	}{
		{name: "required check absent", snapshot: prsnapshot.Snapshot{Blocked: []string{"build", "coverage"}}, want: "required check(s) have not registered for this head: build, coverage"},
		{name: "no observed checks", snapshot: prsnapshot.Snapshot{}, want: "no GitHub checks have registered for this head yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind, reason := classifyChecks(tc.snapshot)
			if kind != KindChecksPending || !strings.Contains(reason, tc.want) {
				t.Fatalf("classification=%q,%q", kind, reason)
			}
		})
	}
}
