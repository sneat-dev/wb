package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestDefaultBranchApplyRefusesFreshComplianceAndInterruptedVisibility(t *testing.T) {
	for _, test := range []struct {
		name, observed, wantDisposition, wantError string
		waitFails                                  bool
		wantMutations, wantCheckpoints             int
	}{
		{name: "already changed before mutation", observed: "main", wantDisposition: "compliant"},
		{name: "visibility wait interrupted", observed: "master", waitFails: true, wantDisposition: "error", wantError: "wait for renamed branch visibility", wantMutations: 1, wantCheckpoints: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldRead, oldExecute, oldWait, oldNow := defaultBranchRead, defaultBranchExecute, defaultBranchRenameWait, defaultBranchRenameNow
			t.Cleanup(func() {
				defaultBranchRead, defaultBranchExecute, defaultBranchRenameWait, defaultBranchRenameNow = oldRead, oldExecute, oldWait, oldNow
			})
			reads, mutations, checkpoints, waits := 0, 0, 0, 0
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
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
			defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				mutations++
				if got := strings.Join(args, " "); got != "api --method POST repos/acme/app/branches/master/rename -f new_name=main" {
					t.Fatalf("unexpected mutation %q", got)
				}
				return githubobserver.CommandResponse{}
			}
			defaultBranchRenameNow = func() time.Time { return time.Now() }
			defaultBranchRenameWait = func(context.Context, time.Duration) error {
				waits++
				if !test.waitFails {
					t.Fatal("unexpected visibility wait")
				}
				return errors.New("observation interrupted")
			}
			planned := defaultBranchRepository{Repository: "acme/app", Desired: "main", ObservedDefault: "master", OldHead: "same", Disposition: "drift"}
			result := applyDefaultBranchWithCheckpoint(context.Background(), planned, func(defaultBranchRepository) error { checkpoints++; return nil })
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
