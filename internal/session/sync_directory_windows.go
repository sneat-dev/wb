//go:build windows

package session

import "os"

func syncDirectory(*os.File) error { return nil }
