package defaultbranch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestDefaultBranchPagesFailureMatrix(t *testing.T) {
	t.Parallel()
	service := New()

	for _, test := range []struct {
		name, failure, want string
		automatic           bool
	}{
		{name: "metadata read", failure: "read:repos/acme/app#1", want: "verify default branch before Pages migration"},
		{name: "head read", failure: "read:repos/acme/app/branches/main#1", want: "verify default head before Pages migration"},
		{name: "malformed before", failure: "decode-before", want: "decode Pages source before migration"},
		{name: "malformed after", failure: "decode-after", want: "decode Pages source after migration"},
		{name: "Pages read", failure: "read:repos/acme/app/pages#1", want: "read Pages source before migration"},
		{name: "automatic old ref unavailable", failure: "read:repos/acme/app/git/ref/heads/master#1", want: "could not prove old master ref is absent", automatic: true},
		{name: "automatic old ref still exists", failure: "old-ref-still-exists", want: "old master ref still exists", automatic: true},
		{name: "automatic transition receipt", failure: "checkpoint#1", want: "persist verified automatic Pages transition", automatic: true},
		{name: "pending migration receipt", failure: "checkpoint#1", want: "persist pending Pages migration"},
		{name: "Pages update rejected", failure: "put#1", want: "update Pages source"},
		{name: "accepted response receipt", failure: "checkpoint#2", want: "persist Pages migration response"},
		{name: "post-update read", failure: "read:repos/acme/app/pages#2", want: "verify Pages source migration"},
		{name: "post-update source differs", failure: "post-mismatch", want: "post-write verification did not preserve legacy build type"},
		{name: "verified migration receipt", failure: "checkpoint#3", want: "persist verified Pages migration"},
	} {
		//nolint:paralleltest // Rows replace callbacks or provider state on the same parent-owned policy Service.
		t.Run(test.name, func(t *testing.T) {
			originalRead, originalExecute := service.deps.Read, service.deps.Execute
			t.Cleanup(func() { service.deps.Read, service.deps.Execute = originalRead, originalExecute })
			calls := make(map[string]int)
			injected := false
			failAt := func(key string) bool {
				calls[key]++
				if test.failure == fmt.Sprintf("%s#%d", key, calls[key]) {
					injected = true
					return true
				}
				return false
			}
			updated := false
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				if failAt("read:" + endpoint) {
					return nil, errors.New("injected endpoint failure")
				}
				switch endpoint {
				case "repos/acme/app":
					return []byte(`{"id":1,"default_branch":"main"}`), nil
				case "repos/acme/app/branches/main":
					return []byte(`{"commit":{"sha":"same"}}`), nil
				case "repos/acme/app/pages":
					if (test.failure == "decode-before" && !updated) || (test.failure == "decode-after" && updated) {
						injected = true
						return []byte("{"), nil
					}
					Branch := "master"
					if test.automatic || updated {
						Branch = "main"
					}
					if updated && test.failure == "post-mismatch" {
						injected = true
						Branch = "master"
					}
					return []byte(`{"build_type":"legacy","source":{"branch":"` + Branch + `","path":"/"}}`), nil
				case "repos/acme/app/git/ref/heads/master":
					if test.failure == "old-ref-still-exists" {
						injected = true
						return []byte(`{"ref":"refs/heads/master"}`), nil
					}
					return nil, errors.New("HTTP 404")
				default:
					t.Fatalf("unmodelled endpoint %q", endpoint)
					return nil, nil
				}
			}
			service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				if got := strings.Join(args, " "); got != "api --method PUT repos/acme/app/pages -f source[branch]=main -f source[path]=/" {
					t.Fatalf("unmodelled Pages mutation %q", got)
				}
				if failAt("put") {
					return githubobserver.CommandResponse{Err: errors.New("injected Pages update failure")}
				}
				updated = true
				return githubobserver.CommandResponse{}
			}
			repo := Repository{Repository: "acme/app", Desired: "main", ObservedDefault: "master", OldHead: "same", Disposition: "drift", PagesBefore: &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"}}
			result := service.applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, func(Repository) error {
				if failAt("checkpoint") {
					return errors.New("injected receipt failure")
				}
				return nil
			})
			if !injected || result.Disposition != "error" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("failure %q: reached=%t result=%+v, want %q (calls=%v)", test.failure, injected, result, test.want, calls)
			}
		})
	}
}

func TestDefaultBranchPagesMigrationSkipsRepositoriesWithoutPages(t *testing.T) {
	t.Parallel()
	service := New()

	planned := Repository{Repository: "acme/app", Desired: "main", Disposition: "compliant"}
	called := false
	actual := service.applyDefaultBranchPagesWithCheckpoint(context.Background(), planned, func(Repository) error {
		called = true
		return errors.New("should not persist")
	})
	if called || !reflect.DeepEqual(actual, planned) {
		t.Fatalf("repository without Pages changed: actual=%+v checkpoint=%t", actual, called)
	}
}

