//go:build e2e

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EScopedCoverageNewAndDeletedPackages(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"new", "deleted", "invalid-profile-destination"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			repo := newRatchetFixtureRepo(t)
			repo.writeFile("old/old.go", "package old\nfunc Value() int { return 1 }\n")
			repo.writeFile("old/old_test.go", "package old\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=1 { t.Fatal(Value()) } }\n")
			base := repo.commitAll("base")
			if scenario == "new" {
				repo.writeFile("new/new.go", "package new\nfunc Value() int { return 2 }\n")
				repo.writeFile("new/new_test.go", "package new\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=2 { t.Fatal(Value()) } }\n")
			} else {
				if err := os.RemoveAll(filepath.Join(repo.dir, "old")); err != nil {
					t.Fatal(err)
				}
			}
			repo.commitAll("change")
			profile := filepath.Join(t.TempDir(), "profile.cov")
			if scenario == "invalid-profile-destination" {
				profile = filepath.Join(repo.dir, "go.mod", "profile.cov")
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"coverage", repo.dir, "--changed", "--affected-packages", "--target", base, "--coverage-profile", profile, "--baseline-file", "unreadable-full-baseline.json", "--format", "json", "--non-interactive"}, &stdout, &stderr)
			if scenario == "invalid-profile-destination" {
				if code == 0 {
					t.Fatal("profile write failure passed")
				}
				return
			}
			if code != 0 || !strings.Contains(stdout.String(), "changed_scope") {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			contents, err := os.ReadFile(profile)
			if err != nil || !strings.HasPrefix(string(contents), "mode: ") {
				t.Fatalf("profile=%q err=%v", contents, err)
			}
		})
	}
}
