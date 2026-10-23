//go:build darwin

package daemonruntime

import (
	"os"
	"testing"
	"time"
)

func runNativeLaunchctl(bound time.Duration, args ...string) ([]byte, error) {
	return runNativeLaunchctlForMode(bound, testing.Testing(), os.Executable, args...)
}

func runNativeLaunchctlForMode(bound time.Duration, isTestProcess bool, executablePath func() (string, error), args ...string) ([]byte, error) {
	executable, err := executablePath()
	if err != nil {
		executable = os.Args[0]
	}
	if err := daemonRefuseTestBinaryForMode(executable, isTestProcess); err != nil {
		return nil, err
	}
	return runBoundedNativeCommand("launchctl", bound, args...)
}
