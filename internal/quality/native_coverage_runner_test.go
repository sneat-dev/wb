package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func nativeCoverageValue(environment []string, name string) string {
	for index := len(environment) - 1; index >= 0; index-- {
		if value, ok := strings.CutPrefix(environment[index], name+"="); ok {
			return value
		}
	}
	return ""
}
func nativeWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
func nativeModule(t *testing.T) string {
	t.Helper()
	module := t.TempDir()
	nativeWrite(t, filepath.Join(module, "go.mod"), "module example.test/nativecoverage\n\ngo 1.27.0\n")
	nativeWrite(t, filepath.Join(module, "main.go"), "package main\nimport \"os\"\nfunc main(){os.Exit(0)}\n")
	return module
}

func TestNativeCoverageCanonicalScopeAndEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want []string
		bad  bool
	}{
		{[]string{"test"}, nil, false}, {[]string{"-coverpkg=./a,./b"}, []string{"./a", "./b"}, false}, {[]string{"-coverpkg", "./a"}, []string{"./a"}, false}, {[]string{"-coverpkg"}, nil, true},
	} {
		got, err := nativeCoveragePatterns(tc.args)
		if !slices.Equal(got, tc.want) || (err != nil) != tc.bad {
			t.Fatalf("patterns %v => %v %v", tc.args, got, err)
		}
	}
	module := nativeModule(t)
	packages, err := resolveNativeCoveragePackages(context.Background(), module, []string{"GOWORK=off"}, []string{"-coverpkg=./..."}, nil, nil)
	if err != nil || !slices.Equal(packages, []string{"example.test/nativecoverage"}) {
		t.Fatalf("canonical %v %v", packages, err)
	}
	known := []string{"example.test/excluded"}
	packages, err = resolveNativeCoveragePackages(context.Background(), module, nil, []string{"-coverpkg=./excluded"}, []string{"./excluded"}, known)
	if err != nil || !slices.Equal(packages, known) {
		t.Fatalf("reuse/exclusion %v %v", packages, err)
	}
	packages[0] = "mutation"
	if known[0] != "example.test/excluded" {
		t.Fatal("aliased scope")
	}
	if _, err = resolveNativeCoveragePackages(context.Background(), module, nil, []string{"-coverpkg"}, nil, nil); !isNativeCoverageFailure(err) {
		t.Fatal(err)
	}
	if _, err = resolveNativeCoveragePackages(context.Background(), module, nil, []string{"-coverpkg=./missing"}, nil, nil); !isNativeCoverageFailure(err) {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(module, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = resolveNativeCoveragePackages(context.Background(), module, nil, []string{"-coverpkg=./empty/..."}, nil, nil); !isNativeCoverageFailure(err) {
		t.Fatal(err)
	}
	environment := []string{"INPUT=private", "WB_TEST_NATIVE_COVERPKG=old"}
	configured := nativeCoverageEnvironment(environment, known)
	if nativeCoverageValue(configured, "WB_TEST_NATIVE_COVERPKG") != known[0] || environment[1] != "WB_TEST_NATIVE_COVERPKG=old" {
		t.Fatal("environment mutated")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = resolveNativeCoveragePackages(ctx, module, nil, []string{"-coverpkg=./..."}, nil, nil); !isNativeCoverageFailure(err) {
		t.Fatal(err)
	}
}

func TestNativeCoverageAttemptAbsenceRefusalAndRetryIsolation(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name                      string
		files                     []string
		directory, remove, failed bool
		want                      bool
	}{
		{name: "empty"}, {name: "metadata-only", files: []string{"covmeta." + digest}, want: true}, {name: "counter-only", files: []string{"covcounters." + digest + ".1.2"}, want: true},
		{name: "unexpected", files: []string{"foreign"}, want: true}, {name: "malformed", files: []string{"covmeta.bad"}, want: true},
		{name: "directory", directory: true, want: true}, {name: "removed", remove: true, want: true}, {name: "failed", failed: true},
		{name: "extra-metadata", files: []string{"covmeta." + digest, "covcounters." + digest + ".1.2", "covmeta." + strings.Repeat("b", 32)}, want: true},
		{name: "extra-counter", files: []string{"covmeta." + digest, "covcounters." + digest + ".1.2", "covcounters." + strings.Repeat("b", 32) + ".1.2"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var directory string
			boom := errors.New("test failed")
			operation := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(_ context.Context, environment []string, _, _ string, _ ...string) (string, error) {
				directory = nativeCoverageValue(environment, "WB_TEST_NATIVE_COVERDIR")
				if tc.failed {
					return "original", boom
				}
				for _, name := range tc.files {
					nativeWrite(t, filepath.Join(directory, name), "corrupt")
				}
				if tc.directory {
					if err := os.Mkdir(filepath.Join(directory, "nested"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if tc.remove {
					if err := os.RemoveAll(directory); err != nil {
						t.Fatal(err)
					}
				}
				return "original", nil
			}}
			out, err := operation.execute(context.Background(), "", nil, "", nil)
			if out != "original" || isNativeCoverageFailure(err) != tc.want {
				t.Fatalf("refusal %q %v", out, err)
			}
			if tc.failed && !errors.Is(err, boom) {
				t.Fatalf("test identity %v", err)
			}
			if _, err = os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("owned directory leaked %v", err)
			}
		})
	}
	if _, err := (nativeCoverageAttempt{temporaryRoot: filepath.Join(t.TempDir(), "missing")}).execute(context.Background(), "", nil, "", nil); !isNativeCoverageFailure(err) {
		t.Fatal(err)
	}
	var directories []string
	operation := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(_ context.Context, env []string, _, _ string, _ ...string) (string, error) {
		directory := nativeCoverageValue(env, "WB_TEST_NATIVE_COVERDIR")
		directories = append(directories, directory)
		if len(directories) == 1 {
			nativeWrite(t, filepath.Join(directory, "covmeta.failed"), "discard")
			return "", errors.New("retry")
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatal("failed data carried into retry")
		}
		return "success", nil
	}}
	out, count, err := runCommandAttempts(context.Background(), 0, 1, func(ctx context.Context) (string, error) { return operation.execute(ctx, "", nil, "", nil) })
	if err != nil || out != "success" || count != 2 || directories[0] == directories[1] {
		t.Fatalf("retry isolation %q %d %v", out, count, err)
	}
	for _, dir := range directories {
		if _, err = os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("retry leaked")
		}
	}
}

