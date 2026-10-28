package agents

import (
	"fmt"
	"time"
)

// containerSetupCleanupAllowance is the fixed overhead a contained execution may
// spend outside the run bound and validation steps: Docker metadata probes, the
// container start wait, cleanup, and the bounded attach reap. It is derived from
// the same finite constants the runtime enforces rather than being a second,
// unrelated guess.
const containerSetupCleanupAllowance = containerMetadataTimeout +
	containerStartTimeout +
	containerCleanupTimeout +
	containerAttachReapTimeout +
	containerAttachKillTimeout

// ContainedLifetime returns the finite maximum wall time a contained execution
// may occupy: the run bound, every validation step's finite/default bound, and
// the fixed setup/cleanup allowance. It refuses an unbounded, overflowing or
// above-maximum total rather than rounding or silently shrinking any part.
//
// The addition is overflow-safe by construction: the maximum is subtracted as
// each component is admitted, so no intermediate value can exceed
// maxContainerLifetime. Legacy host timeout semantics are untouched; this helper
// is only for a contained owner to use when it is wired in a later stage.
func ContainedLifetime(runTimeoutMS int64, steps []ValidationStep) (time.Duration, error) {
	runTimeout, err := containedRunTimeout(runTimeoutMS)
	if err != nil {
		return 0, err
	}
	remaining := maxContainerLifetime
	if runTimeout > remaining-containerSetupCleanupAllowance {
		return 0, fmt.Errorf("contained execution run timeout %s leaves no room for the %s setup/cleanup allowance within the maximum %s", runTimeout, containerSetupCleanupAllowance, maxContainerLifetime)
	}
	remaining -= runTimeout
	remaining -= containerSetupCleanupAllowance
	var validation time.Duration
	// Index rather than range: the step number is the actionable part of a
	// refusal, and a step name may be empty.
	for index, step := range steps {
		timeout, timeoutErr := parseStepTimeout(step.Timeout)
		if timeoutErr != nil {
			return 0, fmt.Errorf("contained execution validation step %d %q: %w", index+1, step.Name, timeoutErr)
		}
		if timeout <= 0 {
			timeout = defaultValidationStepTimeout
		}
		if timeout > remaining {
			return 0, fmt.Errorf("contained execution validation step %d %q needs %s, which exceeds the %s left within the maximum %s", index+1, step.Name, timeout, remaining, maxContainerLifetime)
		}
		remaining -= timeout
		validation += timeout
	}
	return runTimeout + validation + containerSetupCleanupAllowance, nil
}

// ContainedRecordLifetime applies ContainedLifetime to an admitted record's
// persisted run bound and snapshotted validation steps. It is the convenience
// entry point a contained owner uses so the same finite bound is computed from
// exactly what the record carries.
func ContainedRecordLifetime(record Record) (time.Duration, error) {
	return ContainedLifetime(record.TimeoutMS, validationStepsFromRecord(record))
}

// containedRunTimeout converts the persisted millisecond bound to a duration
// without overflow. The representable-duration check comes first so an
// adversarial value such as MaxInt64 is refused as unrepresentable rather than
// wrapping into a negative or tiny lifetime.
func containedRunTimeout(runTimeoutMS int64) (time.Duration, error) {
	if runTimeoutMS <= 0 {
		return 0, fmt.Errorf("contained execution requires a positive run timeout, got %d ms; an unbounded run is refused", runTimeoutMS)
	}
	const maxDuration = time.Duration(1<<63 - 1)
	maxMilliseconds := int64(maxDuration / time.Millisecond)
	if runTimeoutMS > maxMilliseconds {
		return 0, fmt.Errorf("contained execution run timeout %d ms cannot be represented as a Go duration without overflow", runTimeoutMS)
	}
	return time.Duration(runTimeoutMS) * time.Millisecond, nil
}
