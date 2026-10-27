package worktrees

import (
	"errors"
	"fmt"
	"os"
)

// Only this aggregate identifies genuine absence in all sanctioned homes.
// A missing journal component after a claim was found is not claim absence.
var errRelocationClaimAbsent = errors.New("no projected Work Log claim in sanctioned homes")

func projectedClaimInHome(home string, projection workLogProjection) (workLogClaim, bool, error) {
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if errors.Is(err, os.ErrNotExist) {
		return workLogClaim{}, false, nil
	}
	if err != nil {
		return workLogClaim{}, false, err
	}
	defer func() { _ = run.Close() }()
	claim, err := readWorkLogClaimAt(run, projection.ClaimID)
	if errors.Is(err, os.ErrNotExist) {
		return workLogClaim{}, false, nil
	}
	if err != nil {
		return claim, false, err
	}
	if err = validateStaticWorkLogClaim(claim, projection.EffortID, projection.RunID); err != nil {
		return claim, false, err
	}
	if claim.ClaimID != projection.ClaimID {
		return claim, false, fmt.Errorf("projected Work Log claim ID does not match immutable record")
	}
	return claim, true, nil
}
func absentRelocationClaim() error {
	return fmt.Errorf("%w: %w", errRelocationClaimAbsent, os.ErrNotExist)
}
