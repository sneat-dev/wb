package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// migCovModuleProxy builds a filesystem module proxy serving exactly one
// module version, so a campaign's publishable manifest step can resolve a
// released dependency without any network access.
func migCovModuleProxy(t *testing.T, modulePath, version string, files map[string]string) string {
	t.Helper()
	source := t.TempDir()
	for name, contents := range files {
		writeCampaignFile(t, filepath.Join(source, name), contents)
	}
	proxy := t.TempDir()
	escapedPath, err := module.EscapePath(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	escapedVersion, err := module.EscapeVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	versionDir := filepath.Join(proxy, escapedPath, "@v")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := modzip.CreateFromDir(&archive, module.Version{Path: modulePath, Version: version}, source); err != nil {
		t.Fatalf("build module zip for %s@%s: %v", modulePath, version, err)
	}
	writes := map[string][]byte{
		escapedVersion + ".zip":  archive.Bytes(),
		escapedVersion + ".mod":  []byte(files["go.mod"]),
		escapedVersion + ".info": []byte(`{"Version":"` + version + `","Time":"2020-01-02T03:04:05Z"}`),
		"list":                   []byte(version + "\n"),
	}
	for name, contents := range writes {
		if err := os.WriteFile(filepath.Join(versionDir, name), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proxy
}

// migCovInstallFakeGH puts a hermetic `gh` on PATH. It answers the pull
// request verbs used by a campaign and the REST reads used to decide whether a
// pull request's checks are green.
func migCovInstallFakeGH(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	writeCampaignFile(t, filepath.Join(binDir, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
mode="${GH_FAKE_MODE:-ok}"
case "$1 $2" in
  "pr list")
    if [ "$mode" = "fail-list" ]; then
      printf '%s\n' 'HTTP 500: server error' >&2
      exit 1
    fi
    exit 0
    ;;
  "pr create")
    if [ "$mode" = "fail-create" ]; then
      printf '%s\n' 'HTTP 422: Validation Failed' >&2
      exit 1
    fi
    repo=$(basename "$(git config --get remote.origin.url)" .git)
    printf 'https://github.com/acme/%s/pull/1\n' "$repo"
    exit 0
    ;;
  "pr merge")
    if [ "$mode" = "fail-merge" ]; then
      printf '%s\n' 'merge conflict' >&2
      exit 1
    fi
    exit 0
    ;;
esac
if [ "$1" = "api" ]; then
  if [ "$mode" = "fail-api" ]; then
    printf '%s\n' 'HTTP 500: server error' >&2
    exit 1
  fi
  case "$2" in
    *"/pulls/"*)
      printf '%s' '{"number":1,"state":"open","head":{"ref":"wb/migrate/campaign-pr","sha":"0123456789abcdef0123456789abcdef01234567"},"base":{"ref":"main","sha":"0123456789abcdef0123456789abcdef01234567"}}'
      ;;
    *"/check-runs"*)
      if [ "$mode" = "fail-checks" ]; then
        printf '%s' '{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"failure","app":{"slug":"other"}}]}'
      else
        printf '%s' '{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success","app":{"slug":"other"}}]}'
      fi
      ;;
    *"/actions/runs"*)
      printf '%s' '{"total_count":0,"workflow_runs":[]}'
      ;;
    *"/status"*)
      printf '%s' '{"total_count":0,"statuses":[]}'
      ;;
    *"/rules/branches/"*)
      printf '%s' '[]'
      ;;
    *"/branches/"*)
      printf '%s' '{"name":"main","protected":false}'
      ;;
    *)
      printf '%s' '{}'
      ;;
  esac
  exit 0
