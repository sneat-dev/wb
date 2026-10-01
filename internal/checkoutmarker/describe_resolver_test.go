package checkoutmarker

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDescribePathResolutionFailurePreservesCauseAndStopsInspection(t *testing.T) {
	t.Parallel()
	want := errors.New("working directory unavailable")
	calls := 0
	got, err := describeWithPathResolver("relative/checkout", DescribeOptions{
		Now: func() time.Time {
			t.Fatal("inspection requested a timestamp after path resolution failed")
			return time.Time{}
		},
	}, func(path string) (string, error) {
		calls++
		if path != "relative/checkout" {
			t.Fatalf("resolver path = %q", path)
		}
		return "", want
	})
	if calls != 1 || !errors.Is(err, want) || !strings.Contains(err.Error(), "resolve relative/checkout:") {
		t.Fatalf("resolution failure = %v, calls = %d", err, calls)
	}
	if got != (Inspection{}) {
		t.Fatalf("failed resolution returned an inspection: %+v", got)
	}
}
