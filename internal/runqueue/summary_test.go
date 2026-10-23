package runqueue

import "testing"

func TestSummaryKeepsTelemetryPrivate(t *testing.T) {
	t.Parallel()
	if got := Summary(nil); got != "unknown" {
		t.Errorf("Summary(nil) = %q", got)
	}
	if got := Summary([]string{"/usr/local/bin/go", "test", "./..."}); got != "go test" {
		t.Errorf("Summary(go test) = %q", got)
	}
	if got := Summary([]string{"go", "-race", "test"}); got != "go test" {
		t.Errorf("Summary(skipping flags) = %q", got)
	}
	if got := Summary([]string{"/usr/bin/make"}); got != "make" {
		t.Errorf("Summary(makeless) = %q", got)
	}
}
