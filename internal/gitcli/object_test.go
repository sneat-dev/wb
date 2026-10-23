package gitcli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeObjectCommand struct {
	pipe     io.ReadCloser
	pipeErr  error
	startErr error
	waitErr  error
	starts   int
	waits    int
}

func (command *fakeObjectCommand) StdoutPipe() (io.ReadCloser, error) {
	return command.pipe, command.pipeErr
}

func (command *fakeObjectCommand) Start() error {
	command.starts++
	return command.startErr
}

func (command *fakeObjectCommand) Wait() error {
	command.waits++
	return command.waitErr
}

type failingObjectReader struct{}

func (failingObjectReader) Read([]byte) (int, error) { return 0, errors.New("object read failed") }

func TestSHA256ObjectCommandFailureOrdering(t *testing.T) {
	t.Parallel()
	pipeErr := errors.New("pipe descriptors exhausted")
	pipeFailure := &fakeObjectCommand{pipeErr: pipeErr}
	if got, err := sha256ObjectWithCommand(pipeFailure); got != "" || !errors.Is(err, pipeErr) || pipeFailure.starts != 0 || pipeFailure.waits != 0 {
		t.Fatalf("pipe failure = (%q, %v, starts %d, waits %d)", got, err, pipeFailure.starts, pipeFailure.waits)
	}
	startErr := errors.New("start failed")
	startFailure := &fakeObjectCommand{pipe: io.NopCloser(strings.NewReader("data")), startErr: startErr}
	if got, err := sha256ObjectWithCommand(startFailure); got != "" || !errors.Is(err, startErr) || startFailure.starts != 1 || startFailure.waits != 0 {
		t.Fatalf("start failure = (%q, %v, starts %d, waits %d)", got, err, startFailure.starts, startFailure.waits)
	}
	waitErr := errors.New("wait failed")
	waitFailure := &fakeObjectCommand{pipe: io.NopCloser(strings.NewReader("data")), waitErr: waitErr}
	if got, err := sha256ObjectWithCommand(waitFailure); got != "" || !errors.Is(err, waitErr) || waitFailure.starts != 1 || waitFailure.waits != 1 {
		t.Fatalf("wait failure = (%q, %v, starts %d, waits %d)", got, err, waitFailure.starts, waitFailure.waits)
	}
	success := &fakeObjectCommand{pipe: io.NopCloser(strings.NewReader("data"))}
	sum := sha256.Sum256([]byte("data"))
	if got, err := sha256ObjectWithCommand(success); err != nil || got != hex.EncodeToString(sum[:]) || success.starts != 1 || success.waits != 1 {
		t.Fatalf("object hash = (%q, %v, starts %d, waits %d)", got, err, success.starts, success.waits)
	}
}

func TestSHA256ObjectStreamFailureStillWaits(t *testing.T) {
	t.Parallel()
	waited := false
	if got, err := hashObjectAndWait(failingObjectReader{}, func() error { waited = true; return nil }); got != "" || err == nil || !waited {
		t.Fatalf("read failure = (%q, %v, waited %t)", got, err, waited)
	}
}
