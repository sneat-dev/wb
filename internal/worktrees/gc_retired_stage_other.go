//go:build !darwin && !linux

package worktrees

// Other platforms retain the previously planned-only behavior until they have
// an equivalent descriptor-bound directory removal proof.
func retireEmptyUnscopedLocalStages([]LifecycleArtifact) {}
