package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlPathsRetainAbsoluteResolutionErrorsBeforeTrustChecks(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"checkout", "control"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			dispatcher, checkout := hkCovEnv(t)
			failure := errors.New("absolute coordinate unavailable")
			calls := 0
			err := dispatcher.validateControlPathsWithAbsolute([]Event{hkCovEvent(checkout)}, func(path string) (string, error) {
				calls++
				if phase == "checkout" && calls == 1 || phase == "control" && calls == 2 {
					return "", failure
				}
				return filepath.Abs(path)
			})
			wantCalls := 1
			if phase == "control" {
				wantCalls = 2
			}
			if !errors.Is(err, failure) || calls != wantCalls {
				t.Fatalf("resolution=%v calls=%d", err, calls)
			}
		})
	}
}

func TestCheckoutVerificationRetainsAbsoluteAndPostResolutionStatErrors(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"absolute", "stat"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			checkout := filepath.Join(t.TempDir(), "checkout")
			if err := os.Mkdir(checkout, 0700); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("absolute coordinate unavailable")
			absolute := filepath.Abs
			if phase == "absolute" {
				absolute = func(string) (string, error) { return "", failure }
			}
			inspectCalls := 0
			physical, info, err := verifyCheckoutWithPaths(Event{Checkout: checkout}, absolute, func(path string) (os.FileInfo, error) {
				inspectCalls++
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				return os.Stat(path)
			})
			if physical != "" || info != nil || err == nil {
				t.Fatalf("failed checkout admitted=%q %v %v", physical, info, err)
			}
			if phase == "absolute" {
				if !errors.Is(err, failure) || inspectCalls != 0 {
					t.Fatalf("absolute cause=%v calls=%d", err, inspectCalls)
				}
			} else if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "inspect checkout") || inspectCalls != 1 {
				t.Fatalf("stat cause=%v calls=%d", err, inspectCalls)
			}
		})
	}
}

func TestDispatcherDefaultsProvideNativeCheckoutVerifier(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	dispatcher.VerifyCheckout = nil
	effective := dispatcher.defaults()
	if effective.VerifyCheckout == nil {
		t.Fatal("native verifier missing")
	}
	_, _, err := effective.VerifyCheckout(Event{Checkout: filepath.Join(t.TempDir(), "missing")})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native verifier cause=%v", err)
	}
}
