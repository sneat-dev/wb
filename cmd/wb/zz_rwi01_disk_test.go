package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/testenv"
)

// cwDiskScantFreeSpace reports far less than the default 10% headroom floor,
// so disk.Collect always raises a low-headroom finding regardless of what
// the (skipped) tree walk finds.
func cwDiskScantFreeSpace(path string) (disk.Filesystem, error) {
	const gigabyte = 1 << 30
	return disk.Filesystem{Path: path, TotalBytes: 100 * gigabyte, UsedBytes: 99 * gigabyte, AvailableBytes: 1 * gigabyte}, nil
}

// TestRwi01DiskYAMLFormatEncodesTheReport drives the --format yaml branch,
// which no existing disk test exercises (only text and json are covered):
// it must succeed and produce YAML-shaped output.
func TestRwi01DiskYAMLFormatEncodesTheReport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newDiskCmdWithProbe(&invocation{projectsRoot: root}, cwDiskAmpleFreeSpace)
	}, "--format", "yaml", "--skip-sizes")
	if err != nil {
		t.Fatalf("wb disk --format yaml: %v", err)
	}
	if !strings.Contains(stdout, "categories:") || !strings.Contains(stdout, "filesystem:") {
		t.Fatalf("wb disk yaml report does not look like YAML:\n%s", stdout)
	}
	if strings.Contains(stdout, "findings:") {
		t.Fatalf("wb disk yaml raised a finding despite the injected ample-space probe:\n%s", stdout)
	}
}

// TestRwi01DiskFindsLowHeadroom drives the len(report.Findings) > 0 branch,
// which forces an exitFindings error and an explanatory message: an empty
// fleet under the default probe never raises a finding, so the injected
// scant-space probe is required.
func TestRwi01DiskFindsLowHeadroom(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	_, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newDiskCmdWithProbe(&invocation{projectsRoot: root}, cwDiskScantFreeSpace)
	}, "--skip-sizes")
	if err == nil {
		t.Fatal("wb disk with a scant-space probe: want an error, got nil")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("wb disk with a scant-space probe: err = %#v, want *exitError", err)
	}
	if exitErr.code != exitFindings {
		t.Fatalf("wb disk with a scant-space probe: code = %d, want exitFindings", exitErr.code)
	}
}

// rwi01FailingOut always fails its Write, so a command's own text-render
// failure path can be driven without depending on a real broken pipe.
type rwi01FailingOut struct{}

func (rwi01FailingOut) Write([]byte) (int, error) { return 0, errors.New("rwi01: disk write refused") }

// TestRwi01DiskTextRenderWriteFailureIsSurfaced drives the default (text)
// format's fmt.Fprint error-check: a write failure while rendering the
// report must be returned, not swallowed.
func TestRwi01DiskTextRenderWriteFailureIsSurfaced(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	testenv.Isolate(t)
	command := newDiskCmdWithProbe(&invocation{projectsRoot: root}, cwDiskAmpleFreeSpace)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	command.SetOut(rwi01FailingOut{})
	command.SetErr(rwi01FailingOut{})
	command.SetArgs([]string{"--skip-sizes"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "rwi01: disk write refused") {
		t.Fatalf("wb disk text render with a failing writer: err = %v, want the write failure surfaced", err)
	}
}
