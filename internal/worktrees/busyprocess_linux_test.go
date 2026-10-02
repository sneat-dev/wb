//go:build linux

package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestReadPPIDUsesStatParentField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		stat    string
		present bool
		parent  int
		valid   bool
	}{
		{name: "missing"},
		{name: "missing closing parenthesis", present: true, stat: "123 (worker S 42"},
		{name: "truncated after comm", present: true, stat: "123 (worker)"},
		{name: "state without parent", present: true, stat: "123 (worker) S"},
		{name: "nonnumeric parent", present: true, stat: "123 (worker) S invalid"},
		{name: "nested and spaced comm", present: true, stat: "123 (worker (nested) name) S 42 0 0", parent: 42, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.present {
				writeProcessStat(t, root, 123, tc.stat)
			}
			if parent, valid := readPPIDAt(root, 123); parent != tc.parent || valid != tc.valid {
				t.Fatalf("parent = %d, %t, want %d, %t", parent, valid, tc.parent, tc.valid)
			}
		})
	}
}

func TestAncestorPIDsStopAtMissingEvidenceCyclesAndBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		start   int
		parents map[int]int
		want    map[int]bool
	}{
		{name: "missing starting stat", start: 42, want: map[int]bool{42: true}},
		{name: "init excluded", start: 1, want: map[int]bool{}},
		{name: "invalid start excluded", start: 0, want: map[int]bool{}},
		{name: "own ancestors with init excluded", start: 42, parents: map[int]int{42: 43, 43: 1}, want: map[int]bool{42: true, 43: true}},
		{name: "cycle", start: 42, parents: map[int]int{42: 43, 43: 42}, want: map[int]bool{42: true, 43: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for pid, parent := range tc.parents {
				writeProcessStat(t, root, pid, fmt.Sprintf("%d (worker) S %d", pid, parent))
			}
			if got := selfAndAncestorPIDsAt(root, tc.start); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ancestors = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("64 hop bound", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		want := map[int]bool{}
		for pid := 100; pid < 170; pid++ {
			writeProcessStat(t, root, pid, fmt.Sprintf("%d (worker) S %d", pid, pid+1))
			if pid < 164 {
				want[pid] = true
			}
		}
		if got := selfAndAncestorPIDsAt(root, 100); !reflect.DeepEqual(got, want) {
			t.Fatalf("ancestors = %v, want %v", got, want)
		}
	})
}

func TestBusyProcessReasonUsesReadableCwdEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		pid  int
		cwd  string
		comm string
		want string
	}{
		{name: "own process", pid: 42, cwd: "/checkout", comm: " wb \n", want: "this command's own process tree (pid 42, wb) has its working directory inside /checkout; cd out of the checkout and re-run"},
		{name: "own ancestor", pid: 43, cwd: "/checkout/sub/../sub", comm: "shell\n", want: "this command's own process tree (pid 43, shell) has its working directory inside /checkout; cd out of the checkout and re-run"},
		{name: "unrelated nested process", pid: 90, cwd: "/checkout/sub", comm: "agent\n", want: "process 90 (agent) has its working directory inside /checkout"},
		{name: "missing command", pid: 90, cwd: "/checkout", want: "process 90 (unknown) has its working directory inside /checkout"},
		{name: "sibling prefix", pid: 90, cwd: "/checkout-other", comm: "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeProcessStat(t, root, 42, "42 (wb) S 43")
			writeProcessStat(t, root, 43, "43 (shell) S 1")
			directory := filepath.Join(root, strconv.Itoa(tc.pid))
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.cwd, filepath.Join(directory, "cwd")); err != nil {
				t.Fatal(err)
			}
			if tc.comm != "" {
				if err := os.WriteFile(filepath.Join(directory, "comm"), []byte(tc.comm), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Non-process entries and a disappearing process's missing cwd are not evidence.
			for _, name := range []string{"not-a-pid", "0", "-1", "2"} {
				if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if got := busyProcessReasonAt(root, 42, []string{"", "/other", "/checkout/."}); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("unavailable proc root", func(t *testing.T) {
		t.Parallel()
		if got := busyProcessReasonAt(filepath.Join(t.TempDir(), "absent"), 42, []string{"/checkout"}); got != "" {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("empty paths", func(t *testing.T) {
		t.Parallel()
		if got := busyProcessReasonAt(t.TempDir(), 42, []string{"", ""}); got != "" {
			t.Fatalf("reason = %q", got)
		}
	})
	t.Run("empty readable proc root", func(t *testing.T) {
		t.Parallel()
		if got := busyProcessReasonAt(t.TempDir(), 42, []string{"/checkout"}); got != "" {
			t.Fatalf("reason = %q", got)
		}
	})
}

func TestProcessInspectionUsesNativeProc(t *testing.T) {
	t.Parallel()
	if !BusyProcessCheckSupported {
		t.Fatal("Linux process inspection is unsupported")
	}
	if parent, valid := readPPIDAt("/proc", os.Getpid()); !valid || parent != os.Getppid() {
		t.Fatalf("native parent = %d, %t, want %d", parent, valid, os.Getppid())
	}
	ancestors := selfAndAncestorPIDsAt("/proc", os.Getpid())
	if !ancestors[os.Getpid()] || ancestors[1] {
		t.Fatalf("native ancestors = %v", ancestors)
	}
	if got := BusyProcessReason([]string{"", ""}); got != "" {
		t.Fatalf("empty paths reason = %q", got)
	}
}

func writeProcessStat(t *testing.T, root string, pid int, stat string) {
	t.Helper()
	directory := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(stat), 0600); err != nil {
		t.Fatal(err)
	}
}
