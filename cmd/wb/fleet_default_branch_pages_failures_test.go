package main

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
	for _, test := range []struct {
		name, failure, want string
		automatic           bool
	}{
		{name: "metadata read", failure: "read:repos/acme/app#1", want: "verify default branch before Pages migration"},
		{name: "head read", failure: "read:repos/acme/app/branches/main#1", want: "verify default head before Pages migration"},
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
		t.Run(test.name, func(t *testing.T) {
			originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
			t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				if failAt("read:" + endpoint) {
					return nil, errors.New("injected endpoint failure")
				}
				switch endpoint {
				case "repos/acme/app":
					return []byte(`{"id":1,"default_branch":"main"}`), nil
				case "repos/acme/app/branches/main":
					return []byte(`{"commit":{"sha":"same"}}`), nil
				case "repos/acme/app/pages":
					branch := "master"
					if test.automatic || updated {
						branch = "main"
					}
					if updated && test.failure == "post-mismatch" {
						injected = true
						branch = "master"
					}
					return []byte(`{"build_type":"legacy","source":{"branch":"` + branch + `","path":"/"}}`), nil
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
			defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				if got := strings.Join(args, " "); got != "api --method PUT repos/acme/app/pages -f source[branch]=main -f source[path]=/" {
					t.Fatalf("unmodelled Pages mutation %q", got)
				}
				if failAt("put") {
					return githubobserver.CommandResponse{Err: errors.New("injected Pages update failure")}
				}
				updated = true
				return githubobserver.CommandResponse{}
			}
			repo := defaultBranchRepository{Repository: "acme/app", Desired: "main", ObservedDefault: "master", OldHead: "same", Disposition: "drift", PagesBefore: &defaultBranchPagesSource{BuildType: "legacy", Branch: "master", Path: "/"}}
			result := applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, func(defaultBranchRepository) error {
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
	planned := defaultBranchRepository{Repository: "acme/app", Desired: "main", Disposition: "compliant"}
	called := false
	actual := applyDefaultBranchPagesWithCheckpoint(context.Background(), planned, func(defaultBranchRepository) error {
		called = true
		return errors.New("should not persist")
	})
	if called || !reflect.DeepEqual(actual, planned) {
		t.Fatalf("repository without Pages changed: actual=%+v checkpoint=%t", actual, called)
	}
}

func TestDefaultBranchPagesAtDesiredClassifiesObservedSource(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			oldRead := defaultBranchRead
			t.Cleanup(func() { defaultBranchRead = oldRead })
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				if endpoint != "repos/acme/app/pages" {
					t.Fatalf("unexpected endpoint %q", endpoint)
				}
				return []byte(test.body), test.readErr
			}
			result := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			inspectDefaultBranchPagesAtDesired(context.Background(), &result, defaultBranchRepoMetadata{ID: 1, DefaultBranch: "main"})
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
