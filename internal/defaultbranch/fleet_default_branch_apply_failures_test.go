package defaultbranch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestRunDefaultBranchRejectsInvalidInputsBeforeRemoteInspection(t *testing.T) {
	service := New()

	for _, test := range []struct {
		name, want string
		prepare    func(*testing.T) (Scope, Options)
	}{
		{
			name: "invalid policy", want: "parse WB config",
			prepare: func(t *testing.T) (Scope, Options) {
				path := filepath.Join(t.TempDir(), "policy.yaml")
				if err := os.WriteFile(path, []byte("default_branch: ["), 0o600); err != nil {
					t.Fatal(err)
				}
				service.deps.ConfigPath = func() string { return path }
				return Scope{}, Options{Repositories: []string{"acme/app"}, Parallel: 1}
			},
		},
		{
			name: "invalid recovery receipt", want: "read --reconcile-from report",
			prepare: func(t *testing.T) (Scope, Options) {
				return Scope{}, Options{ReconcileFrom: filepath.Join(t.TempDir(), "missing.json"), ReconcileSHA256: strings.Repeat("a", 64), Repositories: []string{"acme/app"}, Parallel: 1}
			},
		},
		{
			name: "invalid repository scope", want: "invalid --repo",
			prepare: func(t *testing.T) (Scope, Options) {
				return Scope{}, Options{Repositories: []string{"not-a-repository"}, Parallel: 1}
			},
		},
		{
			name: "unreadable local clone root", want: "scan local canonical clones",
			prepare: func(t *testing.T) (Scope, Options) {
				path := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				return Scope{ProjectsRoot: path}, Options{Repositories: []string{"acme/app"}, Parallel: 1}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldConfig, oldRead := service.deps.ConfigPath, service.deps.Read
			t.Cleanup(func() { service.deps.ConfigPath, service.deps.Read = oldConfig, oldRead })
			service.deps.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
			service.deps.Read = func(context.Context, string) ([]byte, error) {
				t.Fatal("remote inspection reached after invalid preflight input")
				return nil, nil
			}
			inv, options := test.prepare(t)
			report, err := service.Run(context.Background(), Request{Scope: inv, Options: options}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) || len(report.Repositories) != 0 {
				t.Fatalf("invalid %s: report=%+v err=%v, want %q", test.name, report, err, test.want)
			}
		})
	}
}

func TestDefaultBranchApplyRefusesFreshComplianceAndInterruptedVisibility(t *testing.T) {
	service := New()

	for _, test := range []struct {
		name, observed, wantDisposition, wantError string
		waitFails                                  bool
		wantMutations, wantCheckpoints             int
	}{
		{name: "already changed before mutation", observed: "main", wantDisposition: "compliant"},
		{name: "visibility wait interrupted", observed: "master", waitFails: true, wantDisposition: "error", wantError: "wait for renamed branch visibility", wantMutations: 1, wantCheckpoints: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldRead, oldExecute, oldWait, oldNow := service.deps.Read, service.deps.Execute, service.deps.RenameWait, service.deps.RenameNow
			t.Cleanup(func() {
				service.deps.Read, service.deps.Execute, service.deps.RenameWait, service.deps.RenameNow = oldRead, oldExecute, oldWait, oldNow
			})
			reads, mutations, checkpoints, waits := 0, 0, 0, 0
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				reads++
				switch endpoint {
				case "repos/acme/app":
					return []byte(`{"id":1,"default_branch":"` + test.observed + `"}`), nil
				case "repos/acme/app/branches/master", "repos/acme/app/branches/main":
					if endpoint == "repos/acme/app/branches/main" && test.observed == "master" {
						return nil, errors.New("HTTP 404")
					}
					return []byte(`{"commit":{"sha":"same"}}`), nil
				case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/rules/branches/master?per_page=100":
					return []byte(`[]`), nil
				case "repos/acme/app/contents/.github/workflows?ref=master", "repos/acme/app/pages", "repos/acme/app/branches/master/protection":
					return nil, errors.New("HTTP 404")
				default:
					t.Fatalf("unmodelled apply endpoint %q", endpoint)
					return nil, nil
				}
			}
			service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				mutations++
				if got := strings.Join(args, " "); got != "api --method POST repos/acme/app/branches/master/rename -f new_name=main" {
					t.Fatalf("unexpected mutation %q", got)
				}
				return githubobserver.CommandResponse{}
			}
			service.deps.RenameNow = func() time.Time { return time.Now() }
			service.deps.RenameWait = func(context.Context, time.Duration) error {
				waits++
				if !test.waitFails {
					t.Fatal("unexpected visibility wait")
				}
				return errors.New("observation interrupted")
			}
			planned := Repository{Repository: "acme/app", Desired: "main", ObservedDefault: "master", OldHead: "same", Disposition: "drift"}
			result := service.applyDefaultBranchWithCheckpoint(context.Background(), planned, func(Repository) error { checkpoints++; return nil })
			if result.Disposition != test.wantDisposition || !strings.Contains(result.Error, test.wantError) || mutations != test.wantMutations || checkpoints != test.wantCheckpoints || reads == 0 {
				t.Fatalf("apply result=%+v mutations=%d checkpoints=%d reads=%d waits=%d; want disposition=%q error=%q", result, mutations, checkpoints, reads, waits, test.wantDisposition, test.wantError)
			}
			if test.waitFails && (waits != 1 || !result.RenameAccepted) {
				t.Fatalf("interrupted wait did not preserve accepted mutation: %+v waits=%d", result, waits)
			}
		})
	}
}

func TestDefaultBranchReconcileReportRefusesMissingChangedAndMalformedEvidence(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.json")
	if report, err := readDefaultBranchReport(missing, strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "read --reconcile-from report") || report != nil {
		t.Fatalf("missing report = %+v, %v", report, err)
	}
	path := filepath.Join(root, "receipt.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report, err := readDefaultBranchReport(path, strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") || report != nil {
		t.Fatalf("changed report = %+v, %v", report, err)
	}
	if report, err := readDefaultBranchReport(path, defaultBranchDigest([]byte("{"))); err == nil || !strings.Contains(err.Error(), "decode --reconcile-from report") || report != nil {
		t.Fatalf("malformed report = %+v, %v", report, err)
	}
}
