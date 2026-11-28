//go:build windows

package disk

// ownedByUser has no owner to compare on Windows; every entry counts.
func ownedByUser(path string) bool { return true }
