//go:build !darwin

package daemonruntime

import "time"

func runNativeLaunchctl(bound time.Duration, args ...string) ([]byte, error) {
	return runBoundedNativeCommand("launchctl", bound, args...)
}
