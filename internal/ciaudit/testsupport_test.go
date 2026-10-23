package ciaudit

// Helpers kept for tests only: no production caller remains.

// WorkflowMechanisms reports which verification mechanisms one workflow
// actually runs.
//
// `batch-verification-runs-what-ci-runs` allows a local run to name a mechanism
// as skipped only after proving CI carries it. That proof has to be a read of
// the workflow, not an assumption: an unverified "CI owns it" is the
// 17-occurrence lesson reintroduced as a false assurance, which is worse than
// no gate at all.
//
// The mechanism names match what a verification run reports as skipped.
func WorkflowMechanisms(root, workflow string) (map[string]bool, error) {
	mechanisms, _, err := WorkflowMechanismsWithReuse(root, workflow)
	return mechanisms, err
}
