package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCoveragePathResolutionErrorsRemainVisible(t *testing.T) {
	t.Parallel()
	failure := &os.PathError{Op: "getwd", Err: os.ErrNotExist}
	if path, remove, err := coverageProfilePathWithAbs("retained", nil, func(path string) (string, error) {
		if path != "retained" {
			t.Fatalf("retained=%q", path)
		}
		return "", failure
	}); !errors.Is(err, failure) || path != "" || remove {
		t.Fatalf("profile=%q remove=%v err=%v", path, remove, err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\nuse ./module\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if modules, err := goModulesWithAbs(root, func(path string) (string, error) {
		if path != filepath.Join(root, "module") {
			t.Fatalf("module=%q", path)
		}
		return "", failure
	}); !errors.Is(err, failure) || modules != nil {
		t.Fatalf("modules=%v err=%v", modules, err)
	}
}

func TestCampaignNamesSortSameLineDeclarations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := "package fixture\nimport \"testing\"\nfunc TestDqCovZulu(t *testing.T) {}; func TestDqCovAlpha(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(root, "ordinary_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	matches, err := FindCampaignTestNameMatches(root)
	if err != nil || len(matches) != 2 || matches[0].Line != matches[1].Line || matches[0].Name != "TestDqCovAlpha" || matches[1].Name != "TestDqCovZulu" {
		t.Fatalf("matches=%+v err=%v", matches, err)
	}
}

func TestChangedPackagesPathResolutionErrorPreservesIdentity(t *testing.T) {
	t.Parallel()
	failure := &os.PathError{Op: "getwd", Err: os.ErrNotExist}
	got, err := changedPackagesWithAbs(context.Background(), "relative", "target", func(path string) (string, error) {
		if path != "relative" {
			t.Fatalf("working directory=%q", path)
		}
		return "", failure
	})
	if err != failure || !reflect.DeepEqual(got, ChangedPackagesResult{}) {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}
