package agents

import (
	"fmt"
	"time"
)

// A contained execution has one finite total budget with four explicit parts:
// the setup pool (everything before the harness starts), the harness run bound,
// the configured validation steps and the cleanup pool. The total is what the
// container's own PID1 kernel timeout receives, so it must cover every part;
// an under-counted total would silently bound the run below its declared
// timeout when a legitimate setup phase is slow.
const (
	// containerSetupBudget is the finite pool a contained execution may spend
	// before its harness starts. It covers the verified runtime open (Docker
	// client/daemon resolution, prior-reservation recovery, image inspection,
	// container create, start wait and cgroup verification), the unprivileged
	// worker-home preparation and the two bounded in-container probes. The pool
	// is one explicit constant rather than an optimistic hand-count of the
	// current steps, and every setup phase runs under a context bounded by it,
	// so a slow but legitimate host is not cut off and a hang cannot borrow the
	// declared harness or validation time.
	containerSetupBudget = 10 * time.Minute
	// containerCleanupBudget is the finite pool Close may spend proving the
	// namespace absent after the owner's own lifetime context has expired:
	// the attach reap, the forced kill and the daemon-bound removal. Close opens
	// its own context bounded by containerCleanupTimeout; this pool also
	// accounts for the bounded reap that precedes it.
	containerCleanupBudget = containerCleanupTimeout +
		containerAttachReapTimeout +
		containerAttachKillTimeout
)

// ContainedLifetime returns the finite maximum wall time a contained execution
// may occupy: the setup pool, the run bound, every validation step's
// finite/default bound and the cleanup pool. It refuses an unbounded,
// overflowing or above-maximum total rather than rounding or silently shrinking
// any part. The addition is overflow-safe by construction: the maximum is
// subtracted as each component is admitted, so no intermediate or final value
// can exceed maxContainerLifetime. The setup and cleanup pools are fixed
// constants validated by test to stay below that maximum.
//
// This is the total-vs-stage contract: the returned value is the whole
// execution's outer deadline and the container's kernel timeout, not the
// harness stage alone. Legacy host timeout semantics are untouched.
func ContainedLifetime(runTimeoutMS int64, steps []ValidationStep) (time.Duration, error) {
	runTimeout, err := containedRunTimeout(runTimeoutMS)
	if err != nil {
		return 0, err
	}
	remaining := maxContainerLifetime - containerSetupBudget - containerCleanupBudget
	if runTimeout > remaining {
		return 0, fmt.Errorf("contained execution run timeout %s leaves no room for the %s setup and %s cleanup budgets within the maximum %s", runTimeout, containerSetupBudget, containerCleanupBudget, maxContainerLifetime)
	}
	remaining -= runTimeout
	var validation time.Duration
	// Index rather than range: the step number is the actionable part of the
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
	// Every part above was admitted against the remaining maximum, so this sum
	// cannot exceed maxContainerLifetime or overflow.
	return containerSetupBudget + runTimeout + validation + containerCleanupBudget, nil
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
