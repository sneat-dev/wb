package browser

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserOriginalTargetAndPlatformSelection(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"https://example.test/x", "http://example.test"} {
		resolved, err := browserTarget(target, filepath.Abs)
		if err != nil || resolved != target {
			t.Errorf("browserTarget(%q) = (%q, %v), want it unchanged", target, resolved, err)
		}
	}
	relative, err := browserTarget("some/relative/path", filepath.Abs)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(relative) {
		t.Errorf("browserTarget(relative) = %q, want an absolute path", relative)
	}

	darwinName, darwinArgs, err := browserCommand("darwin", "/x")
	if err != nil || darwinName != "open" || len(darwinArgs) != 1 {
		t.Errorf("darwin browser = (%q, %v, %v)", darwinName, darwinArgs, err)
	}
	linuxName, _, err := browserCommand("linux", "/x")
	if err != nil || linuxName != "xdg-open" {
		t.Errorf("linux browser = (%q, %v)", linuxName, err)
	}
	windowsName, windowsArgs, err := browserCommand("windows", "/x")
	if err != nil || windowsName != "rundll32" || len(windowsArgs) != 2 {
		t.Errorf("windows browser = (%q, %v, %v)", windowsName, windowsArgs, err)
	}
	if _, _, err := browserCommand("plan9", "/x"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unsupported GOOS error = %v", err)
	}

}
