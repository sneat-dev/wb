package hostload

import (
	"context"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // withConsumerRunner replaces package-global consumerRunner and consumersTimeout until cleanup.
func TestCheckIncludesBoundedConsumerDiagnostics(t *testing.T) {
	withConsumerRunner(t, time.Second, func(context.Context) ([]byte, error) {
		return []byte("PID %CPU ELAPSED COMM\n42 80.0 00:10 go secret-token\n"), nil
	})
	err := Check(func() (float64, error) { return 9, nil }, 4, false)
	if err == nil || !strings.Contains(err.Error(), "pid=42 cpu=80.0% etime=00:10 comm=go") {
		t.Fatalf("refusal lacks consumer diagnostic: %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("refusal exposed argv: %v", err)
	}
}
