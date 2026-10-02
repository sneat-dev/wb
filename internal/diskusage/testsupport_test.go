package diskusage

import "context"

// Helpers kept for tests only: no production caller remains.

// Measure walks root without following symlinks and reports both sizes. A root
// that does not exist measures zero: an absent tree occupies nothing, and a
// caller sweeping a fleet must not fail because one path was already removed.
// Unreadable subdirectories are skipped rather than fatal, for the same reason.
func Measure(ctx context.Context, root string) (Usage, error) {
	usage, _, err := measure(ctx, root)
	return usage, err
}