func TestDefaultBranchPagesAtDesiredClassifiesObservedSource(t *testing.T) {
	t.Parallel()
	service := New()

	for _, test := range []struct {
		name, body, disposition, phase, wantError string
		readErr                                   error
	}{
		{name: "Pages absent", readErr: errors.New("HTTP 404"), disposition: "compliant"},
		{name: "Pages unavailable", readErr: errors.New("HTTP 503"), disposition: "error", wantError: "inspect Pages source"},
		{name: "Pages response malformed", body: "{", disposition: "blocked", wantError: "decode Pages source"},
		{name: "legacy source follows default", body: `{"build_type":"legacy","source":{"branch":"main","path":"/docs"}}`, disposition: "compliant"},
		{name: "legacy source still on master", body: `{"build_type":"legacy","source":{"branch":"master","path":"/docs"}}`, disposition: "drift", phase: "unfinished", wantError: "migration remains unfinished"},
	} {
		//nolint:paralleltest // Rows replace callbacks or provider state on the same parent-owned policy Service.
		t.Run(test.name, func(t *testing.T) {
			oldRead := service.deps.Read
			t.Cleanup(func() { service.deps.Read = oldRead })
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				if endpoint != "repos/acme/app/pages" {
					t.Fatalf("unexpected endpoint %q", endpoint)
				}
				return []byte(test.body), test.readErr
			}
			result := Repository{Repository: "acme/app", Desired: "main"}
			service.inspectDefaultBranchPagesAtDesired(context.Background(), &result, RepoMetadata{ID: 1, DefaultBranch: "main"})
			if result.Disposition != test.disposition || result.PagesPhase != test.phase || !strings.Contains(result.Error, test.wantError) {
				t.Fatalf("Pages classification = %+v, want disposition=%q phase=%q error containing %q", result, test.disposition, test.phase, test.wantError)
			}
			if test.disposition == "compliant" && test.readErr == nil && (result.PagesAfter == nil || result.PagesAfter.Branch != "main") {
				t.Fatalf("compliant Pages source lacks verified observation: %+v", result)
			}
			if test.phase == "unfinished" && (result.PagesBefore == nil || result.PagesBefore.Branch != "master") {
				t.Fatalf("unfinished Pages source was not retained: %+v", result)
			}
		})
	}
}

func TestDefaultBranchAutomaticPagesResumeRequiresFreshRemoteProof(t *testing.T) {
	t.Parallel()
	service := New()

	sha := strings.Repeat("a", 40)
	for _, test := range []struct{ name, mode, want string }{
		{"no matching receipt", "no record", "no automatic Pages transition record"},
		{"old ref remains", "old ref", "old source ref still exists"},
		{"old ref state unknown", "old ref error", "could not prove the old source ref is absent"},
		{"Pages source unavailable", "Pages error", "could not read Pages source"},
		{"Pages source malformed", "Pages malformed", "does not prove the automatic transition"},
	} {
		//nolint:paralleltest // Rows replace callbacks or provider state on the same parent-owned policy Service.
		t.Run(test.name, func(t *testing.T) {
			oldRead := service.deps.Read
			t.Cleanup(func() { service.deps.Read = oldRead })
			reads := 0
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				reads++
				switch endpoint {
				case "repos/acme/app/git/ref/heads/master":
					if test.mode == "old ref" {
						return []byte(`{"ref":"refs/heads/master"}`), nil
					}
					if test.mode == "old ref error" {
						return nil, errors.New("HTTP 503")
					}
					return nil, errors.New("HTTP 404")
				case "repos/acme/app/pages":
					if test.mode == "Pages error" {
						return nil, errors.New("HTTP 503")
					}
					if test.mode == "Pages malformed" {
						return []byte("{"), nil
					}
					return []byte(`{"build_type":"legacy","source":{"branch":"main","path":"/"}}`), nil
				default:
					t.Fatalf("unexpected automatic Pages endpoint %q", endpoint)
					return nil, nil
				}
			}
			previous := Repository{Repository: "acme/app", RepositoryID: 1, ObservedDefault: "master", VerifiedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha, Disposition: "error", Error: "Pages source changed after planning; WB will not overwrite it", RenameAccepted: true, PagesBefore: &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"}, PagesPhase: "prepared", Actions: []string{"renamed master to main", "verified default branch and head"}}
			if test.mode == "no record" {
				previous.Repository = "other/app"
			}
			prior := &Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{previous}}
			current := Repository{Repository: "acme/app", RepositoryID: 1, ObservedDefault: "main", Desired: "main", OldHead: sha, Disposition: "compliant"}
			source, head, reason := service.defaultBranchPagesAutomaticResume(context.Background(), prior, current)
			if source != "" || head != "" || !strings.Contains(reason, test.want) {
				t.Fatalf("automatic resume %q = source=%q head=%q reason=%q reads=%d", test.mode, source, head, reason, reads)
			}
			if test.mode == "no record" && reads != 0 {
				t.Fatalf("unmatched receipt reached remote: %d reads", reads)
			}
		})
	}
}
