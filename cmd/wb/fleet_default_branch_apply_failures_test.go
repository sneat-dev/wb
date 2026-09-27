package main

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

func TestDefaultBranchCLIRejectsUnsafeArchiveAndPagesFlagCombinations(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, want string
		args       []string
	}{
		{"restore requires apply", "--restore-archive-from requires --apply", []string{"--restore-archive-from", "receipt.json"}},
		{"restore requires digest", "--restore-archive-sha256", []string{"--apply", "--repo", "acme/app", "--restore-archive-from", "receipt.json"}},
		{"orphan restore digest", "--restore-archive-sha256 requires", []string{"--restore-archive-sha256", digest}},
		{"restore excludes migration flags", "requires exactly one --repo", []string{"--apply", "--repo", "acme/app", "--restore-archive-from", "receipt.json", "--restore-archive-sha256", digest, "--branch", "main"}},
		{"Pages migration excludes archived transition", "--migrate-pages-source does not support archived", []string{"--repo", "acme/app", "--migrate-pages-source", "--temporarily-unarchive"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := newRootCmd()
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs(append([]string{"fleet", "default-branch"}, test.args...))
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) || stdout.Len() != 0 {
				t.Fatalf("unsafe args %q: stdout=%q stderr=%q err=%v, want %q", test.args, stdout.String(), stderr.String(), err, test.want)
			}
		})
	}
}

func TestRunDefaultBranchRejectsInvalidInputsBeforeRemoteInspection(t *testing.T) {
	for _, test := range []struct {
		name, want string
		prepare    func(*testing.T) (*invocation, defaultBranchOptions)
	}{
		{
			name: "invalid policy", want: "parse WB config",
			prepare: func(t *testing.T) (*invocation, defaultBranchOptions) {
				path := filepath.Join(t.TempDir(), "policy.yaml")
				if err := os.WriteFile(path, []byte("default_branch: ["), 0o600); err != nil {
					t.Fatal(err)
				}
				defaultBranchConfigPath = func() string { return path }
				return &invocation{}, defaultBranchOptions{repositories: []string{"acme/app"}, parallel: 1}
			},
		},
		{
			name: "invalid recovery receipt", want: "read --reconcile-from report",
			prepare: func(t *testing.T) (*invocation, defaultBranchOptions) {
				return &invocation{}, defaultBranchOptions{reconcileFrom: filepath.Join(t.TempDir(), "missing.json"), reconcileSHA256: strings.Repeat("a", 64), repositories: []string{"acme/app"}, parallel: 1}
			},
		},
		{
			name: "invalid repository scope", want: "invalid --repo",
			prepare: func(t *testing.T) (*invocation, defaultBranchOptions) {
				return &invocation{}, defaultBranchOptions{repositories: []string{"not-a-repository"}, parallel: 1}
			},
		},
		{
			name: "unreadable local clone root", want: "scan local canonical clones",
			prepare: func(t *testing.T) (*invocation, defaultBranchOptions) {
				path := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
				return &invocation{projectsRoot: path}, defaultBranchOptions{repositories: []string{"acme/app"}, parallel: 1}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldConfig, oldRead := defaultBranchConfigPath, defaultBranchRead
			t.Cleanup(func() { defaultBranchConfigPath, defaultBranchRead = oldConfig, oldRead })
			defaultBranchConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
			defaultBranchRead = func(context.Context, string) ([]byte, error) {
				t.Fatal("remote inspection reached after invalid preflight input")
				return nil, nil
			}
			inv, options := test.prepare(t)
			report, err := runDefaultBranch(context.Background(), inv, options, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) || len(report.Repositories) != 0 {
				t.Fatalf("invalid %s: report=%+v err=%v, want %q", test.name, report, err, test.want)
			}
		})
	}
}

func TestDefaultBranchCLIReportsRemoteFailureAndWriterFailure(t *testing.T) {
	oldConfig, oldRead := defaultBranchConfigPath, defaultBranchRead
	t.Cleanup(func() { defaultBranchConfigPath, defaultBranchRead = oldConfig, oldRead })
	defaultBranchConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	reads := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		reads++
		if endpoint != "repos/acme/app" {
			t.Fatalf("unexpected remote read %q", endpoint)
		}
		return nil, errors.New("injected metadata failure")
	}
	for _, test := range []struct {
		name string
		args []string
		out  *bytes.Buffer
		want string
	}{
		{name: "text", out: new(bytes.Buffer), want: "default-branch findings remain"},
		{name: "json", args: []string{"--json"}, out: new(bytes.Buffer), want: "default-branch findings remain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := newRootCmd()
			command.SetOut(test.out)
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append([]string{"--projects-root", t.TempDir(), "fleet", "default-branch", "--repo", "acme/app", "--branch", "main", "--parallel", "1"}, test.args...))
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(test.out.String(), "acme/app") || !strings.Contains(test.out.String(), "injected metadata failure") {
				t.Fatalf("command output=%q err=%v, want report and %q", test.out.String(), err, test.want)
			}
		})
	}
	if reads != 2 {
		t.Fatalf("remote metadata reads=%d, want one per command", reads)
	}
	for _, format := range []string{"text", "json"} {
		t.Run("writer failure "+format, func(t *testing.T) {
			command := newRootCmd()
			writer := &defaultBranchFailWriter{failAt: 1}
			command.SetOut(writer)
			command.SetErr(&bytes.Buffer{})
			args := []string{"--projects-root", t.TempDir(), "fleet", "default-branch", "--repo", "acme/app", "--branch", "main", "--parallel", "1"}
			if format == "json" {
				args = append(args, "--json")
			}
			command.SetArgs(args)
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") || writer.writes != 1 {
				t.Fatalf("writer failure: writes=%d err=%v", writer.writes, err)
			}
		})
	}
	if reads != 4 {
		t.Fatalf("remote metadata reads=%d, want one per command", reads)
	}
}

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
