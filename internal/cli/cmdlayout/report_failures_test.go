package cmdlayout

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type jsonFailure struct{}

func (jsonFailure) MarshalJSON() ([]byte, error) { return nil, io.ErrUnexpectedEOF }

type yamlFailure struct{}

func (yamlFailure) MarshalYAML() (any, error) { return nil, io.ErrUnexpectedEOF }
func TestOutputEncodersPropagateFailure(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"markdown", "yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			cmd.SetOut(failingWriter{})
			if err := writeLayoutOutput(cmd, format, "text", map[string]int{"one": 1}); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := writeLayoutOutput(cmd, "yaml", "", yamlFailure{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("yaml error=%v", err)
	}
	if err := writeLayoutOutput(cmd, "json", "", make(chan int)); err == nil {
		t.Fatal("JSON accepted channel")
	}
}
func TestReportsStopAtFailedFileOrEncoder(t *testing.T) {
	t.Parallel()
	for _, extension := range []string{"md", "yaml", "json"} {
		t.Run(extension, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "layout-audit."+extension), 0755); err != nil {
				t.Fatal(err)
			}
			if err := writeReports(dir, "audit", "text", map[string]int{"one": 1}); err == nil {
				t.Fatal("expected blocked file failure")
			}
		})
	}
	if err := writeReports(t.TempDir(), "audit", "text", yamlFailure{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("yaml=%v", err)
	}
	// A report can render as YAML while its JSON marshaler fails.
	if err := writeReports(t.TempDir(), "audit", "text", jsonFailure{}); err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("json=%v", err)
	}
}
