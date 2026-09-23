package worktrees

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRetiredArchiveTargetUsesDefaultAndUserOverrides(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	defaultTarget, err := ResolveRetiredArchiveTarget("sneat-co")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sneat-co/backstage-retired"; defaultTarget.Repository != want {
		t.Fatalf("default target = %q, want %q", defaultTarget.Repository, want)
	}

	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), `version: 1
retirement:
  archive_repository: organization-retired
  organizations:
    sneat-co:
      archive_repository: backstage-retired
    datatug:
      archive_repository: datatug-graveyard
`)
	for organization, want := range map[string]string{
		"sneat-co": "sneat-co/backstage-retired",
		"datatug":  "datatug/datatug-graveyard",
		"other":    "other/organization-retired",
	} {
		target, err := ResolveRetiredArchiveTarget(organization)
		if err != nil {
			t.Fatalf("resolve %s: %v", organization, err)
		}
		if target.Repository != want {
			t.Fatalf("target for %s = %q, want %q", organization, target.Repository, want)
		}
	}
}

func TestRepositoryTrackedRetirementPolicyIsRejected(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, base := synchronizedBranchConfigBase(t, fixture)
	defer canonical.close()
	commitRepositoryBranchConfig(t, fixture, "version: 1\nretirement:\n  archive_repository: hostile\n", "hostile archive policy")
	base = synchronizedBranchConfigBaseValue(t, fixture, canonical)
	userConfigPath, pathErr := defaultWorktreesConfigPath()
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	_, err := configuredWorktreePlacement(context.Background(), fixture.projectsRoot, canonical, base)
	if err == nil || !strings.Contains(err.Error(), "must not set retirement") || !strings.Contains(err.Error(), userConfigPath) {
		t.Fatalf("tracked retirement policy error = %v", err)
	}
}

func TestPlanRetiredArchivePreflightFailsClosedAndNeverExportsInspectorErrors(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	secret := "token-not-for-output"
	tests := []struct {
		name    string
		inspect RetiredArchiveInspector
		outcome string
		refusal string
	}{
		{
			name: "private", inspect: func(context.Context, string) (RetiredArchiveInspection, error) {
				return RetiredArchiveInspection{Exists: true, Private: true, Repository: "sneat-co/backstage-retired"}, nil
			}, outcome: "planned",
		},
		{
			name: "public", inspect: func(context.Context, string) (RetiredArchiveInspection, error) {
				return RetiredArchiveInspection{Exists: true, Private: false, Repository: "sneat-co/backstage-retired"}, nil
			}, outcome: "refused", refusal: "archive repository is public",
		},
		{
			name: "missing observed identity", inspect: func(context.Context, string) (RetiredArchiveInspection, error) {
				return RetiredArchiveInspection{Exists: true, Private: true}, nil
			}, outcome: "refused", refusal: "archive repository is unavailable",
		},
		{
			name: "missing", inspect: func(context.Context, string) (RetiredArchiveInspection, error) {
				return RetiredArchiveInspection{Exists: false}, nil
			}, outcome: "refused", refusal: "archive repository is missing",
		},
		{
			name: "missing remote", inspect: func(context.Context, string) (RetiredArchiveInspection, error) {
				return RetiredArchiveInspection{}, errors.New("HTTP 404")
			}, outcome: "refused", refusal: "archive repository is missing",
		},
		{
			name: "unavailable", inspect: func(context.Context, string) (RetiredArchiveInspection, error) {
				return RetiredArchiveInspection{}, errors.New(secret)
			}, outcome: "refused", refusal: "archive repository is unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanRetiredArchivePreflight(context.Background(), "sneat-co/app", test.inspect)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Outcome != test.outcome || plan.Refusal != test.refusal {
				t.Fatalf("plan = %#v, want outcome=%q refusal=%q", plan, test.outcome, test.refusal)
			}
			if plan.LocalQuarantine != "preserved" || plan.RemoteRefRename || plan.WorktreeDeletion {
				t.Fatalf("plan changed local-only boundary: %#v", plan)
			}
			if strings.Contains(fmtPlan(t, plan), secret) {
				t.Fatalf("plan exposed inspector secret: %#v", plan)
			}
		})
	}
}

func fmtPlan(t *testing.T, plan RetiredArchivePlan) string {
	t.Helper()
	return strings.Join([]string{plan.SourceRepository, plan.ArchiveRepository, plan.Outcome, plan.Refusal, plan.LocalQuarantine, plan.WorkLogExport}, " ")
}
