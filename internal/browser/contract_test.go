package browser

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBrowserOpenerPreservesResolutionPlatformOrderAndErrorIdentity(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"web", "relative", "invalid-web-falls-back", "resolve-refusal", "unsupported", "start-refusal"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			boom := errors.New("native operation refusal")
			target := "https://example.test/path?unchanged=yes"
			if stage == "relative" || stage == "resolve-refusal" {
				target = "relative/report.html"
			}
			if stage == "invalid-web-falls-back" {
				target = "http://%zz/"
			}
			goos := "darwin"
			if stage == "unsupported" {
				goos = "plan9"
			}
			calls := []string{}
			instance := opener{goos: goos, absolute: func(got string) (string, error) {
				calls = append(calls, "absolute")
				if got != target {
					t.Fatal(got)
				}
				if stage == "resolve-refusal" {
					return "", boom
				}
				return "/private/resolved/report.html", nil
			}, start: func(name string, args []string) error {
				calls = append(calls, "start")
				expected := target
				if stage == "relative" || stage == "invalid-web-falls-back" {
					expected = "/private/resolved/report.html"
				}
				if name != "open" || !reflect.DeepEqual(args, []string{expected}) {
					t.Fatalf("name=%q args=%v", name, args)
				}
				if stage == "start-refusal" {
					return boom
				}
				return nil
			}}
			err := instance.open(target)
			switch stage {
			case "resolve-refusal":
				if !errors.Is(err, boom) || !reflect.DeepEqual(calls, []string{"absolute"}) {
					t.Fatalf("err=%v calls=%v", err, calls)
				}
			case "unsupported":
				if err == nil || err.Error() != "opening a browser is unsupported on plan9" || len(calls) != 0 {
					t.Fatalf("err=%v calls=%v", err, calls)
				}
			case "start-refusal":
				if !errors.Is(err, boom) || !reflect.DeepEqual(calls, []string{"start"}) {
					t.Fatalf("err=%v calls=%v", err, calls)
				}
			default:
				expected := []string{"start"}
				if stage != "web" {
					expected = []string{"absolute", "start"}
				}
				if err != nil || !reflect.DeepEqual(calls, expected) {
					t.Fatalf("err=%v calls=%v", err, calls)
				}
			}
		})
	}
}

func TestBrowserOpenerForwardsEveryPlatformArgumentWithoutShellInterpretation(t *testing.T) {
	t.Parallel()
	target := "https://example.test/?literal=$(do-not-execute)&query=hello%20world"
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()
			called := false
			instance := opener{goos: goos, absolute: func(string) (string, error) { t.Fatal("valid web URL unexpectedly resolved as a file"); return "", nil }, start: func(name string, args []string) error {
				called = true
				expectedName := "open"
				expectedArgs := []string{target}
				if goos == "linux" {
					expectedName = "xdg-open"
				}
				if goos == "windows" {
					expectedName = "rundll32"
					expectedArgs = []string{"url.dll,FileProtocolHandler", target}
				}
				if name != expectedName || !reflect.DeepEqual(args, expectedArgs) {
					t.Fatalf("name=%q args=%v", name, args)
				}
				return nil
			}}
			if err := instance.open(target); err != nil || !called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

//nolint:paralleltest // genuine native launcher discovery deliberately mutates process-wide PATH; lookup refuses before any browser process starts.
func TestBrowserNativeOpenRefusesAMissingLauncherWithoutLaunchingAProcess(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	target := filepath.Join(t.TempDir(), "private-report.html")
	err := Open(target)
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("native launcher error=%v", err)
	}
}
