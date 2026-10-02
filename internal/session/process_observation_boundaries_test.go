package session

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestHarnessAncestorContinuesAfterUnavailableObservation(t *testing.T) {
	t.Parallel()
	if pid, runtime := FindHarnessAncestor(0); pid != 0 || runtime != "" {
		t.Fatalf("invalid ancestor = %d %q", pid, runtime)
	}
	var observed []int
	pid, runtime := findHarnessAncestorWithReaders(40, func(pid int) (int, bool) { return pid - 1, true }, func(pid int) (ProcessEvidence, bool) {
		observed = append(observed, pid)
		if pid == 39 {
			return ProcessEvidence{}, false
		}
		return ProcessEvidence{Executable: "/opt/bin/claude"}, true
	})
	if pid != 38 || runtime != "claude-code" || len(observed) != 2 || observed[0] != 39 || observed[1] != 38 {
		t.Fatalf("ancestor = %d %q; observed %v", pid, runtime, observed)
	}
	pid, runtime = findHarnessAncestorWithReaders(40, func(pid int) (int, bool) { return pid - 1, true }, func(int) (ProcessEvidence, bool) { return ProcessEvidence{}, false })
	if pid != 0 || runtime != "" {
		t.Fatalf("bounded failure = %d %q", pid, runtime)
	}
}

func TestProcParentColumns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input string
		pid   int
		ok    bool
	}{{"42 (name with ) spaces) S 17 0", 17, true}, {"no parentheses", 0, false}, {"42 (name)", 0, false}, {"42 (name) S bad", 0, false}, {"42 (name) S 0", 0, false}, {"42 (name) S -1", 0, false}} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			pid, ok := parentPIDFromStat([]byte(tc.input))
			if pid != tc.pid || ok != tc.ok {
				t.Fatalf("parent = %d %v", pid, ok)
			}
		})
	}
}

func TestProcEvidenceReadBoundaries(t *testing.T) {
	t.Parallel()
	failure := errors.New("proc observation unavailable")
	for _, name := range []string{"invalid pid", "readlink error", "empty executable", "cmdline error", "terminated arguments", "unterminated arguments", "empty cmdline"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pid := 42
			if name == "invalid pid" {
				pid = 0
			}
			links, reads := 0, 0
			evidence, ok := processEvidenceFromProc(pid, func(path string) (string, error) {
				links++
				if path != filepath.Join("/proc", "42", "exe") {
					t.Fatalf("link path = %q", path)
				}
				if name == "readlink error" {
					return "", failure
				}
				if name == "empty executable" {
					return " \t", nil
				}
				return "/opt/codex", nil
			}, func(path string) ([]byte, error) {
				reads++
				if path != filepath.Join("/proc", "42", "cmdline") {
					t.Fatalf("cmdline path = %q", path)
				}
				if name == "cmdline error" {
					return nil, failure
				}
				if name == "empty cmdline" {
					return nil, nil
				}
				if name == "unterminated arguments" {
					return []byte("codex\x00app-server"), nil
				}
				return []byte("codex\x00app-server\x00"), nil
			})
			success := name == "terminated arguments" || name == "unterminated arguments" || name == "empty cmdline"
			if ok != success {
				t.Fatalf("evidence = %+v %v", evidence, ok)
			}
			if name == "invalid pid" && (links != 0 || reads != 0) {
				t.Fatal("invalid pid observed")
			}
			if success && (evidence.Executable != "/opt/codex" || (name != "empty cmdline" && !processEvidenceMatchesRuntime(evidence, "codex"))) {
				t.Fatalf("evidence = %+v", evidence)
			}
		})
	}
}
