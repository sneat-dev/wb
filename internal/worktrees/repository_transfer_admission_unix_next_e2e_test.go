//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // native fixture and HOME namespace failure use process-wide environment.
func TestE2ERepositoryTransferAdmissionRetainsNativeHomeResolutionErrors(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	fixture.cloneDestination(t)
	badHome := t.TempDir()
	if err := os.Symlink(".wb", filepath.Join(badHome, ".wb")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", badHome)
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	_, nativeCause := wbhome.Root(options.ProjectsRoot)
	if nativeCause == nil {
		t.Fatal("native home loop unexpectedly resolved")
	}
	result, err := RelocateRepository(context.Background(), options)
	if err == nil || err.Error() != nativeCause.Error() || result.Eligible || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("native planninghome=%+v %v", result, err)
	}
	receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options)
	if err == nil || err.Error() != nativeCause.Error() || receipts != nil {
		t.Fatalf("native finalizationhome=%v %v", receipts, err)
	}
	if _, err := os.Stat(fixture.canonical); err != nil {
		t.Fatalf("home refusal moved source: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(badHome, ".wb")); err != nil || target != ".wb" {
		t.Fatalf("home evidence changed=%q %v", target, err)
	}
}

//nolint:paralleltest // each existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionRefusesNativePathResolutionDrift(t *testing.T) {
	for _, second := range []bool{false, true} {
		name := "source path"
		if second {
			name = "destination path"
		}
		//nolint:paralleltest // the existing native fixture sets process-wide environment.
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			sourceHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			saved := fixture.projectsRoot + "-retained"
			changed := false
			corrupt := func() {
				if err := os.Rename(fixture.projectsRoot, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(fixture.projectsRoot), fixture.projectsRoot); err != nil {
					t.Fatal(err)
				}
				changed = true
			}
			restore := func() {
				if changed {
					if err := os.Remove(fixture.projectsRoot); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(saved, fixture.projectsRoot); err != nil {
						t.Fatal(err)
					}
					changed = false
				}
			}
			t.Cleanup(restore)
			calls := 0
			var nativeCause error
			resolve := func(root, repository string) (string, error) {
				calls++
				if !second && calls == 1 {
					corrupt()
				}
				path, err := CanonicalRepositoryPath(root, repository)
				if err != nil {
					nativeCause = err
				}
				if second && calls == 1 && err == nil {
					corrupt()
				}
				return path, err
			}
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
			result, err := relocateRepositoryWithReads(context.Background(), options, resolve, gitRawOutput)
			if nativeCause == nil || !errors.Is(err, nativeCause) || result.Applied || result.Eligible || len(result.ReceiptPaths) != 0 {
				t.Fatalf("pathresolution=%+v %v", result, err)
			}
			wantCalls := 1
			if second {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("admission order resolvercalls=%d want%d", calls, wantCalls)
			}
			wantSource := ""
			if second {
				wantSource = fixture.canonical
			}
			if result.SourceDir != wantSource || result.DestinationDir != "" {
				t.Fatalf("partial path evidence=%+v", result)
			}
			restore()
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != sourceHead {
				t.Fatalf("path refusal changed source=%s", got)
			}
		})
	}
}

//nolint:paralleltest // the existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionNofollowRootAliasRetainsSource(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	alias := filepath.Join(t.TempDir(), "projects-alias")
	if err := os.Symlink(fixture.projectsRoot, alias); err != nil {
		t.Fatal(err)
	}
	sourceHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	options := RepositoryRelocateOptions{ProjectsRoot: alias, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	result, err := RelocateRepository(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "open projects root") || !result.Eligible || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("nofollow application=%+v %v", result, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatalf("nofollow refusal moved source=%s", got)
	}
	if target, err := os.Readlink(alias); err != nil || target != fixture.projectsRoot {
		t.Fatalf("alias evidence changed=%q %v", target, err)
	}
}
