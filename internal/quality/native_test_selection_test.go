package quality

import (
	"path/filepath"
	"strings"
	"testing"
)

const nativeHelperFixture = `//go:build e2e
package fixture
import ("testing"; "os"; "os/exec")
func TestCombinedCaptureHelperProcess(t *testing.T) { if os.Getenv("WB_RUNNER_COMBINED_HELPER") != "1" { return } }
func TestE2EParent(t *testing.T) { exec.Command(os.Args[0], "-test.run=^TestCombinedCaptureHelperProcess$") }
`

func TestNativeSelectionUsesBuildConstraintsAndTestSignatures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, nativeHelperFile), nativeHelperFixture)
	writeQualityFile(t, filepath.Join(root, "pkg", "native_test.go"), `//go:build (darwin || linux) && e2e
package fixture
import ( tt "testing"; "other" )
type receiver struct{}
func TestE2ESelected(t *tt.T) {}
func TestContractSelected(t *tt.T) {}
func TestE2EEmptyResults(t *tt.T) () {}
func TestUnselected(t *tt.T) {}
func TestOrchE2EUnselected(t *tt.T) {}
func (receiver) TestMethod(t *tt.T) {}
func TestMain(t *tt.M) {}
func Helper(t *tt.T) {}
func Testlowercase(t *tt.T) {}
func TestE2EReturns(t *tt.T) int { return 0 }
func TestE2EGeneric[T any](t *tt.T) {}
func TestE2EMultiple(t, u *tt.T) {}
func TestE2ETwoFields(t *tt.T, n int) {}
func TestE2ENotPointer(t tt.T) {}
func TestE2EWrongType(t *tt.B) {}
func TestE2EWrongPackage(t *other.T) {}
func TestE2EUnqualified(t *T) {}
func TestE2ENoParams() {}
`)
	writeQualityFile(t, filepath.Join(root, "pkg", "dot_test.go"), `//go:build linux && e2e
package fixture
import . "testing"
func TestDotUnselected(t *T) {}
func TestE2EDot(t *T) {}
func TestE2EQualifiedDot(t *testing.T) {}
`)
	for _, dir := range []string{"vendor", "node_modules", ".hidden", ".git"} {
		writeQualityFile(t, filepath.Join(root, dir, "ignored_test.go"), "invalid go source")
	}
	writeQualityFile(t, filepath.Join(root, "pkg", "default_test.go"), "//go:build e2e || linux\npackage fixture\nimport \"testing\"\nfunc TestDefault(t *testing.T) {}")
	writeQualityFile(t, filepath.Join(root, "pkg", "production.go"), "invalid non-test source")
	writeQualityFile(t, filepath.Join(root, "pkg", "missing_import_test.go"), "//go:build e2e\npackage fixture\nfunc TestNoTestingImport(t *testing.T) {}")
	writeQualityFile(t, filepath.Join(root, "pkg", "blank_import_test.go"), "//go:build e2e\npackage fixture\nimport _ \"testing\"\nfunc TestBlankImport(t *testing.T) {}")
	writeQualityFile(t, filepath.Join(root, "aliases", "local_test.go"), `//go:build e2e
package fixture
import "testing"
type T = testing.T
func TestLocalAliasUnselected(t *T) {}
`)
	writeQualityFile(t, filepath.Join(root, "aliases", "cross_test.go"), `//go:build e2e
package fixture
func TestCrossFileAliasUnselected(t *T) {}
`)
	problems, err := FindNativeTestSelectionProblems(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 7 || !strings.Contains(strings.Join(problems, "\n"), "TestUnselected") || !strings.Contains(strings.Join(problems, "\n"), "TestOrchE2EUnselected") || !strings.Contains(strings.Join(problems, "\n"), "TestDotUnselected") || !strings.Contains(strings.Join(problems, "\n"), "TestLocalAliasUnselected") || !strings.Contains(strings.Join(problems, "\n"), "TestCrossFileAliasUnselected") {
		t.Fatalf("native selection problems = %v", problems)
	}
}

