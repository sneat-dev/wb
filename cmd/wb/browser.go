package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"

	"github.com/sneat-dev/wb/internal/runner"
)

// openBrowser starts the platform's browser/URL opener for target through r
// and does not wait for it: it outlives this call exactly as the original
// exec.Command(...).Start() (never Wait()) did.
func openBrowser(r runner.Runner, target string) error {
	resolved, err := browserTarget(target)
	if err != nil {
		return err
	}
	name, args, err := browserCommand(runtime.GOOS, resolved)
	if err != nil {
		return err
	}
	_, err = r.Detach("", name, args...)
	return err
}

func browserTarget(target string) (string, error) {
	parsed, err := url.Parse(target)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		return target, nil
	}
	return filepath.Abs(target)
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
