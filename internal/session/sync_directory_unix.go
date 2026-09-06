//go:build !windows

package session

import "os"

func syncDirectory(directory *os.File) error { return directory.Sync() }