func TestNativeSelectionRejectsStaleHelperExceptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, source string }{
		{"live", nativeHelperFixture},
		{"live os alias", strings.ReplaceAll(strings.ReplaceAll(nativeHelperFixture, "\"os\"", "nativeOS \"os\""), "os.", "nativeOS.")},
		{"missing declaration", strings.ReplaceAll(nativeHelperFixture, "TestCombinedCaptureHelperProcess(t", "HelperOnly(t")},
		{"missing marker", strings.ReplaceAll(nativeHelperFixture, "WB_RUNNER_COMBINED_HELPER", "wrong-marker")},
		{"missing caller", strings.ReplaceAll(nativeHelperFixture, "TestE2EParent", "NotATest")},
		{"wrong selector", strings.ReplaceAll(nativeHelperFixture, "-test.run=^TestCombinedCaptureHelperProcess$", "-test.run=^Other$")},
		{"external executable", strings.ReplaceAll(nativeHelperFixture, "os.Args[0]", "\"git\"")},
		{"nonzero argument", strings.ReplaceAll(nativeHelperFixture, "os.Args[0]", "os.Args[1]")},
		{"wrong field", strings.ReplaceAll(nativeHelperFixture, "os.Args[0]", "os.Environ[0]")},
		{"wrong receiver", strings.ReplaceAll(nativeHelperFixture, "os.Args[0]", "other.Args[0]")},
		{"marker wrong receiver", strings.ReplaceAll(nativeHelperFixture, "os.Getenv", "other.Getenv")},
		{"marker wrong call", strings.ReplaceAll(nativeHelperFixture, "os.Getenv", "os.Other")},
		{"marker expression", strings.ReplaceAll(nativeHelperFixture, "os.Getenv(\"WB_RUNNER_COMBINED_HELPER\")", "os.Getenv(marker)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeQualityFile(t, filepath.Join(root, nativeHelperFile), tc.source)
			problems, err := FindNativeTestSelectionProblems(root)
			if err != nil {
				t.Fatal(err)
			}
			if (len(problems) == 0) != strings.HasPrefix(tc.name, "live") {
				t.Fatalf("helper validation = %v", problems)
			}
		})
	}
}

func TestNativeSelectionReportsParseAndRootErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "invalid_test.go"), "invalid go source")
	if _, err := FindNativeTestSelectionProblems(root); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("parse error = %v", err)
	}
	if _, err := FindNativeTestSelectionProblems(filepath.Join(root, "missing")); err == nil || !strings.Contains(err.Error(), "walk") {
		t.Fatalf("walk error = %v", err)
	}
}

func TestNativeWorkflowSelectionRejectsDriftAndMissingRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"quoted", "jobs:\n  native:\n    steps:\n      - run: go test -tags e2e -run '" + NativeGoTestSelector + "' ./...", true},
		{"equals", "jobs:\n  native:\n    steps:\n      - run: go test -tags=e2e -run=" + NativeGoTestSelector + " ./...", true},
		{"double quoted", "jobs:\n  native:\n    steps:\n      - run: go test -tags=e2e -run=\"" + NativeGoTestSelector + "\" ./...", true},
		{"drift", "jobs:\n  native:\n    steps:\n      - run: go test -tags e2e -run '^Test' ./...", false},
		{"good first bad last", "jobs:\n  native:\n    steps:\n      - run: go test -tags e2e -run '" + NativeGoTestSelector + "' -run '^Test' ./...", false},
		{"bad first good last", "jobs:\n  native:\n    steps:\n      - run: go test -tags e2e -run '^Test' -run='" + NativeGoTestSelector + "' ./...", false},
		{"missing selector", "jobs:\n  native:\n    steps:\n      - run: go test -tags e2e ./...", false},
		{"not native", "jobs:\n  native:\n    steps:\n      - run: go test ./...\n      - run: echo go test -tags=e2e", false},
		{"parse error", "jobs: [", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateNativeWorkflowSelector([]byte(tc.data)); (err == nil) != tc.valid {
				t.Fatalf("workflow validation = %v, valid=%t", err, tc.valid)
			}
		})
	}
}

func TestFindUnitTierMatchesSortsByFileAndLine(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "z_test.go"), `package fixture
import "os/exec"
func TestZ() {
 exec.Command("git")
 exec.Command("gh")
}
`)
	writeQualityFile(t, filepath.Join(root, "a_test.go"), `package fixture
import "os/exec"
func TestA() { exec.Command("git") }
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 || matches[0].File != "a_test.go" || matches[1].File != "z_test.go" || matches[2].File != "z_test.go" || matches[1].Line >= matches[2].Line {
		t.Fatalf("ordered unit-tier findings = %v", matches)
	}
}
