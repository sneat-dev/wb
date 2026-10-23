package daemonruntime

import (
	"errors"
	"testing"
)

func TestFileBridgeReportRetainsFirstErrorAndDropsOnlyOverflow(t *testing.T) {
	t.Parallel()
	first, second, third := errors.New("first"), errors.New("second"), errors.New("third")
	server := FileBridgeServer{errors: make(chan error, 1)}
	server.report(first)
	server.report(second)
	if got := <-server.errors; got != first {
		t.Fatalf("retained error=%v want first", got)
	}
	select {
	case got := <-server.errors:
		t.Fatalf("overflow was delivered: %v", got)
	default:
	}
	server.report(third)
	if got := <-server.errors; got != third {
		t.Fatalf("next error=%v want third", got)
	}
}
