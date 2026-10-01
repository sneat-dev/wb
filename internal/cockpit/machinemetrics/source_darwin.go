package machinemetrics

import "golang.org/x/sys/unix"

// NewSource reads the kernel's sysctls and the disk of root, without cgo.
func NewSource(root string) Source {
	return &sysctlSource{
		uint64Of: func(name string) (uint64, error) {
			if name == "vm.pagesize" {
				value, err := unix.SysctlUint32(name)
				return uint64(value), err
			}
			if name == "hw.memsize" {
				return unix.SysctlUint64(name)
			}
			value, err := unix.SysctlUint32(name)
			return uint64(value), err
		},
		raw:  func(name string) ([]byte, error) { return unix.SysctlRaw(name) },
		disk: func() (uint64, uint64, error) { return diskUsage(root) },
	}
}
