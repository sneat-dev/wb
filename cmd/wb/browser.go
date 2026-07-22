package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

func openBrowser(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	name, args, err := browserCommand(runtime.GOOS, absolute)
	if err != nil {
		return err
	}
	return exec.Command(name, args...).Start()
}

func browserCommand(goos, path string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{path}, nil
	case "linux":
		return "xdg-open", []string{path}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", path}, nil
	default:
		return "", nil, fmt.Errorf("opening a browser is unsupported on %s", goos)
	}
}
