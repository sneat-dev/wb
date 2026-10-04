package browser

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Open starts the platform browser launcher asynchronously, preserving native errors.
func Open(target string) error {
	return opener{goos: runtime.GOOS, absolute: filepath.Abs, start: startBrowserCommand}.open(target)
}

type opener struct {
	goos     string
	absolute func(string) (string, error)
	start    func(string, []string) error
}

func startBrowserCommand(name string, args []string) error {
	return exec.Command(name, args...).Start()
}
func (instance opener) open(target string) error {
	resolved, err := browserTarget(target, instance.absolute)
	if err != nil {
		return err
	}
	name, args, err := browserCommand(instance.goos, resolved)
	if err != nil {
		return err
	}
	return instance.start(name, args)
}
func browserTarget(target string, absolute func(string) (string, error)) (string, error) {
	parsed, err := url.Parse(target)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		return target, nil
	}
	return absolute(target)
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