// This native miniature executable proves Go's conversion boundary; it is not a WB authority witness.
func TestNativeCoverageActualConversionAndStrictUnion(t *testing.T) {
	t.Parallel()
	module := nativeModule(t)
	binary := filepath.Join(t.TempDir(), "native")
	build := exec.Command("go", "build", "-covermode=atomic", "-o", binary, ".")
	build.Dir = module
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build miniature: %s %v", output, err)
	}
	raw := t.TempDir()
	command := exec.Command(binary)
	command.Env = append(os.Environ(), "GOCOVERDIR="+raw)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native miniature: %s %v", output, err)
	}
	entries, err := os.ReadDir(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateNativeCoverageArtifacts(entries); err != nil {
		t.Fatal(err)
	}
	// Derive the exact producer tuple from supported conversion, never from guessed columns.
	baselineNative := filepath.Join(t.TempDir(), "native.cov")
	if output, err := runWithEnv(context.Background(), nil, module, "go", "tool", "covdata", "textfmt", "-i="+raw, "-o="+baselineNative); err != nil {
		t.Fatalf("convert fixture metadata: %s %v", output, err)
	}
	_, nativeBlocks, err := readCoverageProfile(baselineNative)
	if err != nil || len(nativeBlocks) != 1 {
		t.Fatalf("native tuple %v %v", nativeBlocks, err)
	}
	nativeBlock := nativeBlocks[0]
	for _, tc := range []string{"union", "mode", "identity", "conversion", "canceled", "missing-profile", "missing-native-profile"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			profile := filepath.Join(t.TempDir(), "parent.cov")
			mode := "atomic"
			statements := nativeBlock.statements
			if tc == "mode" {
				mode = "set"
			}
			if tc == "identity" {
				statements = nativeBlock.statements + 1
			}
			nativeWrite(t, profile, fmt.Sprintf("mode: %s\n%s %d 0\nexample.test/nativecoverage/other.go:1.1,1.2 1 1\n", mode, nativeBlock.location, statements))
			if tc == "missing-profile" {
				if err := os.Remove(profile); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var owned string
			operation := nativeCoverageAttempt{temporaryRoot: t.TempDir(), run: func(ctx context.Context, env []string, dir, name string, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "test" {
					owned = nativeCoverageValue(env, "WB_TEST_NATIVE_COVERDIR")
					for _, entry := range entries {
						data, err := os.ReadFile(filepath.Join(raw, entry.Name()))
						if err != nil {
							t.Fatal(err)
						}
						if tc == "conversion" {
							data = []byte("malformed")
						}
						if err := os.WriteFile(filepath.Join(owned, entry.Name()), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return "Go test succeeded", nil
				}
				if tc == "missing-native-profile" {
					return "conversion controlled result", nil
				}
				out, err := runWithEnv(ctx, env, dir, name, args...)
				if tc == "canceled" {
					cancel()
				}
				return out, err
			}}
			out, err := operation.execute(ctx, module, nil, profile, []string{"test"})
			if tc == "union" {
				if err != nil {
					t.Fatalf("union %s %v", out, err)
				}
				data, err := os.ReadFile(profile)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), fmt.Sprintf("%s %d %d", nativeBlock.location, nativeBlock.statements, nativeBlock.count)) || !strings.Contains(string(data), "other.go:1.1,1.2 1 1") {
					t.Fatalf("lost parent/native accounting:\n%s", data)
				}
			} else if !isNativeCoverageFailure(err) {
				t.Fatalf("expected infrastructure refusal %s: %v", tc, err)
			}
			if _, err = os.Stat(owned); !os.IsNotExist(err) {
				t.Fatal("conversion leaked owned artifacts")
			}
		})
	}
}

