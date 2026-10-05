//go:build e2e

package quality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readCommandDiagnostic(t *testing.T, err error) (CoverageDiagnosticManifest, []byte) {
	t.Helper()
	var failure *coverageCommandError
	if !errors.As(err, &failure) || failure.manifestPath == "" {
		t.Fatalf("missing current manifest: %v", err)
	}
	diagnostic := coverageDiagnosticFromPath(failure.manifestPath)
	if diagnostic == nil {
		t.Fatal("manifest unreadable")
	}
	raw, readErr := os.ReadFile(diagnostic.Manifest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	sum := sha256.Sum256(raw)
	if diagnostic.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("manifest digest differs")
	}
	var manifest CoverageDiagnosticManifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil || len(manifest.Files) != 1 {
		t.Fatalf("manifest=%+v %v", manifest, err)
	}
	file := manifest.Files[0]
	log, readErr := os.ReadFile(file.Path)
	sum = sha256.Sum256(log)
	if readErr != nil || file.Bytes != len(log) || file.SHA256 != hex.EncodeToString(sum[:]) || file.ElapsedNS <= 0 {
		t.Fatalf("log receipt=%+v %v", file, readErr)
	}
	for _, path := range []string{filepath.Dir(file.Path), file.Path, diagnostic.Manifest} {
		info, statErr := os.Stat(path)
		want := os.FileMode(0600)
		if info != nil && info.IsDir() {
			want = 0700
		}
		if statErr != nil || info.Mode().Perm() != want {
			t.Fatalf("privacy mode %s: %v %v", path, info, statErr)
		}
	}
	return manifest, log
}

func TestE2ECoverageCommandRetainsCompleteFinalFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("original command failure")
	raw := "HEAD\n" + strings.Repeat("middle evidence\n", 2000) + "TAIL\n"
	directory, module := filepath.Join(t.TempDir(), "reports"), t.TempDir()
	options := RunOptions{CoverageDiagnosticsDir: directory, CoverageDiagnosticsRepository: "example/repo", Retry: 2}
	// Seed a legacy shard receipt; each command phase must retain its own evidence.
	sink := newCoverageDiagnosticsSink(directory, "example/repo", module)
	if err := sink.persist(0, goCoverageJob{label: "shard"}, goCoverageJobResult{output: "shard", err: boom}); err != nil {
		t.Fatal(err)
	}
	legacy := coverageDiagnosticFor(directory, "example/repo", module)
	for _, phase := range []string{"unsharded", "native"} {
		args := []string{"test"}
		if phase == "native" {
			args = append(args, "-tags=e2e")
		}
		calls := 0
		attempt := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(context.Context, []string, string, string, ...string) (string, error) {
			calls++
			if calls < 3 {
				return "discarded retry", boom
			}
			return raw, boom
		}}
		output, count, err := runGoCoverageCommandWithAttempt(context.Background(), options, module, "", args, attempt)
		if output != raw || count != 3 || calls != 3 || !errors.Is(err, boom) || err.Error() != boom.Error() {
			t.Fatalf("command output/count/error: %d %d %v", count, calls, err)
		}
		manifest, log := readCommandDiagnostic(t, err)
		if string(log) != raw || manifest.Repository != "example/repo" || manifest.Module != module || manifest.Files[0].Label != phase+" coverage" || manifest.Files[0].TimeoutSource != "" {
			t.Fatalf("raw/manifest changed: %+v", manifest)
		}
		if !strings.Contains(manifest.Files[0].Path, "-"+phase+"-") {
			t.Fatal("phase not namespaced")
		}
	}
	if got := coverageDiagnosticFor(directory, "example/repo", module); got == nil || *got != *legacy {
		t.Fatalf("legacy manifest replaced: %+v", got)
	}
	files, err := filepath.Glob(filepath.Join(directory, "coverage-raw-*.log"))
	if err != nil || len(files) != 3 {
		t.Fatalf("phase logs=%v %v", files, err)
	}
}

