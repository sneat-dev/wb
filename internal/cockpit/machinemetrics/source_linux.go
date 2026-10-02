package machinemetrics

import "os"

// NewSource reads /proc and the disk of root.
func NewSource(root string) Source {
	return &procSource{readFile: os.ReadFile, disk: func() (uint64, uint64, error) { return diskUsage(root) }}
}