func TestNativeCoverageFailedBaselineAndOrdinaryRunner(t *testing.T) {
	t.Parallel()
	module := nativeModule(t)
	record := filepath.Join(t.TempDir(), "record")
	nativeWrite(t, filepath.Join(module, "main_test.go"), `package main
import("testing";"os";"path/filepath")
func TestFailed(t *testing.T){dir:=os.Getenv("WB_TEST_NATIVE_COVERDIR");if err:=os.WriteFile(os.Getenv("ATTEMPT_RECORD"),[]byte(dir),0600);err!=nil{t.Fatal(err)};if err:=os.WriteFile(filepath.Join(dir,"covmeta.failed"),[]byte("discard"),0600);err!=nil{t.Fatal(err)};t.Fatal("baseline failure")}
`)
	profile := filepath.Join(t.TempDir(), "baseline.cov")
	recorder := &redBaseRecorder{}
	output, attempts, err := runGoCoverageCommand(context.Background(), RunOptions{Env: []string{"ATTEMPT_RECORD=" + record}, redBase: recorder}, module, profile, []string{"test", "-coverprofile=" + profile, "."})
	if err != nil || attempts != 1 || !strings.Contains(output, "baseline failure") || recorder.baseline("source") == nil {
		t.Fatalf("baseline %d %v %s", attempts, err, output)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(string(data)); !os.IsNotExist(err) {
		t.Fatal("accepted failed baseline retained native data")
	}
	output, attempts, err = runWithOptions(context.Background(), RunOptions{Env: []string{"GOWORK=off"}}, module, "go", "env", "GOWORK")
	if err != nil || attempts != 1 || strings.TrimSpace(output) != "off" {
		t.Fatalf("ordinary runner %q %d %v", output, attempts, err)
	}
	if output, err := runStdout(context.Background(), module, "go", "env", "GOWORK"); err != nil || strings.TrimSpace(output) != "off" {
		t.Fatalf("default stdout binding %q %v", output, err)
	}
	if _, attempts, err = runGoCoverageCommand(context.Background(), RunOptions{}, module, profile, []string{"test", "-coverpkg"}); attempts != 0 || !isNativeCoverageFailure(err) {
		t.Fatalf("scope preparation %d %v", attempts, err)
	}
}

func TestNativeCoverageShardedEnvironmentAndInfrastructureRefusal(t *testing.T) {
	t.Parallel()
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			t.Parallel()
			module := nativeModule(t)
			marker := filepath.Join(t.TempDir(), "environment")
			nativeWrite(t, filepath.Join(module, "main_test.go"), `package main
import("testing";"os";"path/filepath")
func TestNative(t *testing.T){if os.Getenv("PRIVATE_OVERRIDE")!="preserved"{t.Fatal("explicit environment lost")};if err:=os.WriteFile(os.Getenv("ENV_RECORD"),[]byte("preserved"),0600);err!=nil{t.Fatal(err)};if os.Getenv("PARTIAL_NATIVE")=="true"{if err:=os.WriteFile(filepath.Join(os.Getenv("WB_TEST_NATIVE_COVERDIR"),"covmeta.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),[]byte("incomplete"),0600);err!=nil{t.Fatal(err)}}}
`)
			profile := filepath.Join(t.TempDir(), "shard.cov")
			options := RunOptions{GoTestPackages: []string{"."}, GoTestShards: 2, GoShardPackages: []string{"."}, Env: []string{"PRIVATE_OVERRIDE=preserved", "ENV_RECORD=" + marker, "PARTIAL_NATIVE=" + fmt.Sprint(refused)}}
			_, _, err := runCoverageWithOptions(context.Background(), options, module, profile)
			if refused {
				if !isNativeCoverageFailure(err) {
					t.Fatalf("partial shard accepted %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "preserved" {
				t.Fatalf("caller env %s %v", data, err)
			}
		})
	}
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{IncludeE2E: true, GoTestPackages: []string{"-invalid"}}, "", ""); err == nil {
		t.Fatal("included native scope refusal lost")
	}
}

