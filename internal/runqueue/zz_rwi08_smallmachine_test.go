package runqueue

import "testing"

// smallMachineUnits falls through to 0 for a kind none of the switch's
// cases matches (KindNone is not CPU-governed at all).
func TestRWI08SmallMachineUnitsUnmatchedKindFallsThroughToZero(t *testing.T) {
	t.Parallel()

	if got := smallMachineUnits(KindNone, 4); got != 0 {
		t.Fatalf("smallMachineUnits(KindNone, 4) = %d, want 0", got)
	}
}
