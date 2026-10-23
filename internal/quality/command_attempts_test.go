package quality

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCommandAttemptsPreserveRetryCancellationAndInfrastructure(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation failed")
	for _, tc := range []struct {
		name                     string
		retry, failures          int
		canceled, infrastructure bool
		want                     int
	}{
		{"success", 0, 0, false, false, 1}, {"retry", 1, 1, false, false, 2}, {"exhausted", 1, 4, false, false, 2}, {"canceled", 3, 4, true, false, 1}, {"infrastructure", 3, 4, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			calls := 0
			out, count, err := runCommandAttempts(ctx, 0, tc.retry, func(context.Context) (string, error) {
				calls++
				if calls <= tc.failures {
					if tc.infrastructure {
						return "output", &nativeCoverageError{boom}
					}
					return "output", boom
				}
				return "output", nil
			})
			if out != "output" || count != tc.want || calls != tc.want {
				t.Fatalf("retry contract %q count%d calls%d", out, count, calls)
			}
			if tc.failures < tc.want {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, boom) {
				t.Fatalf("error identity %v", err)
			}
		})
	}
	for _, infrastructure := range []bool{false, true} {
		t.Run("deadline"+map[bool]string{false: "ordinary", true: "native"}[infrastructure], func(t *testing.T) {
			t.Parallel()
			_, count, err := runCommandAttempts(context.Background(), time.Millisecond, 0, func(ctx context.Context) (string, error) {
				<-ctx.Done()
				if infrastructure {
					return "", &nativeCoverageError{ctx.Err()}
				}
				return "", ctx.Err()
			})
			if count != 1 || err == nil || !strings.Contains(err.Error(), "timed out after") || isNativeCoverageFailure(err) != infrastructure {
				t.Fatalf("deadline %d %v", count, err)
			}
		})
	}
	wrapped := preserveNativeCoverageFailure(&nativeCoverageError{boom}, context.Canceled)
	if !isNativeCoverageFailure(wrapped) || !errors.Is(wrapped, boom) || !errors.Is(wrapped, context.Canceled) {
		t.Fatalf("classification lost %v", wrapped)
	}
	if preserveNativeCoverageFailure(boom, context.Canceled) != context.Canceled {
		t.Fatal("ordinary error changed")
	}
}