func TestNativeCoverageTaggedDiscoveryUsesActualEnvironment(t *testing.T) {
	t.Parallel()
	module := nativeModule(t)
	if err := os.Mkdir(filepath.Join(module, "tagged"), 0700); err != nil {
		t.Fatal(err)
	}
	nativeWrite(t, filepath.Join(module, "tagged/tagged.go"), "//go:build nativefixture\n\npackage tagged\nfunc Value() int {return 1}\n")
	nativeWrite(t, filepath.Join(module, "tagged/tagged_test.go"), "//go:build nativefixture\n\npackage tagged\nimport \"testing\"\nfunc TestTagged(t *testing.T){if Value()!=1{t.Fatal(\"value\")}}\n")
	profile := filepath.Join(t.TempDir(), "tagged.cov")
	options := RunOptions{GoTestPackages: []string{"./..."}, GoShardPackages: []string{"./tagged"}, GoTestShards: 2, Env: []string{"GOFLAGS=-tags=nativefixture"}, coverPackages: []string{"./..."}}
	if out, _, err := runCoverageWithOptions(context.Background(), options, module, profile); err != nil {
		t.Fatalf("tag discovery %v\n%s", err, out)
	}
	data, err := os.ReadFile(profile)
	if err != nil || !strings.Contains(string(data), "/tagged/tagged.go:") {
		t.Fatalf("tagged profile %s %v", data, err)
	}
	// An explicit invalid instrumentation scope must fail discovery before attempts.
	options.coverPackages = []string{"./missing"}
	if _, attempts, err := runCoverageWithOptions(context.Background(), options, module, profile); attempts != 0 || !isNativeCoverageFailure(err) {
		t.Fatalf("scope refusal %d %v", attempts, err)
	}
}
