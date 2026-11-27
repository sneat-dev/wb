//go:build windows

package diskprune

// LockDir is unsupported on Windows, where prune deletes nothing.
func LockDir(path string) (*Lock, error) { return nil, ErrUnsupported }

// LockDirs is unsupported on Windows, where prune deletes nothing.
func LockDirs(paths []string) (*Lock, error) { return nil, ErrUnsupported }

// LockModuleVersion is unsupported on Windows, where prune deletes nothing.
func LockModuleVersion(path string) (func(), error) { return nil, ErrUnsupported }