func TestE2ECoverageCommandDisabledSuccessFallbackAndFault(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"disabled", "success", "empty", "write fault", "native infrastructure", "retry success"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "reports")
			boom := errors.New("failure identity")
			options := RunOptions{CoverageDiagnosticsDir: directory}
			if name == "disabled" {
				options.CoverageDiagnosticsDir = ""
			}
			if name == "write fault" {
				nativeWrite(t, directory, "occupied")
			}
			if name == "retry success" {
				options.Retry = 2
			}
			calls := 0
			attempt := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(context.Context, []string, string, string, ...string) (string, error) {
				calls++
				if name == "success" || (name == "retry success" && calls == 2) {
					return "success", nil
				}
				if name == "native infrastructure" {
					return "native failure", &nativeCoverageError{cause: boom}
				}
				return "", boom
			}}
			out, count, err := runGoCoverageCommandWithAttempt(context.Background(), options, t.TempDir(), "", []string{"test"}, attempt)
			if name == "success" || name == "retry success" {
				want := 1
				if name == "retry success" {
					want = 2
				}
				if err != nil || count != want || out != "success" {
					t.Fatal(out, count, err)
				}
				if _, err = os.Stat(directory); !os.IsNotExist(err) {
					t.Fatal("successful command emitted diagnostics")
				}
				return
			}
			if !errors.Is(err, boom) || count != 1 {
				t.Fatal(count, err)
			}
			var metadata *coverageCommandError
			if name == "disabled" {
				if errors.As(err, &metadata) {
					t.Fatal("disabled metadata")
				}
				return
			}
			if name == "write fault" {
				if !errors.As(err, &metadata) || metadata.manifestPath != "" || !strings.Contains(err.Error(), "write coverage diagnostics") {
					t.Fatal(err)
				}
				return
			}
			_, log := readCommandDiagnostic(t, err)
			if name == "empty" && string(log) != boom.Error()+"\n" {
				t.Fatalf("fallback=%q", log)
			}
			if name == "native infrastructure" && !isNativeCoverageFailure(err) {
				t.Fatal("native classification lost")
			}
		})
	}
	// Discovery still fails before any attempt and carries its original infrastructure error.
	_, count, err := runGoCoverageCommand(context.Background(), RunOptions{}, t.TempDir(), "", []string{"-coverpkg"})
	if count != 0 || !isNativeCoverageFailure(err) {
		t.Fatal(count, err)
	}
	_, count, err = runGoCoverageCommand(context.Background(), RunOptions{CoverageDiagnosticsDir: t.TempDir()}, t.TempDir(), "", []string{"-coverpkg"})
	var discoveryFailure *coverageCommandError
	if count != 0 || !isNativeCoverageFailure(err) || !errors.As(err, &discoveryFailure) || discoveryFailure.manifestPath != "" {
		t.Fatalf("discovery failure lost current unavailable marker: %d %v", count, err)
	}
}

