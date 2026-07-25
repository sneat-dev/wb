package console

import (
	"bytes"
	"os"
	"testing"
)

func TestIsTerminalRejectsEverythingThatIsNotATerminal(t *testing.T) {
	t.Parallel()

	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = pipeReader.Close(); _ = pipeWriter.Close() }()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	regular, err := os.CreateTemp(t.TempDir(), "console-")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer func() { _ = regular.Close() }()

	var nilFile *os.File
	tests := map[string]any{
		"pipe read end":  pipeReader,
		"pipe write end": pipeWriter,
		"dev null":       devNull,
		"regular file":   regular,
		"bytes buffer":   &bytes.Buffer{},
		"nil interface":  nil,
		"typed nil file": nilFile,
	}
	for name, stream := range tests {
		if IsTerminal(stream) {
			t.Errorf("IsTerminal(%s) = true, want false", name)
		}
	}
}

// TestIsTerminalAcceptsARealTerminal is the positive half of the contract. It
// can only run where a controlling terminal exists, so it skips rather than
// weakening the negative assertions above.
func TestIsTerminalAcceptsARealTerminal(t *testing.T) {
	t.Parallel()
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skipf("no controlling terminal available: %v", err)
	}
	defer func() { _ = tty.Close() }()
	if !IsTerminal(tty) {
		t.Error("IsTerminal(/dev/tty) = false, want true")
	}
}

func TestDisabledReadsTheEnvironmentAsAnOptOut(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"yes":   true, // anything unparseable still means "do not be interactive"
	}
	for value, want := range tests {
		if got := disabled(value); got != want {
			t.Errorf("disabled(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestInteractiveRefusesWhenForcedOffEvenOnATerminal(t *testing.T) {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skipf("no controlling terminal available: %v", err)
	}
	defer func() { _ = tty.Close() }()

	t.Setenv(EnvDisable, "")
	if !Interactive(tty, false) {
		t.Error("Interactive(terminal, forced=false) = false, want true")
	}
	if Interactive(tty, true) {
		t.Error("--non-interactive did not suppress the terminal UI")
	}
	t.Setenv(EnvDisable, "1")
	if Interactive(tty, false) {
		t.Errorf("%s=1 did not suppress the terminal UI", EnvDisable)
	}
}

func TestInteractiveIsAlwaysFalseWithoutATerminal(t *testing.T) {
	t.Parallel()
	if Interactive(&bytes.Buffer{}, false) {
		t.Error("Interactive(non-terminal) = true, want false")
	}
}
