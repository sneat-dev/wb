//go:build !linux

package diskprune

// scanTree needs mount IDs and status-change times this platform does not
// give the same way; trees are never pruned here, so every scan is partial.
func scanTree(root string, mount uint64, protected []string) scanResult {
	return scanResult{Partial: true}
}

func scanTreeWithIDs(ids map[[2]uint64]string, stop func() bool) func(string, uint64, []string) scanResult {
	return scanTree
}
