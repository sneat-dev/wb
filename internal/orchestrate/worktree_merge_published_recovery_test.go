package orchestrate

import (
	"errors"
	"strings"
	"testing"
)

func TestPublishedRecoveryDriftErrorsPreserveCauseAndTrimNotes(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("original drift")
	for _, tc := range []struct{ note, want string }{{"", "original drift"}, {" \t ", "original drift"}, {"  prior local failure \n", "original drift (prior local failure)"}} {
		t.Run(tc.want+tc.note, func(t *testing.T) {
			t.Parallel()
			receipt := WorktreeMergeReceipt{LocalSync: tc.note}
			err := worktreeMergeDriftError(&receipt, sentinel)
			if !errors.Is(err, sentinel) || err.Error() != tc.want || receipt.LocalSync != tc.note {
				t.Fatalf("drift identity=%v note=%q", err, receipt.LocalSync)
			}
			if strings.TrimSpace(tc.note) == "" && err != sentinel {
				t.Fatal("empty note replaced error identity")
			}
		})
	}
}
