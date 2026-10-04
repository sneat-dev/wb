package daemonhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubPepperFailureStagesKeepActualReadAndPrivateModes(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"mkdir", "write"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "private-state")
			sentinel := errors.New(stage + " refused")
			mkdirCalls, writeCalls := 0, 0
			mkdir := func(path string, mode os.FileMode) error {
				mkdirCalls++
				if path != directory || mode != 0700 {
					t.Fatalf("mkdir=%q,%o", path, mode)
				}
				if stage == "mkdir" {
					return sentinel
				}
				return os.MkdirAll(path, mode)
			}
			write := func(path string, payload []byte, mode os.FileMode) error {
				writeCalls++
				if path != filepath.Join(directory, "pepper") || mode != 0600 || len(payload) != hubPepperBytes {
					t.Fatalf("write=%q,%o,bytes%d", path, mode, len(payload))
				}
				info, err := os.Stat(directory)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("real directory=%v,%v", info, err)
				}
				return sentinel
			}
			pepper, err := hubPepperWithEffects(directory, mkdir, write)
			if pepper != nil || !errors.Is(err, sentinel) {
				t.Fatalf("%s pepper=%v,error=%v", stage, pepper, err)
			}
			wantWrites := 1
			if stage == "mkdir" {
				wantWrites = 0
			}
			if mkdirCalls != 1 || writeCalls != wantWrites {
				t.Fatalf("stages mkdir%d/write%d", mkdirCalls, writeCalls)
			}
			if _, err := os.Stat(filepath.Join(directory, "pepper")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failure persisted pepper:%v", err)
			}
		})
	}
}

func TestMachineNameObservedHostnameFailuresPreserveRefusal(t *testing.T) {
	t.Parallel()
	// These explicitly simulate failed/empty observations, not native OS failure.
	for _, value := range []string{"error", "empty", "whitespace"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			calls := 0
			name, err := localMachineName(filepath.Join(t.TempDir(), "absent.yaml"), func() (string, error) {
				calls++
				switch value {
				case "error":
					return "", errors.New("hostname refused")
				case "whitespace":
					return "\t ", nil
				}
				return "", nil
			})
			if name != "" || err == nil || !strings.Contains(err.Error(), "hub machine name could not be resolved; set remote.machine in wb.yaml") || calls != 1 {
				t.Fatalf("observed name=%q,error=%v,calls%d", name, err, calls)
			}
		})
	}
}
