package worktreepolicy

import (
	"regexp"
	"strings"
	"testing"
)

func TestDecodePolicyShapesAndBranchValidator(t *testing.T) {
	t.Parallel()
	const path = "policy.yaml"
	valid := func(branch string) bool { return branch == "team/probe" }
	cases := []struct {
		name, yaml, wantError string
		validator             BranchValidator
		wantProbe             bool
	}{
		{"minimal", "version: 1\n", "", valid, false},
		{"full", "version: 1\nworktrees:\n  branch_prefix: team/\n  store: central\n  root: /tmp/worktrees\nretirement:\n  archive_repository: .archive\n  organizations:\n    my-org:\n      archive_repository: repo_name\n", "", valid, true},
		{"empty prefix", "version: 1\nworktrees:\n  branch_prefix: ''\n", "", valid, false},
		{"oversize", strings.Repeat("#", MaxConfigSize+1), "exceeds", valid, false},
		{"malformed", "version: [1\n", "parse worktrees config", valid, false},
		{"unknown field", "version: 1\nunknown: true\n", "parse worktrees config", valid, false},
		{"version", "version: 2\n", "has version 2", valid, false},
		{"multiple documents", "version: 1\n---\nversion: 1\n", "multiple YAML documents", valid, false},
		{"malformed second document", "version: 1\n---\n[\n", "parse worktrees config", valid, false},
		{"prefix whitespace", "version: 1\nworktrees:\n  branch_prefix: ' team/'\n", "surrounding whitespace", valid, false},
		{"prefix slash", "version: 1\nworktrees:\n  branch_prefix: team\n", "must end with /", valid, false},
		{"prefix grammar", "version: 1\nworktrees:\n  branch_prefix: other/\n", "invalid branch_prefix", valid, true},
		{"store whitespace", "version: 1\nworktrees:\n  store: ' central'\n", "store must not have surrounding whitespace", valid, false},
		{"store unknown", "version: 1\nworktrees:\n  store: mystery\n", "store mode", valid, false},
		{"root whitespace", "version: 1\nworktrees:\n  root: ' /tmp'\n", "root must not have surrounding whitespace", valid, false},
		{"root empty", "version: 1\nworktrees:\n  root: ''\n", "root must not be empty", valid, false},
		{"archive repository", "version: 1\nretirement:\n  archive_repository: ' bad'\n", "retirement.archive_repository", valid, false},
		{"organization", "version: 1\nretirement:\n  organizations:\n    .hidden: {}\n", "invalid organization", valid, false},
		{"organization repository", "version: 1\nretirement:\n  organizations:\n    my-org:\n      archive_repository: 'bad/name'\n", "organizations.my-org.archive_repository", valid, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			probed := false
			config, found, err := Decode(path, []byte(tc.yaml), func(branch string) bool {
				probed = true
				return tc.validator(branch)
			})
			if probed != tc.wantProbe {
				t.Fatalf("validator called = %v, want %v", probed, tc.wantProbe)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || found {
					t.Fatalf("found=%v err=%v, want %q", found, err, tc.wantError)
				}
				return
			}
			if err != nil || !found || config.Version != Version {
				t.Fatalf("config=%+v found=%v err=%v", config, found, err)
			}
			if tc.name == "full" {
				if config.Worktrees.BranchPrefix == nil || *config.Worktrees.BranchPrefix != "team/" {
					t.Fatalf("branch prefix = %v, want team/", config.Worktrees.BranchPrefix)
				}
				if config.Worktrees.Store == nil || *config.Worktrees.Store != StoreModeCentral {
					t.Fatalf("store = %v, want %s", config.Worktrees.Store, StoreModeCentral)
				}
				if config.Worktrees.Root == nil || *config.Worktrees.Root != "/tmp/worktrees" {
					t.Fatalf("root = %v, want /tmp/worktrees", config.Worktrees.Root)
				}
				if config.Retirement.ArchiveRepository == nil || *config.Retirement.ArchiveRepository != ".archive" {
					t.Fatalf("archive repository = %v, want .archive", config.Retirement.ArchiveRepository)
				}
				if len(config.Retirement.Organizations) != 1 {
					t.Fatalf("organizations = %+v, want exactly my-org", config.Retirement.Organizations)
				}
				organization, ok := config.Retirement.Organizations["my-org"]
				if !ok || organization.ArchiveRepository == nil || *organization.ArchiveRepository != "repo_name" {
					t.Fatalf("my-org archive repository = %+v, want repo_name", organization)
				}
			}
		})
	}
}

func TestRepositoryPlacementPolicyRejectsEachMachineLocalSetting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"allowed branch prefix", "version: 1\nworktrees:\n  branch_prefix: team/\n", ""},
		{"store", "version: 1\nworktrees:\n  store: central\n", "worktrees.store"},
		{"root", "version: 1\nworktrees:\n  root: /tmp\n", "worktrees.root"},
		{"archive", "version: 1\nretirement:\n  archive_repository: archive\n", "must not set retirement"},
		{"organizations", "version: 1\nretirement:\n  organizations:\n    org: {}\n", "must not set retirement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config, found, err := Decode("policy.yaml", []byte(tc.yaml), func(string) bool { return true })
			if err != nil || !found {
				t.Fatalf("decode: found=%v err=%v", found, err)
			}
			err = RepositoryPlacementPolicy(config, "base-sha", "/user/policy")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "/user/policy")) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSafeSegmentsMatchFacadeGrammar(t *testing.T) {
	t.Parallel()
	organization := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	repository := regexp.MustCompile(`^[.A-Za-z0-9][A-Za-z0-9._-]*$`)
	for _, value := range []string{"", ".", "..", "org", "org.name", "a_b", "a-b", ".hidden", "_bad", "-bad", "a/b", "a b", "é", "A9"} {
		wantOrganization := organization.MatchString(value) && value != "." && value != ".."
		config := "version: 1\nretirement:\n  organizations:\n    " + "'" + value + "': {}\n"
		_, found, err := Decode("policy.yaml", []byte(config), func(string) bool { return true })
		if (err == nil && found) != wantOrganization {
			t.Fatalf("organization %q: found=%v err=%v, want valid=%v", value, found, err, wantOrganization)
		}
		wantRepository := repository.MatchString(value) && value != "." && value != ".."
		if got := ValidateRetiredArchiveRepositoryName(value) == nil; got != wantRepository {
			t.Fatalf("repository %q: got valid=%v, want %v", value, got, wantRepository)
		}
	}
}
