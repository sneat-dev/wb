package shared

import (
	"bytes"
	"errors"
	"testing"
)

type argumentWriterError struct{ err error }

func (w argumentWriterError) Write([]byte) (int, error) { return 0, w.err }
func TestArgumentOrCurrentPreservesEmptyAndMultipleSemantics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{{nil, "."}, {[]string{}, "."}, {[]string{""}, ""}, {[]string{"/tmp/repo"}, "/tmp/repo"}, {[]string{"one", "two"}, "."}} {
		if got := ArgumentOrCurrent(tc.args); got != tc.want {
			t.Fatalf("args=%v result=%q want=%q", tc.args, got, tc.want)
		}
	}
}
func TestSimpleWritersPreserveBytesAndErrors(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := WriteLine(&out, "a", 2); err != nil {
		t.Fatal(err)
	}
	if err := WriteFormat(&out, "[%s]", "value"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a 2\n[value]" {
		t.Fatal(out.String())
	}
	sentinel := errors.New("writer failed")
	if err := WriteLine(argumentWriterError{sentinel}, "a"); err != sentinel {
		t.Fatal(err)
	}
	if err := WriteFormat(argumentWriterError{sentinel}, "%s", "a"); err != sentinel {
		t.Fatal(err)
	}
}
