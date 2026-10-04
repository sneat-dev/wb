//go:build darwin

package daemonruntime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/filewrite"
)

// These cases simulate the observed mode only at the private producer pipeline.
// Every production wrapper still derives testing.Testing(), and its real refusal
// cases independently prove that a go-test process cannot launch a daemon.
func TestPrivateProcessModeRetainsIndependentExecutableSuffixFence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path              string
		mode, wantRefusal bool
	}{{"/private/wb", true, true}, {"/private/wb.test", false, true}, {"/private/wb", false, false}} {
		err := daemonRefuseTestBinaryForMode(tc.path, tc.mode)
		if (err != nil) != tc.wantRefusal || err != nil && !strings.Contains(err.Error(), "Go test binary as the WB daemon ("+tc.path+")") {
			t.Fatalf("mode %v path %q refusal=%v", tc.mode, tc.path, err)
		}
	}
}

func TestPrivateLaunchdPipelinePreservesNativeOrderAndAtomicFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name               string
		step               filewrite.Step
		failCommand, print string
		want               string
	}{
		{name: "ready", print: "pid = 902"},
		{name: "create", step: filewrite.StepOpenOrCreate, want: "fixture write refusal"},
		{name: "chmod", step: filewrite.StepChmod, want: "fixture write refusal"},
		{name: "write", step: filewrite.StepWrite, want: "fixture write refusal"},
		{name: "close", step: filewrite.StepClose, want: "fixture write refusal"},
		{name: "rename", step: filewrite.StepRename, want: "fixture write refusal"},
		{name: "bootstrap", failCommand: "bootstrap", want: "bootstrap WB launch agent"},
		{name: "kickstart", failCommand: "kickstart", want: "start WB launch agent"},
		{name: "ready timeout", want: "did not report a running PID within 100ms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			log := filepath.Join(home, "logs", "daemon.log")
			var calls []string
			boom := errors.New("fixture write refusal")
			native := defaultNativeOperations()
			native.lifecycleBounds = func() LifecycleBounds { return LifecycleBounds{Ready: 100 * time.Millisecond} }
			native.runLaunchctl = func(args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == "bootout" {
					return []byte("old job absent"), boom
				} // ignored as before
				if args[0] == tc.failCommand {
					return []byte("  native diagnostic \n"), boom
				}
				if args[0] == "print" {
					return []byte(tc.print), nil
				}
				return nil, nil
			}
			virtual := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
			now := func() time.Time { return virtual }
			sleep := func(d time.Duration) { virtual = virtual.Add(d) }
			var inj *filewrite.Injector
			if tc.step != "" {
				inj = &filewrite.Injector{Step: tc.step, Err: boom}
			}
			pid, err := native.startDaemonProcessForMode("/private/wb", []string{"daemon", "serve", "--projects-root", home}, log, inj, now, sleep, false)
			if tc.want == "" {
				if err != nil || pid != 902 || !reflect.DeepEqual(calls, []string{"bootout", "bootstrap", "kickstart", "print"}) {
					t.Fatalf("native order/result=%v,%d,%v", calls, pid, err)
				}
			} else {
				if pid != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("failure result=%d,%v", pid, err)
				}
				if tc.step != "" {
					if !errors.Is(err, boom) || len(calls) != 0 {
						t.Fatalf("write failure reached executor=%v,%v", calls, err)
					}
				} else if tc.failCommand != "" && !strings.Contains(err.Error(), "native diagnostic") {
					t.Fatalf("native diagnostic lost=%v", err)
				}
			}
			plist := filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
			if tc.step != "" {
				if _, err := os.Stat(plist); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed atomic publish left final plist=%v", err)
				}
			} else {
				info, err := os.Stat(plist)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("published plist=%v,%v", info, err)
				}
				data, err := os.ReadFile(plist)
				if err != nil || !strings.Contains(string(data), home) || !strings.Contains(string(data), log) {
					t.Fatalf("published arguments/log=%s,%v", data, err)
				}
			}
			files, err := filepath.Glob(filepath.Join(filepath.Dir(plist), ".wb-daemon-*.plist"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary plists=%v,%v", files, err)
			}
		})
	}
}

func TestPrivateLaunchdPreparationRefusesInvalidHomeAndDirectoriesBeforeExecutor(t *testing.T) {
	for _, kind := range []string{"home missing", "log parent blocked", "plist parent blocked", "suffix fenced", "process fenced"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			log := filepath.Join(home, "logs", "daemon.log")
			native := defaultNativeOperations()
			called := false
			native.runLaunchctl = func(...string) ([]byte, error) { called = true; return nil, nil }
			executable := "/private/wb"
			mode := false
			switch kind {
			case "home missing":
				t.Setenv("HOME", "")
			case "log parent blocked":
				if err := os.WriteFile(filepath.Join(home, "logs"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "plist parent blocked":
				if err := os.WriteFile(filepath.Join(home, "Library"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "suffix fenced":
				executable += ".test"
			case "process fenced":
				mode = true
			}
			if _, err := native.startDaemonProcessForMode(executable, nil, log, nil, time.Now, time.Sleep, mode); err == nil || called {
				t.Fatalf("pre-effect refusal=%v,executor=%t", err, called)
			}
		})
	}
}

func TestPrivateLaunchctlModeRetainsExecutableFallbackAndRealBoundedChild(t *testing.T) {
	t.Run("fallback suffix remains refused", func(t *testing.T) {
		boom := errors.New("unavailable executable")
		if _, err := runNativeLaunchctlForMode(time.Second, false, func() (string, error) { return "", boom }, "print", "private-job"); err == nil || !strings.Contains(err.Error(), "Go test binary") {
			t.Fatalf("fallback refusal=%v", err)
		}
	})
	t.Run("private executor", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "launchctl")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\""), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		output, err := runNativeLaunchctlForMode(time.Second, false, func() (string, error) { return "/private/wb", nil }, "print", "private-job")
		if err != nil || string(output) != "print|private-job" {
			t.Fatalf("private executor=%q,%v", output, err)
		}
	})
}

func TestNativeLaunchdObserversUseOnePrintAndPreserveBootoutErrorFallback(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	var calls [][]string
	native.runLaunchctl = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("pid = 903"), nil
	}
	if !native.processAlive(903) || native.processAlive(904) || len(calls) != 2 || calls[0][0] != "print" {
		t.Fatalf("liveness print=%v", calls)
	}
	native.runLaunchctl = func(...string) ([]byte, error) { return nil, errors.New("bootout unavailable") }
	if err := native.stopDaemonProcess(0, daemon.SupervisorNone, ""); err != nil {
		t.Fatalf("absent PID fallback=%v", err)
	}
	// A deliberately nonexistent PID proves native SIGTERM error propagation;
	// it cannot signal a process created by another fixture.
	if err := native.stopDaemonProcess(1<<30, daemon.SupervisorNone, ""); err == nil {
		t.Fatal("missing native PID unexpectedly accepted signal")
	}
}
