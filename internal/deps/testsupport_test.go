package deps

// Helpers kept for tests only: no production caller remains.

// BumpOperationID returns the stable Go campaign identity for a sorted seed
// set. Kept for backward compatibility with every caller that predates npm
// support; new callers that also know the ecosystem should use
// BumpOperationIDFor.
func BumpOperationID(events []ReleaseEvent) string {
	return BumpOperationIDFor(EcosystemGo, events)
}

// DriftFailed reports whether the complete report should exit non-zero.
func DriftFailed(report DriftReport, failOnDrift bool) bool {
	return DriftFailedWith(report, failOnDrift, false)
}
