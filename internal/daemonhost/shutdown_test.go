package daemonhost

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestClassifyServeResultNormalizesEveryCleanShutdownOutcome(t *testing.T) {
	t.Parallel()
	realErr := errors.New("listener accept failed")
	cases := map[string]struct {
		in   error
		want error
	}{
		"nil, as fileBridge.Serve reports on ctx.Done":                 {in: nil, want: errCleanDaemonShutdown},
		"http.ErrServerClosed, as server.Serve reports after Shutdown": {in: http.ErrServerClosed, want: errCleanDaemonShutdown},
		"net.ErrClosed, as Serve reports after listener.Close":         {in: net.ErrClosed, want: errCleanDaemonShutdown},
		"a real serve error passes through unchanged":                  {in: realErr, want: realErr},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := classifyServeResult(testCase.in)
			if !errors.Is(got, testCase.want) || (testCase.want == realErr && got != realErr) {
				t.Fatalf("classifyServeResult(%v) = %v, want %v", testCase.in, got, testCase.want)
			}
		})
	}
}

func TestAwaitDaemonServeResultReducesTheClassifiedResult(t *testing.T) {
	t.Parallel()
	if err := awaitDaemonServeResult(classifyServeResult(nil)); err != nil {
		t.Fatalf("clean shutdown (nil) = %v, want nil", err)
	}
	if err := awaitDaemonServeResult(classifyServeResult(http.ErrServerClosed)); err != nil {
		t.Fatalf("clean shutdown (http.ErrServerClosed) = %v, want nil", err)
	}
	real := errors.New("daemon file bridge request backlog exceeds 1024 entries")
	if err := awaitDaemonServeResult(classifyServeResult(real)); !errors.Is(err, real) {
		t.Fatalf("real error = %v, want %v", err, real)
	}
}
