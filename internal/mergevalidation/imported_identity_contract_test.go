package mergevalidation

import (
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestImportedMainDeadcodeIdentitiesRetainCompleteEvidence(t *testing.T) {
	t.Parallel()
	failed := deadcodeFailureReport(DeadcodeCommand, "native tool wire contract", "main.A", "main.B")
	if _, valid := importedMainDeadcodeIdentities(failed, quality.CheckLint, DeadcodeCommand, "."); !valid {
		t.Fatal("complete exact identity set lost")
	}
}