fi
exit 1
`)
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// migCovPRFixture builds a two-repository campaign whose dependency release is
// served by a local module proxy.
func migCovPRFixture(t *testing.T) campaignIntegrationFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "WB Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "wb@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "WB Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "wb@example.test")
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "github", ".wb"))
	remotes := filepath.Join(root, "remotes")

	providerSource := filepath.Join(root, "source", "provider")
	writeCampaignFile(t, filepath.Join(providerSource, "go.mod"), "module github.com/acme/provider\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(providerSource, "provider.go"), "package provider\n\nconst Package = \"github.com/acme/provider/pkg\"\n")
	commitCampaignRepository(t, providerSource, filepath.Join(remotes, "acme", "provider.git"))

	consumerSource := filepath.Join(root, "source", "consumer")
	writeCampaignFile(t, filepath.Join(consumerSource, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n\nreplace github.com/acme/provider => ../provider\n")
	writeCampaignFile(t, filepath.Join(consumerSource, "consumer.go"), "package consumer\n\nimport \"github.com/acme/provider\"\n\nconst Package = \"github.com/acme/provider/pkg\"\nvar _ = provider.Package\n")
	// The published repository carries the same source without the local
	// replacement: the campaign must introduce and then retire it.
	remoteConsumer := filepath.Join(root, "remote-consumer")
	writeCampaignFile(t, filepath.Join(remoteConsumer, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n")
	writeCampaignFile(t, filepath.Join(remoteConsumer, "consumer.go"), "package consumer\n\nimport \"github.com/acme/provider\"\n\nconst Package = \"github.com/acme/provider/pkg\"\nvar _ = provider.Package\n")
	commitCampaignRepository(t, remoteConsumer, filepath.Join(remotes, "acme", "consumer.git"))

	proxy := migCovModuleProxy(t, "github.com/acme/provider", "v1.0.0", map[string]string{
		"go.mod":      "module github.com/acme/provider\n\ngo 1.24\n",
		"provider.go": "package provider\n\nconst Package = \"github.com/acme/provider/pkg\"\n",
	})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GONOSUMDB", "*")

	return campaignIntegrationFixture{
		spec: Spec{
			Format: MigrationFormatV1,
			ID:     "campaign-pr",
			Title:  "Campaign pull request.",
			Steps: []Step{{
				Kind: "text.replace", Language: "go",
				From: "github.com/acme/provider/pkg", To: "github.com/acme/new-provider/pkg",
			}},
			GoModuleReleases: []GoModuleRelease{{Path: "github.com/acme/provider", Version: "v1.0.0"}},
		},
		sourceRoot: consumerSource,
		githubDir:  filepath.Join(root, "github"),
		cloneURL: func(repository string) string {
			return filepath.Join(remotes, strings.TrimPrefix(repository, "github.com/")+".git")
		},
	}
}

func TestMigCovCampaignPRPublishesAndMergesPullRequests(t *testing.T) {
	fixture := migCovPRFixture(t)
	logPath := migCovInstallFakeGH(t)

	report, err := RunCampaign(fixture.spec, fixture.sourceRoot, CampaignOptions{
		GitHubDir: fixture.githubDir,
		Apply:     true,
		PR:        true,
		Merge:     true,
		Verify:    VerifyNone,
		Parallel:  2,
		CloneURL:  fixture.cloneURL,
	})
	if err != nil {
		t.Fatalf("RunCampaign(--pr --merge) = %v", err)
	}
	if report.Status != "applied" {
		t.Fatalf("campaign status = %q", report.Status)
	}
	consumer := campaignRepositoryByName(t, report, "github.com/acme/consumer")
	if consumer.Commit == "" || !consumer.Pushed || !consumer.Merged {
		t.Fatalf("consumer repository report = %+v", consumer)
	}
	if !strings.HasPrefix(consumer.PR, "https://github.com/acme/consumer/pull/") {
		t.Fatalf("consumer pull request = %q", consumer.PR)
	}
	if len(consumer.RequiredChecks) != 1 || consumer.RequiredChecks[0].Name != "check-run:build" || consumer.RequiredChecks[0].Bucket != "pass" {
		t.Fatalf("required checks = %+v, want one passing build check", consumer.RequiredChecks)
	}
	if len(consumer.Modules) != 1 || !consumer.Modules[0].PublishableManifest {
		t.Fatalf("consumer modules = %+v", consumer.Modules)
	}
	goMod := mustReadCampaignFile(t, filepath.Join(consumer.WorktreeDir, "go.mod"))
	if strings.Contains(goMod, "replace github.com/acme/provider") {
		t.Fatalf("published go.mod still pins the campaign worktree:\n%s", goMod)
	}
	if !strings.Contains(goMod, "github.com/acme/provider v1.0.0") {
		t.Fatalf("published go.mod does not pin the released version:\n%s", goMod)
	}

	log := mustReadCampaignFile(t, logPath)
	for _, want := range []string{
		"pr list --head wb/migrate/campaign-pr --base main --state open",
		"pr create --base main --head wb/migrate/campaign-pr",
		"pr merge https://github.com/acme/consumer/pull/1 --merge",
		"api repos/acme/consumer/pulls/1",
		"api repos/acme/consumer/commits/0123456789abcdef0123456789abcdef01234567/check-runs",
		"api repos/acme/consumer/branches/main",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh call log missing %q:\n%s", want, log)
		}
	}
}

func TestMigCovCampaignPRRefusesUnpublishedDependency(t *testing.T) {
	fixture := migCovPRFixture(t)
	migCovInstallFakeGH(t)
	fixture.spec.GoModuleReleases = nil

	_, err := RunCampaign(fixture.spec, fixture.sourceRoot, CampaignOptions{
		GitHubDir: fixture.githubDir,
		Apply:     true,
		PR:        true,
		Verify:    VerifyNone,
		CloneURL:  fixture.cloneURL,
	})
	if err == nil || !strings.Contains(err.Error(), "add go_module_release") {
		t.Fatalf("RunCampaign(--pr without release) = %v", err)
	}
}
