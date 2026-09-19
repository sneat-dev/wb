package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCall records one recorded invocation and the canned response to
// return for it.
type fakeCall struct {
	stdout []byte
	stderr []byte
	err    error
}

// fakeRunner is an in-memory [Runner]. Tests register a response per exact
// argv (joined with a single space, which is safe here because no test
// argument in this package contains a literal space); an unregistered argv
// fails the test loudly instead of silently returning zero values, so a
// missing fixture is never confused with "herdr returned nothing".
type fakeRunner struct {
	t         *testing.T
	responses map[string]fakeCall
	calls     [][]string
	envs      [][]string
}

func newFakeRunner(t *testing.T) *fakeRunner {
	t.Helper()
	return &fakeRunner{t: t, responses: map[string]fakeCall{}}
}

func (f *fakeRunner) on(args []string, call fakeCall) *fakeRunner {
	f.responses[strings.Join(args, " ")] = call
	return f
}

func (f *fakeRunner) Run(_ context.Context, binary string, args []string, env []string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	f.envs = append(f.envs, append([]string(nil), env...))
	key := strings.Join(args, " ")
	call, ok := f.responses[key]
	if !ok {
		f.t.Fatalf("fakeRunner: no response registered for %q %q", binary, args)
	}
	return call.stdout, call.stderr, call.err
}

// mustReadTestdata reads a fixture file relative to internal/herdr/testdata
// and fails the test if it cannot.
func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return data
}

// lookupFromMap returns an [EnvLookup] backed by a plain map, so identity
// and client tests never read the real process environment.
func lookupFromMap(values map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
