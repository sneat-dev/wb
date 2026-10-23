package runner

import (
	"io"
	"os/exec"
	"testing"
)

func TestConfigureOutputCapturePreservesAlternatingWrites(t *testing.T) {
	t.Parallel()
	command := &exec.Cmd{}
	capture := configureOutputCapture(command, true)
	if command.Stdout != command.Stderr {
		t.Fatal("combined capture must attach the same writer to both streams")
	}
	for _, write := range []struct {
		stream io.Writer
		text   string
	}{
		{command.Stdout, "out-1\n"},
		{command.Stderr, "err-1\n"},
		{command.Stdout, "out-2\n"},
		{command.Stderr, "err-2\n"},
	} {
		if _, err := io.WriteString(write.stream, write.text); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := capture.combined.String(), "out-1\nerr-1\nout-2\nerr-2\n"; got != want {
		t.Fatalf("combined output = %q, want %q", got, want)
	}
	if capture.stdout.Len() != 0 || capture.stderr.Len() != 0 {
		t.Fatalf("separate buffers populated: stdout=%q stderr=%q", capture.stdout.String(), capture.stderr.String())
	}
}

func TestConfigureOutputCaptureKeepsSeparateStreamsByDefault(t *testing.T) {
	t.Parallel()
	command := &exec.Cmd{}
	capture := configureOutputCapture(command, false)
	if command.Stdout == command.Stderr {
		t.Fatal("default capture must use separate writers")
	}
	if _, err := io.WriteString(command.Stdout, "out\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(command.Stderr, "err\n"); err != nil {
		t.Fatal(err)
	}
	if capture.stdout.String() != "out\n" || capture.stderr.String() != "err\n" || capture.combined.Len() != 0 {
		t.Fatalf("separate capture: stdout=%q stderr=%q combined=%q", capture.stdout.String(), capture.stderr.String(), capture.combined.String())
	}
}
