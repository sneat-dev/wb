package deps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestFleetGraphRefusesUnresolvableCanonicalRoots(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"go", "npm"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var err error
			repositories := []Repository{{Slug: "acme/app"}}
			if kind == "go" {
				_, err = discoverGoFleetGraph(context.Background(), repositories, orchestrate.Options{}, goGraphDiscoveryPolicy{}, nil)
			} else {
				_, err = discoverNpmFleetGraph(context.Background(), repositories, orchestrate.Options{}, npmGraphDiscoveryPolicy{}, nil)
			}
			if err == nil || !strings.Contains(err.Error(), "projects root is required") {
				t.Fatalf("invalid canonical root accepted: %v", err)
			}
		})
	}
	if _, ok := resolveDuplicateCloneModuleDeclaration(context.Background(), []goFleetModule{{Path: "example.com/sdk", Repository: "acme/app"}}, orchestrate.Options{}); ok {
		t.Fatal("duplicate provider accepted without a canonical root")
	}
}

func TestModuleGraphScratchPublicationRetainsNativeErrors(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"go.mod", "go.sum"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.24\n")
			writeTestFile(t, filepath.Join(root, "go.sum"), "example.com/sdk v1.0.0 h1:test\n")
			scratch := filepath.Join(t.TempDir(), "owned")
			entries, err := resolveModuleGraphWithScratch(context.Background(), root, Options{}, func(_, pattern string) (string, error) {
				if pattern != "wb-go-directive-*" {
					t.Fatalf("scratch pattern=%s", pattern)
				}
				if err := os.Mkdir(scratch, 0o700); err != nil {
					return "", err
				}
				if err := os.Mkdir(filepath.Join(scratch, name), 0o700); err != nil {
					return "", err
				}
				return scratch, nil
			})
			if err == nil || len(entries) != 0 {
				t.Fatalf("entries=%+v err=%v", entries, err)
			}
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != filepath.Join(scratch, name) {
				t.Fatalf("wrong native publication failure: %v", err)
			}
			if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned scratch leaked: %v", err)
			}
		})
	}
}

func TestDriftGroupsHaveOneDeterministicallyOrderedEntryPerDependency(t *testing.T) {
	t.Parallel()
	repos := []DriftRepository{{Repository: "acme/a", Dependencies: []DriftDependency{{Dependency: "z", Declared: VersionEvidence{Value: "1.0.0"}}, {Dependency: "a", Declared: VersionEvidence{Value: "2.0.0"}}}}, {Repository: "acme/b", Dependencies: []DriftDependency{{Dependency: "a", Declared: VersionEvidence{Value: "3.0.0"}}}}}
	groups := classifyDriftGroups(repos, DriftOptions{Ecosystem: EcosystemNPM}, time.Time{})
	if len(groups) != 2 || groups[0].Dependency != "a" || groups[1].Dependency != "z" || groups[0].Classification != DriftDivergent {
		t.Fatalf("groups=%+v", groups)
	}
}

func TestPeerInspectionRetainsAbsoluteResolutionFailure(t *testing.T) {
	t.Parallel()
	cause := errors.New("working directory unavailable")
	calls := 0
	report, err := inspectPeersWithAbsolute(context.Background(), PeerOptions{Package: "sdk", Against: " relative "}, func(path string) (string, error) {
		calls++
		if path != "relative" {
			t.Fatalf("resolution input=%q", path)
		}
		return "", cause
	})
	if !errors.Is(err, cause) || calls != 1 || len(report.Peers) != 0 {
		t.Fatalf("report=%+v err=%v calls=%d", report, err, calls)
	}
}