func TestE2ECoverageCommandBaselineAcceptanceCannotHideDiagnosticFault(t *testing.T) {
	t.Parallel()
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	for _, fault := range []bool{false, true} {
		directory, module := filepath.Join(t.TempDir(), "reports"), t.TempDir()
		if fault {
			nativeWrite(t, directory, "occupied")
		}
		profile := filepath.Join(module, "base.cov")
		nativeWrite(t, profile, "mode: set\nexample/a.go:1.1,2.2 1 1\n")
		recorder := &redBaseRecorder{}
		options := RunOptions{CoverageDiagnosticsDir: directory, redBase: recorder}
		attempt := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(context.Context, []string, string, string, ...string) (string, error) {
			return redBaseFailingPackageOutput, exitErr
		}}
		out, count, err := runGoCoverageCommandWithAttempt(context.Background(), options, module, profile, []string{"test"}, attempt)
		if out != redBaseFailingPackageOutput || count != 1 || len(recorder.failed) == 0 {
			t.Fatal("original baseline decision changed", out, count, err)
		}
		if fault {
			var metadata *coverageCommandError
			if err == nil || !errors.As(err, &metadata) || metadata.manifestPath != "" || errors.Is(err, exitErr) {
				t.Fatalf("accepted command/IO fault: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestE2ECoverageCommandTimeoutSources(t *testing.T) {
	t.Parallel()
	boom := errors.New("failure")
	for _, name := range []string{"success", "ordinary", "check", "caller", "cancelled", "attempt", "binary"} {
		ctx, attemptCtx := context.Background(), context.Background()
		cancel := func() {}
		switch name {
		case "caller":
			ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
		case "cancelled":
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		case "attempt":
			attemptCtx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}
		if name == "check" {
			checkCtx, done := context.WithCancelCause(context.Background())
			done(errLogicalCheckTimeout)
			ctx = checkCtx
		}
		output := ""
		if name == "binary" {
			output = "panic: test timed out after 1s"
		}
		failure := boom
		if name == "success" {
			failure = nil
		}
		want := map[string]string{"success": "", "ordinary": "", "check": "check", "caller": "caller", "cancelled": "caller-cancelled", "attempt": "attempt", "binary": "attempt"}[name]
		if got := coverageCommandTimeoutSource(ctx, attemptCtx, output, failure); got != want {
			t.Fatalf("%s=%s want%s", name, got, want)
		}
		cancel()
	}
	directory := filepath.Join(t.TempDir(), "reports")
	options := RunOptions{CoverageDiagnosticsDir: directory, Timeout: time.Millisecond, Retry: 0}
	attempt := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(ctx context.Context, _ []string, _, _ string, _ ...string) (string, error) {
		<-ctx.Done()
		return "complete timeout output", ctx.Err()
	}}
	_, count, err := runGoCoverageCommandWithAttempt(context.Background(), options, t.TempDir(), "", []string{"test"}, attempt)
	manifest, log := readCommandDiagnostic(t, err)
	if count != 1 || manifest.Files[0].TimeoutSource != "attempt" || string(log) != "complete timeout output" {
		t.Fatal(count, manifest, string(log))
	}
}

func TestE2ECoverageReportBindsNativeFailureAndContextEvidence(t *testing.T) {
	if qualityFixtureChild(t) {
		fixtureCoverageReportBindsNativeFailureAndContextEvidence(t)
		return
	}
	t.Parallel()
	runQualityFixtureChild(t)
}

func fixtureCoverageReportBindsNativeFailureAndContextEvidence(t *testing.T) {
	for _, name := range []string{"native fail", "deadline", "check deadline", "cancel", "write fault"} {
		module := nativeModule(t)
		shim := dqCovFakeGo(t, module)
		ready := filepath.Join(module, "native-ready")
		script := `#!/bin/sh
if [ "$1" = list ]; then printf "example.test/nativecoverage\n"; exit 0; fi
profile=''
native=''
for arg in "$@"; do
 case "$arg" in
 -coverprofile=*) profile=${arg#-coverprofile=} ;;
 -tags=e2e) native=1 ;;
 esac
done
if [ -n "$native" ]; then
 printf 'native head\ncomplete middle evidence\nnative tail\n'
 printf ready > "$READY"
 if [ "$WAIT" = 1 ]; then sleep 600; fi
 exit 1
fi
printf 'mode: set\nexample/a.go:1.1,2.2 1 1\n' > "$profile"
exit 0
`
		nativeWrite(t, shim, script)
		directory := filepath.Join(t.TempDir(), "reports")
		// Existing manifests must never substitute for this invocation's evidence.
		old := newCoverageDiagnosticsSink(directory, "example/repo", module)
		if err := old.persist(0, goCoverageJob{label: "old shard"}, goCoverageJobResult{output: "stale", err: errors.New("old")}); err != nil {
			t.Fatal(err)
		}
		options := RunOptions{IncludeE2E: true, CoverageDiagnosticsDir: directory, Env: []string{"READY=" + ready, "WAIT=0"}}
		ctx := context.Background()
		cancel := func() {}
		if name == "deadline" {
			ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
			options.Env[1] = "WAIT=1"
		}
		if name == "check deadline" {
			options.CheckTimeout = 2 * time.Second
			options.Env[1] = "WAIT=1"
		}
		cancelDone := make(chan error, 1)
		if name == "cancel" {
			ctx, cancel = context.WithCancel(ctx)
			options.Env[1] = "WAIT=1"
			go func() {
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						cancel()
						cancelDone <- nil
						return
					}
					if time.Now().After(deadline) {
						cancel()
						cancelDone <- errors.New("native phase never ready")
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
		}
		if name == "write fault" {
			// Force only the native raw-log write to fail; the old manifest still exists.
			blocker := filepath.Join(directory, "coverage-raw-"+coverageDiagnosticStem("example/repo", module)+"-native-1.log")
			if err := os.Mkdir(blocker, 0700); err != nil {
				t.Fatal(err)
			}
		}
		report := CoverWithOptions(ctx, "example/repo", module, options)
		cancel()
		if name == "cancel" {
			if err := <-cancelDone; err != nil {
				t.Fatal(err)
			}
		}
		if report.Status != StatusFailed {
			t.Fatalf("%s report=%+v", name, report)
		}
		if name == "write fault" {
			if report.Diagnostic != nil || !strings.Contains(report.Error, "write coverage diagnostics") {
				t.Fatalf("stale evidence/fault hidden: %+v", report)
			}
			continue
		}
		if report.Diagnostic == nil || !strings.Contains(report.Diagnostic.Manifest, "-native.yaml") {
			t.Fatalf("%s report lost native evidence: %+v", name, report)
		}
		raw, err := os.ReadFile(report.Diagnostic.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		var manifest CoverageDiagnosticManifest
		if err = yaml.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		log, err := os.ReadFile(manifest.Files[0].Path)
		if err != nil || string(log) != "native head\ncomplete middle evidence\nnative tail\n" {
			t.Fatalf("native raw=%q %v", log, err)
		}
		want := ""
		if name == "deadline" {
			want = "caller"
		}
		if name == "check deadline" {
			want = "check"
		}
		if name == "cancel" {
			want = "caller-cancelled"
		}
		if manifest.Files[0].TimeoutSource != want {
			t.Fatalf("timeout=%s want%s", manifest.Files[0].TimeoutSource, want)
		}
		if name == "deadline" && !strings.Contains(report.Error, context.DeadlineExceeded.Error()) {
			t.Fatal(report.Error)
		}
		if name == "cancel" && !strings.Contains(report.Error, context.Canceled.Error()) {
			t.Fatal(report.Error)
		}
	}
}
