//go:build e2e

package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EModuleNormalizationPreservesCommandAndReadFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"parse after tidy", "drop unused replacement", "read after drop", "audit after drop"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := migCovWriteGoMod(t, dir, "module example.com/app\n\ngo 1.27\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ./dep\n")
			dep := filepath.Join(dir, "dep")
			migCovWriteGoMod(t, dep, "module example.com/dep\n\ngo 1.27\n")
			hit, tidied := false, false
			run := func(root, name string, args ...string) (string, error) {
				if strings.Join(args, " ") == "mod tidy" {
					out, err := runIn(root, name, args...)
					if err != nil {
						return out, err
					}
					tidied = true
					if stage == "parse after tidy" {
						if err := os.WriteFile(path, []byte("invalid go.mod"), 0600); err != nil {
							t.Fatal(err)
						}
						hit = true
					}
					return out, err
				}
				if tidied && strings.HasPrefix(strings.Join(args, " "), "mod edit -dropreplace=") {
					if stage == "drop unused replacement" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						hit = true
						return runIn(root, name, args...)
					}
					out, err := runIn(root, name, args...)
					if err != nil {
						return out, err
					}
					hit = true
					if stage == "read after drop" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					}
					if stage == "audit after drop" {
						if err := os.WriteFile(path, []byte("invalid go.mod"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return out, err
				}
				return runIn(root, name, args...)
			}
			result, err := updateGoModuleWithRun(dir, Spec{}, "example.com/app", map[string]string{"example.com/dep": dep}, run)
			if !hit || err == nil || result.Changed {
				t.Fatalf("hit=%v result=%+v error=%v", hit, result, err)
			}
			if stage == "read after drop" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native read cause=%v", err)
			}
		})
	}
}

func TestE2EModuleFinalizationPreservesEditAndReadFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"drop", "read after edit", "read after tidy", "audit after tidy"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			dep := filepath.Join(dir, "dep")
			path := migCovWriteGoMod(t, dir, "module example.com/app\n\ngo 1.27\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ./dep\n")
			migCovWriteGoMod(t, dep, "module example.com/dep\n\ngo 1.27\n")
			hit := false
			run := func(root, name string, args ...string) (string, error) {
				command := strings.Join(args, " ")
				if stage == "drop" && strings.HasPrefix(command, "mod edit -dropreplace=") {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					hit = true
					return runIn(root, name, args...)
				}
				out, err := runIn(root, name, args...)
				if err != nil {
					return out, err
				}
				if stage == "read after edit" && strings.HasPrefix(command, "mod edit -require=") {
					hit = true
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				if command == "mod tidy" && (stage == "read after tidy" || stage == "audit after tidy") {
					hit = true
					var err error
					if stage == "read after tidy" {
						err = os.Remove(path)
					} else {
						err = os.WriteFile(path, []byte("invalid go.mod"), 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				return out, err
			}
			result, err := finalizeGoModuleWithRun(dir, Spec{GoModuleReleases: []GoModuleRelease{{Path: "example.com/dep", Version: "v1.0.0"}}}, "example.com/app", map[string]string{"example.com/dep": dep}, nil, run)
			if !hit || err == nil || result.Changed {
				t.Fatalf("hit=%v result=%+v error=%v", hit, result, err)
			}
			if strings.HasPrefix(stage, "read") && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native read cause=%v", err)
			}
		})
	}
}

func TestE2ECampaignPlanningRetainsCanonicalObservationFailures(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	migCovWriteGoMod(t, source, "module github.com/acme/app\n\ngo 1.27\n")
	root := t.TempDir()
	canonical := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := migCovTextReplaceSpec("canonical")
	spec.Steps[0].From = "github.com/acme/app/pkg"
	plan, err := planCampaign(spec, source, CampaignOptions{GitHubDir: root, Ref: "main"})
	if plan != nil || err == nil {
		t.Fatalf("plan=%v error=%v", plan, err)
	}
	if got := mustReadCampaignFile(t, canonical); got != "retained" {
		t.Fatalf("canonical evidence=%q", got)
	}
}

func TestE2EResumeDiscoveryRetainsCanonicalAndRegistryFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"canonical stat", "registration"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			source := t.TempDir()
			migCovWriteGoMod(t, source, "module github.com/acme/app\n\ngo 1.27\n")
			root := t.TempDir()
			if stage == "canonical stat" {
				if err := os.WriteFile(filepath.Join(root, "github.com"), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(root, "acme", "app"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			got, err := campaignDiscoveryRoot(Spec{ID: "resume"}, source, CampaignOptions{GitHubDir: root, Resume: true})
			if got != "" || err == nil {
				t.Fatalf("root=%q error=%v", got, err)
			}
			if stage == "registration" && !strings.Contains(err.Error(), "worktree list") {
				t.Fatalf("native registration error=%v", err)
			}
		})
	}
}
